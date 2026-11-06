package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

// fakeImportSessions is the console's fake API with import sessions (#1498): it opens and
// closes sessions, and checks that every import names the run's open session.
type fakeImportSessions struct {
	t      *testing.T
	mu     sync.Mutex
	opened []apitypes.OpenImportSessionBody
	closed []string
	open   map[string]int
}

// withImportSessions serves the session routes and hands every import to imports.
func withImportSessions(t *testing.T, imports http.Handler) (*fakeImportSessions, http.Handler) {
	f := &fakeImportSessions{t: t, open: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/accounts/{account}/messages/import-sessions", func(w http.ResponseWriter, r *http.Request) {
		var body apitypes.OpenImportSessionBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		f.opened = append(f.opened, body)
		id := fmt.Sprintf("%032x", len(f.opened))
		f.open[id] = 0
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(endpoint.Data[apitypes.ImportSession]{Data: apitypes.ImportSession{ID: id}})
	})
	mux.HandleFunc("DELETE /api/v1/accounts/{account}/messages/import-sessions/{session}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("session")
		f.mu.Lock()
		n, ok := f.open[id]
		delete(f.open, id)
		f.closed = append(f.closed, id)
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(endpoint.Data[apitypes.ImportSessionSummary]{Data: apitypes.ImportSessionSummary{ID: id, Imported: int64(n)}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages/import") {
			id := r.URL.Query().Get("import_session")
			f.mu.Lock()
			_, ok := f.open[id]
			if ok {
				f.open[id]++
			}
			f.mu.Unlock()
			if !ok {
				t.Errorf("import without the run's open session: %q", r.URL.RawQuery)
			}
		}
		imports.ServeHTTP(w, r)
	})
	return f, mux
}

// importSessionServer is a fake API whose imports go to imports, with import sessions.
func importSessionServer(t *testing.T, imports http.Handler) *httptest.Server {
	_, handler := withImportSessions(t, imports)
	return httptest.NewServer(handler)
}

// runs answers how many sessions were opened, and fails when one was left open.
func (f *fakeImportSessions) runs() int {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.open) != 0 {
		f.t.Fatalf("sessions left open: %v", f.open)
	}
	return len(f.opened)
}

// One run of import-mbox is one session: opened once, named on every import, and
// closed at the end with its summary printed, also when the run fails part way.
func TestImportRunIsOneSession(t *testing.T) {
	_, raw, _ := mailImportSource(t)
	path := t.TempDir() + "/history.mbox"
	mbox := "From old@example.test Thu Jan  2 01:04:05 2020\n" + string(raw) + "\nFrom old@example.test Thu Jan  2 01:04:05 2020\n" + strings.Replace(string(raw), "history@", "second@", 1) + "\n"
	if err := os.WriteFile(path, []byte(mbox), 0o600); err != nil {
		t.Fatal(err)
	}
	imports := 0
	sessions, handler := withImportSessions(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imports++
		var body apitypes.ImportMessageBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"id":%d,"imported":true,"message_id":"<history@example.test>","folder":%q}}`, 40+imports, body.Folder)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()
	var out, stderr bytes.Buffer
	code := runImportMail(context.Background(), "mbox", []string{"--server", srv.URL, "--account-id", "12", "--path", path, "--mbox-format", "mboxrd", "--folder", "History"}, "private-test-key", srv.Client(), &out, &stderr)
	if code != exitOK || imports != 2 || sessions.runs() != 1 {
		t.Fatalf("code=%d imports=%d sessions=%d out=%s err=%s", code, imports, len(sessions.opened), &out, &stderr)
	}
	if sessions.opened[0].ClientName != "restmail import-mbox" || len(sessions.closed) != 1 {
		t.Fatalf("opened %+v closed %v", sessions.opened, sessions.closed)
	}
	if !strings.Contains(out.String(), "Import summary: 2 imported, 0 deduplicated, 0 refused") || !strings.Contains(out.String(), sessions.closed[0]) {
		t.Fatalf("summary not printed: %s", &out)
	}

	// A run that fails part way still closes its session, and says what it did.
	failing, handler := withImportSessions(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	bad := httptest.NewServer(handler)
	defer bad.Close()
	out.Reset()
	stderr.Reset()
	code = runImportMail(context.Background(), "mbox", []string{"--server", bad.URL, "--account-id", "12", "--path", path, "--mbox-format", "mboxrd", "--folder", "History"}, "private-test-key", bad.Client(), &out, &stderr)
	if code != exitFailed || failing.runs() != 1 || len(failing.closed) != 1 || !strings.Contains(stderr.String(), "Import summary: ") || !strings.Contains(stderr.String(), "server refused import (HTTP 500)") {
		t.Fatalf("failed run: code=%d closed=%v out=%s err=%s", code, failing.closed, &out, &stderr)
	}

	// An interrupted run (its context ends during the first import) closes its session too.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupted, handler := withImportSessions(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":41,"imported":true,"message_id":"<history@example.test>","folder":"History"}}`))
	}))
	stopped := httptest.NewServer(handler)
	defer stopped.Close()
	out.Reset()
	code = runImportMail(ctx, "mbox", []string{"--server", stopped.URL, "--account-id", "12", "--path", path, "--mbox-format", "mboxrd", "--folder", "History", "--json"}, "private-test-key", stopped.Client(), &out, &stderr)
	if code != exitFailed || len(interrupted.closed) != 1 || interrupted.runs() != 1 {
		t.Fatalf("interrupted run: code=%d opened=%d closed=%v err=%s", code, len(interrupted.opened), interrupted.closed, &stderr)
	}
}
