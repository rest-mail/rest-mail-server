package rfctest

import (
	"bufio"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

var errTCPClosedWithoutTLSAlert = errors.New("TCP closed without a TLS close alert")

// Go's TLS reader permits bare TCP EOF at a record boundary. Replace transport
// EOF with a distinct error so only a decrypted close_notify can produce EOF
// from tls.Conn.Read. This also works with TLS 1.3's encrypted alert records.
type closureConn struct{ net.Conn }

func (c closureConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if errors.Is(err, io.EOF) {
		err = errTCPClosedWithoutTLSAlert
	}
	return n, err
}

// checklist: POP3-ITLS-04 POP3-ITLS-09
func TestImplicitTLSCloseNotify(t *testing.T) {
	for _, version := range []struct {
		name string
		id   uint16
	}{{"TLS12", tls.VersionTLS12}, {"TLS13", tls.VersionTLS13}} {
		for _, authenticated := range []bool{false, true} {
			state := "authorization"
			if authenticated {
				state = "transaction"
			}
			for _, initiator := range []string{"QUIT", "client-alert"} {
				t.Run(version.name+"/"+state+"/"+initiator, func(t *testing.T) {
					server, client := stlsConfigs(t)
					server.MinVersion, server.MaxVersion = version.id, version.id
					client.MinVersion, client.MaxVersion = version.id, version.id
					addr, b := implicitTLSServer(t, server)
					raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = raw.Close() })
					_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
					secured := tls.Client(closureConn{raw}, client)
					if err := secured.Handshake(); err != nil {
						t.Fatal(err)
					}
					if got := secured.ConnectionState(); got.Version != version.id || len(got.VerifiedChains) == 0 {
						t.Fatalf("TLS version/verification: %+v", got)
					}
					w := &wire{t: t, conn: secured, reader: bufio.NewReader(secured)}
					w.status(w.line(), "+OK")
					if authenticated {
						w.login("secret")
						w.status(w.cmd("STAT"), "+OK")
					}
					if initiator == "QUIT" {
						w.status(w.cmd("QUIT"), "+OK")
					} else {
						// Send only the TLS alert, leaving TCP open to receive the
						// server's alert. No POP3 QUIT or TCP EOF triggers closure.
						if err := secured.CloseWrite(); err != nil {
							t.Fatal(err)
						}
					}
					_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
					if got, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
						t.Fatalf("TLS closure: byte %q, error %v; want close_notify EOF", got, err)
					}
					// The close alert must be followed by TCP termination, not
					// merely a TLS half-close that leaves the socket open.
					var next [1]byte
					if n, err := raw.Read(next[:]); n != 0 || !errors.Is(err, io.EOF) {
						t.Fatalf("after close_notify: read %d bytes, error %v; want TCP EOF", n, err)
					}
					wantCalls := int32(0)
					if authenticated {
						wantCalls = 1
					}
					if got := b.calls.Load(); got != wantCalls {
						t.Fatalf("authentication calls = %d, want %d", got, wantCalls)
					}
				})
			}
		}
	}
}
