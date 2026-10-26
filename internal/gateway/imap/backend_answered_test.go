package imap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// Exercise the adapter's HTTP boundary: an Answered-only STORE must reach
// storage, survive the next listing, and travel with COPY and APPEND.
func TestAnsweredSurvivesStoreFetchCopyAndAppend(t *testing.T) {
	answered := false
	patches, deliveries := 0, 0
	date := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiclient.MessageListResponse{Data: []apiclient.MessageSummary{{ID: 41, IsAnswered: answered}}})
	})
	mux.HandleFunc("/api/v1/messages/41", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			var update apiclient.MessageUpdate
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if update.IsAnswered == nil || update.IsRead != nil || update.IsDraft != nil || update.IsFlagged != nil {
				t.Error("Answered-only STORE lost its field or changed other flags")
			}
			if update.IsAnswered != nil {
				answered = *update.IsAnswered
			}
			patches++
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(apiclient.MessageDetailResponse{Data: apiclient.MessageDetail{ID: 41, MailboxID: 12, IsAnswered: answered, ReceivedAt: date}})
	})
	mux.HandleFunc("/api/v1/messages/41/raw", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Subject: old\r\n\r\nhistory\r\n"))
	})
	mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, r *http.Request) {
		var delivery apiclient.DeliverRequest
		if err := json.NewDecoder(r.Body).Decode(&delivery); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if delivery.IsAnswered == nil || !*delivery.IsAnswered {
			t.Error("COPY/APPEND dropped Answered")
		}
		if deliveries == 0 && (delivery.ReceivedAt == nil || !delivery.ReceivedAt.Equal(date)) {
			t.Error("COPY changed INTERNALDATE")
		}
		deliveries++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":42}}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	m := &mailbox{api: apiclient.New(server.URL), email: "user@example.test", accountID: 12, token: "test"}
	for _, value := range []bool{true, false, true} {
		if err := m.Store(41, goimap.FlagUpdate{Answered: &value}); err != nil {
			t.Fatal(err)
		}
		if got := fetchFlags(t, m).Answered; got != value {
			t.Fatalf("Answered=%t, want%t", got, value)
		}
	}
	if _, err := m.CopyUID(41, "Archive"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendUID("Archive", goimap.FlagUpdate{Answered: boolp(true)}, []byte("Subject: append\r\n\r\nhistory\r\n")); err != nil {
		t.Fatal(err)
	}
	if patches != 3 || deliveries != 2 {
		t.Fatalf("HTTP calls: patches=%d deliveries=%d", patches, deliveries)
	}
}
