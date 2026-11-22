package mtls_test

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil/reloadtest"
	"github.com/rest-mail/rest-mail-server/internal/mtls"
	"github.com/rest-mail/rest-mail-server/internal/mtls/mtlstest"
)

func TestAPIHTTPSListenerAndGatewayClientServeRenewedCertificates(t *testing.T) {
	for _, side := range []string{"server", "client"} {
		t.Run(side, func(t *testing.T) {
			dir := t.TempDir()
			ca, err := mtlstest.NewCA("API HTTPS reload CA")
			if err != nil {
				t.Fatal(err)
			}
			caPath := writeFile(t, dir, "ca.crt", ca.CertPEM)
			issue := func(identity string) ([]byte, []byte) {
				t.Helper()
				var cert, key []byte
				var err error
				if identity == "server" {
					cert, key, err = ca.IssueServer("api", []string{"api.test"}, []net.IP{net.ParseIP("127.0.0.1")}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
				} else {
					cert, key, err = ca.IssueClient("rest-mail-imap", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
				}
				if err != nil {
					t.Fatal(err)
				}
				return cert, key
			}
			for _, identity := range []string{"server", "client"} {
				cert, key := issue(identity)
				writeFile(t, dir, identity+".crt", cert)
				writeFile(t, dir, identity+".key", key)
			}
			serverConfig, err := mtls.ServerTLSConfig(caPath, filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"))
			if err != nil {
				t.Fatal(err)
			}
			clientConfig, err := mtls.ClientTLSConfig(caPath, filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"))
			if err != nil {
				t.Fatal(err)
			}
			clientConfig.ServerName = "api.test"
			endpoint := startMTLSServer(t, serverConfig, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				fmt.Fprint(writer, request.TLS.PeerCertificates[0].SerialNumber.String())
			}))
			connect := func() *tls.Conn {
				t.Helper()
				conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", strings.TrimPrefix(endpoint, "https://"), clientConfig)
				if err != nil {
					t.Fatal(err)
				}
				return conn
			}
			read := func(conn *tls.Conn) string {
				t.Helper()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: api.test\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				response, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("established HTTPS: status=%d error=%v", response.StatusCode, err)
				}
				if side == "client" {
					return string(body)
				}
				if len(conn.ConnectionState().VerifiedChains) == 0 {
					t.Fatal("API certificate has no verified chain")
				}
				return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
			}
			fresh := func() string {
				t.Helper()
				conn := connect()
				defer func() { _ = conn.Close() }()
				return read(conn)
			}
			established := connect()
			defer func() { _ = established.Close() }()
			original := read(established)
			cert, key := issue(side)
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				t.Fatal(err)
			}
			renewed := pair.Leaf.SerialNumber.String()
			publish := func() {
				t.Helper()
				replacePEM(t, filepath.Join(dir, side+".key"), key)
				replacePEM(t, filepath.Join(dir, side+".crt"), cert)
				time.Sleep(5100 * time.Millisecond)
				if got := fresh(); got != renewed {
					t.Fatalf("fresh HTTPS after replacement: %s, want %s", got, renewed)
				}
				if got := read(established); got != original {
					t.Fatalf("established identity changed: %s, want %s", got, original)
				}
			}
			publish()
			for _, control := range []string{"missing", "invalid"} {
				t.Run(control, func(t *testing.T) {
					before := reloadtest.Metric(t, "restmail_tls_cert_reload_failures_total")
					failing := reloadtest.Metric(t, "restmail_tls_cert_reload_failing")
					path := filepath.Join(dir, side+".crt")
					if control == "missing" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					} else {
						replacePEM(t, path, []byte("invalid replacement"))
					}
					time.Sleep(5100 * time.Millisecond)
					if got := fresh(); got != renewed {
						t.Fatalf("%s replacement displaced usable certificate: %s", control, got)
					}
					if got := reloadtest.Metric(t, "restmail_tls_cert_reload_failures_total") - before; got != 1 {
						t.Fatalf("failure counter change=%v, want 1", got)
					}
					if got := reloadtest.Metric(t, "restmail_tls_cert_reload_failing") - failing; got != 1 {
						t.Fatalf("failing gauge change=%v, want 1", got)
					}
					publish()
					if got := reloadtest.Metric(t, "restmail_tls_cert_reload_failing") - failing; got != 0 {
						t.Fatalf("failing gauge after repair=%v, want 0", got)
					}
				})
			}
		})
	}
}
