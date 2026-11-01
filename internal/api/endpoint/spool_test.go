package endpoint

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
