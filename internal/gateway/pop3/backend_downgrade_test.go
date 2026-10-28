package pop3

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/rawmsg"
)

// TestRetrieve_DowngradesUTF8Headers proves RETR hands a client that did not
// send UTF8 (RFC 6856) no raw UTF-8 headers: the header block goes out as
// ASCII while the body stays as stored.
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

// TestMessages_SizeCountsDowngradedOctets proves STAT and LIST report the
// octets RETR sends (RFC 1939 section 5): a session that did not send UTF8 is
// told the downgraded size, one that did is told the stored size.
func TestMessages_SizeCountsDowngradedOctets(t *testing.T) {
	const raw = "From: Jörg <joerg@bücher.example>\r\nSubject: Grüße\r\n\r\nHallo, Jörg.\r\n"
	downgraded := rawmsg.Downgrade(raw)
	list := fmt.Sprintf(`{"data":[{"id":7,"raw_size":%d,"downgraded_size":%d}]}`, len(raw), len(downgraded))
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/accounts/0/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(list))
	})
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

	for _, utf8 := range []bool{false, true} {
		m := &mailbox{api: apiclient.New(srv.URL), token: "tok", utf8: utf8}
		msgs, err := m.Messages()
		if err != nil {
			t.Fatalf("Messages (utf8=%v): %v", utf8, err)
		}
		got, err := m.Retrieve("7")
		if err != nil {
			t.Fatalf("Retrieve (utf8=%v): %v", utf8, err)
		}
		if len(msgs) != 1 || msgs[0].Size != len(got) {
			t.Fatalf("utf8=%v: LIST size %v, RETR sends %d octets", utf8, msgs, len(got))
		}
	}
}
