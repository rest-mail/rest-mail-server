package imap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// #2107 AC01: an APPEND's literal goes to a spool file, not memory, on its way from the
// client to the import operation, and the stored octets are what they were before.

// TestAC01AppendStoresEveryShapeByteForByte pins what the import receives for each form of
// APPEND a client sends: line endings become CRLF and every other octet is kept.
func TestAC01AppendStoresEveryShapeByteForByte(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, c := range []struct {
		name, command, literal, want string
		utf8                         bool
	}{
		{name: "CRLF", command: "APPEND INBOX {%d}",
			literal: "Subject: crlf\r\n\r\nline one\r\nline two\r\n",
			want:    "Subject: crlf\r\n\r\nline one\r\nline two\r\n"},
		{name: "bare LF", command: "APPEND INBOX {%d}",
			literal: "Subject: lf\n\nline one\nline two\n",
			want:    "Subject: lf\r\n\r\nline one\r\nline two\r\n"},
		{name: "mixed endings and bare CR", command: "APPEND INBOX {%d}",
			literal: "Subject: mixed\r\n\nA\rB\r\r\nC\n\rD",
			want:    "Subject: mixed\r\n\r\nA\rB\r\r\nC\r\n\rD"},
		{name: "8-bit body", command: "APPEND Archive (\\Seen) {%d}",
			literal: "Subject: eight\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\ncaf\xe9 \xff\xfe\x80\r\n",
			want:    "Subject: eight\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\ncaf\xe9 \xff\xfe\x80\r\n"},
		{name: "non-synchronizing literal with date-time", command: `APPEND INBOX "07-Feb-1994 22:43:04 -0800" {%d+}`,
			literal: "Subject: nonsync\n\nbody\n",
			want:    "Subject: nonsync\r\n\r\nbody\r\n"},
		{name: "UTF8 literal8", command: "APPEND INBOX UTF8 (~{%d}", utf8: true,
			literal: "Subject: Gr\xc3\xbc\xc3\x9fe\r\nFrom: J\xc3\xb6rg <j@example.test>\n\n\xe2\x9c\x93 done\n",
			want:    "Subject: Gr\xc3\xbc\xc3\x9fe\r\nFrom: J\xc3\xb6rg <j@example.test>\r\n\r\n\xe2\x9c\x93 done\r\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := newFakeAPI()
			srv := httptest.NewServer(api.handler())
			defer srv.Close()
			h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
			h.login("a1")
			if c.utf8 {
				if _, st := h.command("e1", "ENABLE UTF8=ACCEPT"); !strings.Contains(st, "OK") {
					t.Fatalf("ENABLE = %q", st)
				}
			}
			command := fmt.Sprintf(c.command, len(c.literal))
			h.send("a2 %s", command)
			if !strings.HasSuffix(command, "+}") {
				if cont := h.readLine(); !strings.HasPrefix(cont, "+") {
					t.Fatalf("continuation = %q", cont)
				}
			}
			end := "\r\n"
			if c.utf8 {
				end = ")\r\n"
			}
			h.writeLine(c.literal + end)
			if _, st := h.readResponse("a2"); !strings.Contains(st, "a2 OK") {
				t.Fatalf("APPEND = %q", st)
			}
			if got := string(api.lastImport(t).RawMessage); got != c.want {
				t.Fatalf("stored %q\nwant   %q", got, c.want)
			}
		})
	}
}

// streamedImportAPI is the fake API with an import route that never holds the request:
// it copies each body to a file, for the test to read once the APPEND is answered.
type streamedImportAPI struct {
	*fakeAPI
	dir    string
	mu     sync.Mutex
	bodies []string
	length []int64
}

func (a *streamedImportAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/accounts/{acct}/messages/import", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		name := filepath.Join(a.dir, fmt.Sprintf("body-%d", len(a.bodies)))
		a.bodies = append(a.bodies, name)
		a.length = append(a.length, r.ContentLength)
		a.mu.Unlock()
		file, err := os.Create(name)
		if err == nil {
			_, err = io.Copy(file, r.Body)
			_ = file.Close()
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"id":100,"imported":true,"folder":"INBOX"}}`)
	})
	mux.Handle("/", a.fakeAPI.handler())
	return mux
}

// TestAC01AppendHoldsNoWholeMessage measures what one APPEND of a 9 MiB message allocates,
// from the command line to the tagged OK, in the whole process: the IMAP session, the
// gateway's import call and the fake API's handler. Holding the literal whole costs its
// size several times over (the literal, its CRLF copy and its JSON encoding), so the
// bound, a tenth of the message, fails at once if any of those comes back.
func TestAC01AppendHoldsNoWholeMessage(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	api := &streamedImportAPI{fakeAPI: newFakeAPI(), dir: t.TempDir()}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("a1")

	const size = 9 << 20
	literal := bytes.Repeat([]byte("0123456789abcdefghijklmnopqrstu\n"), size/32)
	copy(literal, "Subject: large\n\n")
	command := fmt.Sprintf("a2 APPEND INBOX {%d}\r\n", len(literal))

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	h.writeLine(command)
	if cont := h.readLine(); !strings.HasPrefix(cont, "+") {
		t.Fatalf("continuation = %q", cont)
	}
	if _, err := h.cw.Write(literal); err != nil {
		t.Fatal(err)
	}
	h.writeLine("\r\n")
	_, st := h.readResponse("a2")
	runtime.ReadMemStats(&after)
	if !strings.Contains(st, "a2 OK") {
		t.Fatalf("APPEND = %q", st)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("APPEND of %d octets allocated %d bytes", len(literal), allocated)
	if allocated > size/10 {
		t.Errorf("APPEND of %d octets allocated %d bytes, want at most %d: the message was held whole", len(literal), allocated, size/10)
	}

	if len(api.bodies) != 1 {
		t.Fatalf("import calls = %d, want 1", len(api.bodies))
	}
	body, err := os.ReadFile(api.bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	if api.length[0] != int64(len(body)) {
		t.Errorf("Content-Length %d, body %d octets", api.length[0], len(body))
	}
	var req apiclient.ImportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if want := normalizeToCRLF(literal); !bytes.Equal(req.RawMessage, want) {
		t.Fatalf("import got %d octets, want the %d octets of the literal with CRLF endings", len(req.RawMessage), len(want))
	}
	if req.Folder != "INBOX" || !req.AlwaysStore || !req.MatchSubmission || req.InternalDate.IsZero() {
		t.Errorf("import fields = %+v", req)
	}
	syncSession(h)
	assertNoSpool(t, spool)
}

// syncSession waits for the session to finish the command before, whose tagged answer is
// sent before its spool file is removed.
func syncSession(h *transcript) {
	h.t.Helper()
	if _, st := h.command("s1", "NOOP"); !strings.HasPrefix(st, "s1 OK") {
		h.t.Fatalf("NOOP = %q", st)
	}
}

// assertNoSpool fails when dir, the test's TMPDIR, still holds a file.
func assertNoSpool(t *testing.T, dir string) {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("spool left %d file(s): %v", len(files), files)
	}
}

// TestAC01AppendSpoolIsRemoved proves no APPEND leaves its spool file behind: one the API
// stores, one it refuses, and one whose import panics.
func TestAC01AppendSpoolIsRemoved(t *testing.T) {
	msg := "Subject: spool\r\n\r\nbody\r\n"
	t.Run("stored", func(t *testing.T) {
		spool := t.TempDir()
		t.Setenv("TMPDIR", spool)
		api := newFakeAPI()
		srv := httptest.NewServer(api.handler())
		defer srv.Close()
		h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
		h.login("a1")
		if _, st := h.appendMsg("a2", "INBOX", msg); !strings.Contains(st, "a2 OK") {
			t.Fatalf("APPEND = %q", st)
		}
		syncSession(h)
		assertNoSpool(t, spool)
	})
	t.Run("refused", func(t *testing.T) {
		spool := t.TempDir()
		t.Setenv("TMPDIR", spool)
		api := newFakeAPI()
		api.importStatus = http.StatusInternalServerError
		srv := httptest.NewServer(api.handler())
		defer srv.Close()
		h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
		h.login("a1")
		if _, st := h.appendMsg("a2", "INBOX", msg); !strings.Contains(st, "a2 NO") {
			t.Fatalf("APPEND = %q, want NO", st)
		}
		syncSession(h)
		assertNoSpool(t, spool)
	})
	t.Run("panicked", func(t *testing.T) {
		spool := t.TempDir()
		t.Setenv("TMPDIR", spool)
		api := newFakeAPI()
		srv := httptest.NewServer(api.handler())
		defer srv.Close()
		panicky := apiclient.WithDoer(func(client *http.Client, req *http.Request, _ string) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/messages/import") {
				panic("import interrupted")
			}
			return client.Do(req)
		})
		h := newTranscript(t, NewBackend(apiclient.New(srv.URL, panicky), nil))
		h.login("a1")
		h.send("a2 APPEND INBOX {%d}", len(msg))
		if cont := h.readLine(); !strings.HasPrefix(cont, "+") {
			t.Fatalf("continuation = %q", cont)
		}
		h.writeLine(msg + "\r\n")
		if line := h.readLine(); !strings.HasPrefix(line, "* BYE") {
			t.Fatalf("after a panicking import = %q, want * BYE", line)
		}
		<-h.done
		assertNoSpool(t, spool)
	})
}

// TestAC01AppendOversizeRefusedAsBefore proves a literal over the APPEND limit is refused
// with the same response as before spooling, before any octet is invited or spooled.
func TestAC01AppendOversizeRefusedAsBefore(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	api := newFakeAPI()
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	h := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	h.login("a1")
	if _, st := h.command("a2", "APPEND INBOX {%d}", 10<<20+1); st != "a2 NO Message too large" {
		t.Fatalf("oversize APPEND = %q, want a2 NO Message too large", st)
	}
	if _, st := h.command("a3", "NOOP"); !strings.HasPrefix(st, "a3 OK") {
		t.Fatalf("NOOP after the refusal = %q", st)
	}
	assertNoSpool(t, spool)
}
