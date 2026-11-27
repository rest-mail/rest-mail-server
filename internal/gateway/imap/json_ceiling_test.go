package imap

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// A message detail over the gateway's read ceiling fails the fetch with the client's
// refusal rather than being served or rebuilt from something else (#2107 AC02).
func TestMessageDetailOverTheReadCeilingFailsTheFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":42,"body_text":"` + strings.Repeat("a", 4096) + `"}}`))
	}))
	t.Cleanup(srv.Close)

	m := &mailbox{api: apiclient.New(srv.URL, apiclient.WithMaxRawMessageBytes(100)), token: "tok"}
	if got, err := m.Fetch(42); !errors.Is(err, apiclient.ErrResponseTooLarge) {
		t.Fatalf("got %q, %v; want ErrResponseTooLarge", got, err)
	}
}
