package pop3

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// TestMessages_SizesCostNoConversion proves STAT and LIST report each message's converted
// size from what the server measured when it stored the message: the one request is the
// list, however many messages it holds, and each size is the stored figure for the
// session (the figures here are not what any conversion of any source would give, so a
// size worked out again could not match them).
func TestMessages_SizesCostNoConversion(t *testing.T) {
	const count = 200
	var others atomic.Int64
	var list strings.Builder
	for id := 1; id <= count; id++ {
		if id > 1 {
			list.WriteString(",")
		}
		fmt.Fprintf(&list, `{"id":%d,"raw_size":%d,"downgraded_size":%d,"pop3_utf8_size":%d}`, id, 1000+id, 2000+id, 3000+id)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/accounts/0/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[` + list.String() + `]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		others.Add(1)
		http.Error(w, "a listing must not read a message", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, utf8 := range []bool{false, true} {
		m := &mailbox{api: apiclient.New(srv.URL), token: "tok", utf8: utf8}
		msgs, err := m.Messages()
		if err != nil {
			t.Fatalf("Messages (utf8=%v): %v", utf8, err)
		}
		if len(msgs) != count {
			t.Fatalf("utf8=%v: %d messages, want %d", utf8, len(msgs), count)
		}
		for i, msg := range msgs {
			id := i + 1
			want := 2000 + id
			if utf8 {
				want = 3000 + id
			}
			if msg.Size != want {
				t.Fatalf("utf8=%v: message %d reports %d, want the stored %d", utf8, id, msg.Size, want)
			}
		}
	}
	if n := others.Load(); n != 0 {
		t.Errorf("listing %d messages made %d other requests, want none", count, n)
	}
}
