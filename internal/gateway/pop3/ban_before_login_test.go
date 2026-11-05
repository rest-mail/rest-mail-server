package pop3

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
)

// A banned address that opens a fresh POP3 connection is dropped before the
// greeting and never reaches the auth API, even with the right password
// (#1678; go-pop3 checks the ban at accept time since its issue #17). The
// gateway is wired as cmd/pop3-gateway wires it.
func TestBannedAddressRefusedAtConnect(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"access_token":"tok","user":{"id":1,"email":"a@example.com"}}}`)
	}))
	t.Cleanup(api.Close)

	lim := connlimiter.New(connlimiter.Config{
		AuthMaxFails:    1,
		AuthBanWindow:   time.Minute,
		AuthBanDuration: time.Minute,
	})
	lim.RecordAuthFail("127.0.0.1")
	if !lim.IsBanned("127.0.0.1") {
		t.Fatal("setup: 127.0.0.1 is not banned")
	}

	srv, ports := serveOnFreePorts(t, false, func() *pop3server.Server {
		return pop3server.New(NewBackend(apiclient.New(api.URL), lim), nil, lim)
	})
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", ports.POP3))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)

	if greeting, err := r.ReadString('\n'); err == nil {
		_, _ = io.WriteString(conn, "USER a@example.com\r\nPASS right\r\n")
		user, _ := r.ReadString('\n')
		pass, _ := r.ReadString('\n')
		t.Fatalf("banned address greeted with %q; USER/PASS with the right password got %q %q (API calls %d)",
			strings.TrimSpace(greeting), strings.TrimSpace(user), strings.TrimSpace(pass), calls.Load())
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("auth API calls = %d, want 0", got)
	}
}
