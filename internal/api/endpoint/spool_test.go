package endpoint

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/go-chi/chi/v5"
)

func TestSpoolBodyHoldsTheBodyInAFileUntilDone(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	file, done, err := SpoolBody(r, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("read %q, %v", got, err)
	}
	done()
	if _, err := os.Stat(file.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file stayed: %v", err)
	}
}

func TestSpoolBodyRefusesWhatABoundBodyWould(t *testing.T) {
	status := func(err error) int {
		var refusal *Error
		if !errors.As(err, &refusal) {
			t.Fatalf("not a refusal: %v", err)
		}
		return refusal.Status
	}
	text := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	text.Header.Set("Content-Type", "text/plain")
	if _, _, err := SpoolBody(text, "application/json"); status(err) != http.StatusBadRequest {
		t.Fatalf("media type: %v", err)
	}

	large := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("too long"))
	large.Body = http.MaxBytesReader(httptest.NewRecorder(), large.Body, 3)
	if _, _, err := SpoolBody(large, "application/json"); status(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %v", err)
	}
}

// A route that takes several media types leaves the check to CheckSpooled, so SpoolBody takes
// its body whatever the request says of it.
func TestSpoolBodyWithNoMediaLeavesTheTypeAlone(t *testing.T) {
	for _, declared := range []string{"", "text/plain", "a b/c; ="} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
		r.Header.Set("Content-Type", declared)
		file, done, err := SpoolBody(r, "")
		if err != nil {
			t.Fatalf("%q: %v", declared, err)
		}
		done()
		if _, err := os.Stat(file.Name()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the file stayed: %v", err)
		}
	}
}

// The body failing is the client's refusal; the server's own disk failing is not, and leaves
// nothing behind.
func TestSpoolBodyAnswersADiskFailureAsItIs(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))
	_, _, err := SpoolBody(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")), "")
	var refusal *Error
	if err == nil || errors.As(err, &refusal) {
		t.Fatalf("got %v, want the disk's error and not a refusal", err)
	}
}

type spoolForms struct {
	JSON *struct {
		A string `json:"a"`
	} `body:"application/one+json,application/json"`
	Raw *[]byte `body:"application/raw"`
}

// CheckSpooled refuses what bind refuses before reading the content, in the same words: each
// case is put to the real binder as well, by a registered route, so the two can't come apart.
func TestCheckSpooledRefusesAsABoundBodyIs(t *testing.T) {
	bound := chi.NewRouter()
	Register(Routes{Router: bound, Registry: NewRegistry()}, Operation{Method: http.MethodPost, Path: "/"},
		func(*http.Request, spoolForms) (Empty, error) { return Empty{}, nil })
	for _, tc := range []struct{ contentType, body string }{
		{"", ""},
		{"", "  \n"},
		{"", " "},
		{"", " x"},
		{"", "{}"},
		{"", "\xff"},
		{"text/plain", ""},
		{"TEXT/Plain; charset=x", ""},
		{"text/plain", "  "},
		{"text/plain", "x"},
		{"a b/c; =", ""},
		{"a b/c; =", " "},
		{"a b/c; =", "x"},
		{"   ", "x"},
		{"application/json", ""},
		{"application/JSON; charset=x", "  "},
		{"application/raw", ""},
		{"application/raw", " "},
		{"application/raw", "x"},
	} {
		t.Run(tc.contentType+"/"+tc.body, func(t *testing.T) {
			request := func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
				if tc.contentType != "" {
					r.Header.Set("Content-Type", tc.contentType)
				}
				return r
			}
			var want httptest.ResponseRecorder
			want.Body = new(bytes.Buffer)
			bound.ServeHTTP(&want, request())

			r := request()
			file, done, err := SpoolBody(r, "")
			if err != nil {
				t.Fatal(err)
			}
			defer done()
			err = CheckSpooled(r, &spoolForms{}, file)
			if err == nil {
				// What's left for the binder to refuse is the content, which this doesn't read.
				if strings.TrimSpace(tc.body) == "" || strings.Contains(want.Body.String(), "Unsupported media type") {
					t.Fatalf("passed, where the binder answered %d %s", want.Code, want.Body)
				}
				at, seekErr := file.Seek(0, io.SeekCurrent)
				if seekErr != nil || at != 0 {
					t.Fatalf("left the file at %d: %v", at, seekErr)
				}
				return
			}
			got := httptest.NewRecorder()
			writeError(got, err)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Fatalf("answered %d %s, where the binder answered %d %s", got.Code, got.Body, want.Code, want.Body)
			}
		})
	}
}

func TestSpoolRemovesItsFileWhenTheSourceFails(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	broken := errors.New("broken")
	if _, _, err := Spool(io.MultiReader(strings.NewReader("x"), iotest.ErrReader(broken))); !errors.Is(err, broken) {
		t.Fatalf("got %v", err)
	}
	left, err := os.ReadDir(spool)
	if err != nil || len(left) != 0 {
		t.Fatalf("left %v, %v", left, err)
	}
}
