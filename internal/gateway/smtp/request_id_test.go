package smtp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

// Both of the SMTP gateway's clients carry a connection's ID (#1580).
func TestClients_WithRequestIDTagsBothServices(t *testing.T) {
	got := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(requestid.Header)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	b := withRequestID(Clients{Delivery: apiclient.New(srv.URL), Submission: apiclient.New(srv.URL)}, "smtp-1")
	_, _ = b.CheckMailbox("someone@example.test") // delivery
	_ = b.CloseSession("rmgs_token")              // submission
	for range 2 {
		if id := <-got; id != "smtp-1" {
			t.Fatalf("X-Request-Id = %q, want smtp-1", id)
		}
	}
}
