package imap

import (
	"bytes"
	"net/http"
	"reflect"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// TestAC01AppendFromMatchesTheInMemoryImport proves the spooled APPEND (AppendFrom) asks
// the API for exactly what the in-memory APPEND asked before #2107: the same folder name,
// octet for octet, a decomposed (NFD) one included, which the API, not the gateway,
// converts; the same fields; and, when the API refuses, the same error, so the client is
// answered as before.
func TestAC01AppendFromMatchesTheInMemoryImport(t *testing.T) {
	const raw = "Subject: parity\n\nbody\n"
	date := time.Date(2019, 6, 7, 8, 9, 10, 0, time.UTC)
	seen := true
	flags := goimap.FlagUpdate{Seen: &seen, Keywords: []string{"$Label"}}
	// The request the in-memory path built, as appendAt built it before the spool.
	inMemory := func(m *mailbox, folder string) error {
		_, err := m.importMessage(m.accountID, &apiclient.ImportRequest{
			RawMessage: normalizeToCRLF([]byte(raw)), Folder: folder, Flags: importFlags(flags),
			InternalDate: date, AlwaysStore: true, MatchSubmission: true,
		})
		return err
	}
	streamed := func(m *mailbox, folder string) error {
		_, err := m.AppendFrom(folder, flags, date, bytes.NewReader([]byte(raw)), int64(len(raw)))
		return err
	}

	for _, folder := range []string{"Café", "Café", "INBOX", "Archive/Tourné"} {
		api := newFakeAPI()
		m := newUnitMailbox(t, api)
		if err := inMemory(m, folder); err != nil {
			t.Fatalf("%q: in-memory import: %v", folder, err)
		}
		if err := streamed(m, folder); err != nil {
			t.Fatalf("%q: AppendFrom: %v", folder, err)
		}
		api.mu.Lock()
		imports := append([]apiclient.ImportRequest(nil), api.imports...)
		api.mu.Unlock()
		if len(imports) != 2 {
			t.Fatalf("%q: imports = %d, want 2", folder, len(imports))
		}
		if imports[1].Folder != folder {
			t.Errorf("AppendFrom sent folder %+q, want %+q as given", imports[1].Folder, folder)
		}
		if !reflect.DeepEqual(imports[0], imports[1]) {
			t.Errorf("%q: AppendFrom sent %+v\nthe in-memory import sent %+v", folder, imports[1], imports[0])
		}
	}

	for _, refusal := range []struct {
		status int
		body   any
	}{
		{http.StatusForbidden, map[string]any{"error": map[string]string{"code": "forbidden", "message": "Insufficient permissions"}}},
		{http.StatusNotFound, map[string]any{"error": map[string]string{"code": "folder_not_found", "message": "Folder not found"}}},
		{http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]string{"code": "too_large", "message": "Message too large"}}},
		{http.StatusInternalServerError, nil},
	} {
		api := newFakeAPI()
		api.importStatus, api.importBody = refusal.status, refusal.body
		m := newUnitMailbox(t, api)
		before, after := inMemory(m, "Café"), streamed(m, "Café")
		if before == nil || after == nil || before.Error() != after.Error() || reflect.TypeOf(before) != reflect.TypeOf(after) {
			t.Errorf("status %d: AppendFrom error %v, want the in-memory import's %v", refusal.status, after, before)
		}
	}
}
