package imap

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// folderIDs is every message ID the fake API holds in folder, ascending.
func folderIDs(api *fakeAPI, folder string) []uint {
	api.mu.Lock()
	defer api.mu.Unlock()
	var ids []uint
	for id, m := range api.msgs {
		if m.folder == folder {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func setTransferSession(t *testing.T, failAt int) (*fakeAPI, *transcript) {
	t.Helper()
	api := newFakeAPI()
	for _, id := range []uint{1, 2, 3, 4} {
		api.seed(id, "INBOX", "Subject: m\r\n\r\nbody\r\n", "m")
	}
	api.seed(9, "Archive", "Subject: old\r\n\r\nold\r\n", "old")
	api.transferFailAt = failAt
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("a1")
	if _, st := h.command("a2", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT = %q", st)
	}
	return api, h
}

// COPY and MOVE of a set are one API call that the API runs in one
// transaction. When it fails on the 3rd message the command answers NO and
// both folders hold what they held (RFC 3501 §6.4.7, RFC 6851 §3.3); no
// message is copied or deleted one at a time.
func TestServer_SetTransferFailureChangesNothing(t *testing.T) {
	for _, command := range []string{"COPY 1:4 Archive", "UID COPY 1:4 Archive", "MOVE 1:4 Archive", "UID MOVE 1:4 Archive"} {
		t.Run(command, func(t *testing.T) {
			api, h := setTransferSession(t, 3)
			unt, st := h.command("c1", "%s", command)
			if !strings.HasPrefix(st, "c1 NO") || len(unt) != 0 {
				t.Fatalf("%s = %q %q, want a bare NO", command, unt, st)
			}
			if got := folderIDs(api, "Archive"); !slices.Equal(got, []uint{9}) {
				t.Fatalf("Archive holds %v after the failed %s", got, command)
			}
			if got := folderIDs(api, "INBOX"); !slices.Equal(got, []uint{1, 2, 3, 4}) {
				t.Fatalf("INBOX holds %v after the failed %s", got, command)
			}
			if len(api.transfers) != 1 || len(api.imports) != 0 || len(api.deletes) != 0 {
				t.Fatalf("API saw %d transfers, %d imports, %d deletes; want one transfer", len(api.transfers), len(api.imports), len(api.deletes))
			}
		})
	}
}

// A set copy and a set move that succeed report COPYUID for every message
// from the IDs the API returned; the move expunges each one.
func TestServer_SetTransferReportsEveryUID(t *testing.T) {
	api, h := setTransferSession(t, 0)
	_, st := h.command("c1", "COPY 3,1 Archive")
	if st != "c1 OK [COPYUID 1 3,1 100,101] COPY completed" {
		t.Fatalf("COPY = %q", st)
	}
	unt, st := h.command("c2", "UID MOVE 2,4 Archive")
	want := []string{"* OK [COPYUID 1 2,4 102,103] Moved", "* 4 EXPUNGE", "* 2 EXPUNGE"}
	if !strings.HasPrefix(st, "c2 OK") || !slices.Equal(unt, want) {
		t.Fatalf("UID MOVE = %q %q, want %q", unt, st, want)
	}
	if got := folderIDs(api, "Archive"); !slices.Equal(got, []uint{9, 100, 101, 102, 103}) {
		t.Fatalf("Archive holds %v", got)
	}
	if got := folderIDs(api, "INBOX"); !slices.Equal(got, []uint{1, 3}) {
		t.Fatalf("INBOX holds %v", got)
	}
	if len(api.transfers) != 2 || !api.transfers[1].Move || api.transfers[0].SourceFolder != "INBOX" ||
		!slices.Equal(api.transfers[0].MessageIDs, []uint{3, 1}) {
		t.Fatalf("transfers: %+v", api.transfers)
	}
}
