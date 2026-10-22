package pop3

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// TestRetrieve_DowngradesUTF8Headers proves RETR never hands a client raw UTF-8
// headers: no client can enable UTF-8 here yet (RFC 6855/6856), so the header
// block goes out as ASCII while the body stays as stored.
func TestRetrieve_DowngradesUTF8Headers(t *testing.T) {
	const raw = "From: Jörg <joerg@bücher.example>\r\nSubject: Grüße\r\n\r\nHallo, Jörg.\r\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/messages/7", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":7}}`))
	})
	mux.HandleFunc("/api/v1/messages/7/raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		_, _ = w.Write([]byte(raw))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	m := &mailbox{api: apiclient.New(srv.URL), token: "tok"}

	got, err := m.Retrieve("7")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	want := "From: =?utf-8?q?J=C3=B6rg?= <joerg@xn--bcher-kva.example>\r\n" +
		"Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n\r\nHallo, Jörg.\r\n"
	if string(got) != want {
		t.Fatalf("Retrieve served:\n%q\nwant\n%q", got, want)
	}
}
