package imap

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/rawmsg"
)

// TestFetch_DowngradesUTF8Headers proves FETCH hands a client that has not
// enabled UTF-8 (RFC 6855) no raw UTF-8 headers: the header block goes out as
// ASCII while the body stays as stored.
func TestFetch_DowngradesUTF8Headers(t *testing.T) {
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

	got, err := m.Fetch(7)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := "From: =?utf-8?q?J=C3=B6rg?= <joerg@xn--bcher-kva.example>\r\n" +
		"Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n\r\nHallo, Jörg.\r\n"
	if string(got) != want {
		t.Fatalf("Fetch served:\n%q\nwant\n%q", got, want)
	}
}

// TestMessages_SizeCountsDowngradedOctets proves RFC822.SIZE is the octet count
// FETCH sends (RFC 3501 section 2.3.4): a session that has not enabled UTF-8 is
// told the downgraded size, one that has is told the stored size.
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
		msgs, err := m.Messages("INBOX")
		if err != nil {
			t.Fatalf("Messages (utf8=%v): %v", utf8, err)
		}
		got, err := m.Fetch(7)
		if err != nil {
			t.Fatalf("Fetch (utf8=%v): %v", utf8, err)
		}
		if len(msgs) != 1 || msgs[0].Size != len(got) {
			t.Fatalf("utf8=%v: RFC822.SIZE %v, FETCH sends %d octets", utf8, msgs, len(got))
		}
	}
}
