package imap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// Revoked mail:import and mail:write grants refuse APPEND and COPY respectively
// with a tagged NO that stores nothing. Restoring each grant makes its command
// work again, without restoring the other grant.
func TestServer_AppendAndCopyRefusedWithoutTheirGrants(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", "Subject: Seed\r\n\r\nseed body\r\n", "Seed")
	api.importStatus = http.StatusForbidden
	api.importBody = map[string]any{"error": map[string]string{"code": "forbidden", "message": "Not allowed"}}
	api.transferStatus, api.transferBody = api.importStatus, api.importBody
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("a1")
	if _, st := h.command("a2", "SELECT INBOX"); !strings.Contains(st, "OK") {
		t.Fatalf("SELECT status = %q", st)
	}
	if _, st := h.appendMsg("a3", "INBOX", "Subject: Appended\r\n\r\nhi\r\n"); !strings.HasPrefix(st, "a3 NO") {
		t.Errorf("APPEND without mail:import = %q, want a3 NO", st)
	}
	if _, st := h.command("a4", "COPY 1 Archive"); !strings.HasPrefix(st, "a4 NO") {
		t.Errorf("COPY without mail:write = %q, want a4 NO", st)
	}
	for id := uint(100); id < 110; id++ {
		if folderOf(api, id) != "" {
			t.Fatalf("a refused import stored message %d", id)
		}
	}

	api.mu.Lock()
	api.importStatus, api.importBody = 0, nil
	api.mu.Unlock()
	if _, st := h.appendMsg("a5", "INBOX", "Subject: Appended\r\n\r\nhi\r\n"); !strings.Contains(st, "OK") {
		t.Errorf("APPEND with mail:import restored = %q, want OK", st)
	}
	if _, st := h.command("c1", "COPY 1 Archive"); !strings.HasPrefix(st, "c1 NO") {
		t.Errorf("COPY with only mail:import restored = %q, want c1 NO", st)
	}
	api.mu.Lock()
	api.transferStatus, api.transferBody = 0, nil
	api.importStatus = http.StatusForbidden
	api.mu.Unlock()
	if _, st := h.command("a6", "COPY 1 Archive"); !strings.Contains(st, "OK") {
		t.Errorf("COPY with only mail:write restored = %q, want OK", st)
	}
	if _, st := h.appendMsg("a7", "INBOX", "Subject: Still refused\r\n\r\nhi\r\n"); !strings.HasPrefix(st, "a7 NO") {
		t.Errorf("APPEND with only mail:write restored = %q, want a7 NO", st)
	}
}
