package filters

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// newCappedDuplicate builds a duplicate filter aimed at srv. The SSRF guard
// refuses httptest's loopback address, so the test swaps in a plain transport.
func newCappedDuplicate(t *testing.T, url string, limit int64) pipeline.Filter {
	t.Helper()
	f, err := NewDuplicateWithLimit(limit)([]byte(`{"webhook_url":"` + url + `","timeout_ms":2000}`))
	if err != nil {
		t.Fatal(err)
	}
	f.(*duplicateFilter).client.Transport = http.DefaultTransport
	return f
}

func TestDuplicateRequestCap(t *testing.T) {
	email := unitEmail()
	want, err := json.Marshal(email)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(want))

	t.Run("at limit is sent unchanged", func(t *testing.T) {
		got := make(chan []byte, 4)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got <- b
		}))
		defer srv.Close()

		res, err := newCappedDuplicate(t, srv.URL, size).Execute(t.Context(), email)
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != pipeline.ActionContinue || res.Log.Result != "pass" {
			t.Fatalf("result = %s/%s, want continue/pass", res.Action, res.Log.Result)
		}
		select {
		case b := <-got:
			if string(b) != string(want) {
				t.Fatalf("server received %d bytes, want the %d marshalled bytes", len(b), len(want))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no request reached the server")
		}
	})

	t.Run("one byte over is not sent", func(t *testing.T) {
		got := make(chan []byte, 4)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got <- b
		}))
		defer srv.Close()

		res, err := newCappedDuplicate(t, srv.URL, size-1).Execute(t.Context(), email)
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != pipeline.ActionContinue || res.Log.Result != "error" {
			t.Fatalf("result = %s/%s, want continue/error", res.Action, res.Log.Result)
		}
		select {
		case b := <-got:
			t.Fatalf("server received %d bytes, want no request", len(b))
		case <-time.After(500 * time.Millisecond):
		}
	})
}
