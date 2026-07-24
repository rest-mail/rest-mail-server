package imap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	imapsrv "github.com/rest-mail/go-imap"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

func boolp(b bool) *bool { return &b }

// flagState is the mutable server-side flag state of one message: the miniature
// REST API the IMAP adapter talks to. PATCH /api/v1/messages/{id} mutates only
// the keys present in the body (mirroring the real UpdateMessage handler), and
// the folder listing reads the current state back.
type flagState struct {
	id        uint
	isRead    bool
	isFlagged bool
	isStarred bool
	isDraft   bool
}

// newFlagMockMailbox wires a mailbox to an httptest server backed by st, so a
// STORE (PATCH) followed by a FETCH (folder listing) exercises the adapter's
// full round-trip flag mapping.
func newFlagMockMailbox(t *testing.T, st *flagState) *mailbox {
	t.Helper()
	mux := http.NewServeMux()

	// Folder listing → the single message's current flag state.
	mux.HandleFunc("/api/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		resp := apiclient.MessageListResponse{
			Data: []apiclient.MessageSummary{{
				ID:        st.id,
				IsRead:    st.isRead,
				IsFlagged: st.isFlagged,
				IsStarred: st.isStarred,
				IsDraft:   st.isDraft,
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Flag update → apply exactly the keys the adapter sent, as the real
	// UpdateMessage handler does (only present keys are written).
	mux.HandleFunc("/api/v1/messages/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["is_read"].(bool); ok {
			st.isRead = v
		}
		if v, ok := body["is_flagged"].(bool); ok {
			st.isFlagged = v
		}
		if v, ok := body["is_starred"].(bool); ok {
			st.isStarred = v
		}
		if v, ok := body["is_draft"].(bool); ok {
			st.isDraft = v
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &mailbox{api: apiclient.New(srv.URL), token: "tok"}
}

// fetchFlags SELECTs the folder and returns the single message's neutral flags
// as the IMAP engine would see them.
func fetchFlags(t *testing.T, m *mailbox) imapsrv.Message {
	t.Helper()
	msgs, err := m.Messages("INBOX")
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	return msgs[0]
}

// TestStore_FlaggedClearIsSymmetric is the red-green regression for the
// asymmetric \Flagged mapping in issue #197. A webmail-starred message folds
// into \Flagged on FETCH (is_flagged || is_starred). STORE -FLAGS \Flagged must
// therefore clear the star as well, or the message re-appears \Flagged on the
// next SELECT — what you STORE is not what you FETCH back.
func TestStore_FlaggedClearIsSymmetric(t *testing.T) {
	st := &flagState{id: 1, isStarred: true} // webmail-starred, is_flagged=false
	m := newFlagMockMailbox(t, st)

	if !fetchFlags(t, m).Flagged {
		t.Fatal("precondition: a webmail-starred message must FETCH as \\Flagged")
	}

	// STORE -FLAGS \Flagged
	if err := m.Store(1, imapsrv.FlagUpdate{Flagged: boolp(false)}); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if fetchFlags(t, m).Flagged {
		t.Fatal("after STORE -FLAGS \\Flagged the message must FETCH un-flagged; is_starred was left set, so it re-appears flagged")
	}
}

// TestStore_FlaggedSetThenClearRoundTrips pins the full \Flagged round-trip in
// both directions on a plain (never-starred) message: +FLAGS then -FLAGS must
// leave it exactly as STOREd at each step.
func TestStore_FlaggedSetThenClearRoundTrips(t *testing.T) {
	st := &flagState{id: 1}
	m := newFlagMockMailbox(t, st)

	if fetchFlags(t, m).Flagged {
		t.Fatal("precondition: message starts un-flagged")
	}
	if err := m.Store(1, imapsrv.FlagUpdate{Flagged: boolp(true)}); err != nil {
		t.Fatalf("Store +Flagged: %v", err)
	}
	if !fetchFlags(t, m).Flagged {
		t.Fatal("after STORE +FLAGS \\Flagged the message must FETCH as \\Flagged")
	}
	if err := m.Store(1, imapsrv.FlagUpdate{Flagged: boolp(false)}); err != nil {
		t.Fatalf("Store -Flagged: %v", err)
	}
	if fetchFlags(t, m).Flagged {
		t.Fatal("after STORE -FLAGS \\Flagged the message must FETCH un-flagged")
	}
}
