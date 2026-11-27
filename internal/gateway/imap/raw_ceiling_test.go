package imap

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// A stored source over the gateway's read ceiling fails the fetch instead of being rebuilt
// from the message's fields, which would hand the client a different message; one at the
// ceiling is served verbatim (#2107 AC02).
func TestRawMessageOverTheReadCeilingFailsTheFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		_, _ = w.Write([]byte(storedRawWithAttachment))
	}))
	t.Cleanup(srv.Close)
	size := int64(len(storedRawWithAttachment))

	at := &mailbox{api: apiclient.New(srv.URL, apiclient.WithMaxRawMessageBytes(size)), token: "tok"}
	if got, err := at.rawMessage(sampleDetail(42)); err != nil || got != storedRawWithAttachment {
		t.Fatalf("at the ceiling: %v, %q", err, got)
	}

	under := &mailbox{api: apiclient.New(srv.URL, apiclient.WithMaxRawMessageBytes(size-1)), token: "tok"}
	if got, err := under.rawMessage(sampleDetail(42)); !errors.Is(err, apiclient.ErrRawMessageTooLarge) {
		t.Fatalf("over the ceiling: got %q, %v; want ErrRawMessageTooLarge", got, err)
	}
}
