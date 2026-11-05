package pop3

import (
	"errors"
	"math/rand/v2"
	"net"
	"syscall"
	"testing"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"
)

// serveOnFreePorts starts the server newServer builds on free ports: POP3, and
// POP3TLS as well if withTLS is set.
//
// go-pop3 v0.4.0 takes port numbers and binds them itself, so a test cannot hold
// a port open and hand it over as it does for the SMTP and IMAP servers. Instead
// it tries a port and picks again only if the bind says another process has it.
// Ports come from below Linux's default ephemeral range (32768-60999), where
// the kernel hands out nothing to other processes' outgoing connections or to
// listeners on port 0. The server is built afresh each try because a failed
// ListenAndServe can leave its first port open, and Close cannot be undone.
func serveOnFreePorts(t *testing.T, withTLS bool, newServer func() *pop3server.Server) (*pop3server.Server, pop3server.Ports) {
	t.Helper()
	const attempts = 20
	var err error
	for range attempts {
		ports := pop3server.Ports{POP3: testPort()}
		if withTLS {
			ports.POP3TLS = testPort()
		}
		srv := newServer()
		if err = srv.ListenAndServe(ports); err == nil {
			return srv, ports
		}
		_ = srv.Close()
		if !errors.Is(err, syscall.EADDRINUSE) {
			t.Fatalf("start the POP3 server: %v", err)
		}
	}
	t.Fatalf("start the POP3 server: no free port in %d tries: %v", attempts, err)
	return nil, pop3server.Ports{}
}

// testPort picks a port below the ephemeral range.
func testPort() int {
	const first, last = 20000, 32767
	return first + rand.IntN(last-first+1)
}

// TestPortInUseIsTheErrorServeOnFreePortsRetries holds serveOnFreePorts to what
// it relies on: a port already held comes back from go-pop3 as EADDRINUSE.
func TestPortInUseIsTheErrorServeOnFreePortsRetries(t *testing.T) {
	held, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	srv := pop3server.New(nil, nil, nil)
	t.Cleanup(func() { _ = srv.Close() })
	port := held.Addr().(*net.TCPAddr).Port
	if err := srv.ListenAndServe(pop3server.Ports{POP3: port}); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("ListenAndServe on a held port = %v, want EADDRINUSE", err)
	}
}
