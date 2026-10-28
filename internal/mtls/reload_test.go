package mtls_test

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/mtls"
	"github.com/restmail/restmail/internal/mtls/mtlstest"
)

// Use ServeTLS as the API does. httptest.StartTLS injects a static test
// certificate when Certificates is empty, bypassing GetCertificate without SNI.
func startMTLSServer(t *testing.T, cfg *tls.Config, handler http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{TLSConfig: cfg, Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Errorf("ServeTLS: %v", err)
		}
	})
	return "https://" + ln.Addr().String()
}

func replacePEM(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path+".new", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}

func TestMTLSHandshakesReloadFilePairs(t *testing.T) {
	for _, side := range []string{"server", "client"} {
		t.Run(side, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			ca, err := mtlstest.NewCA("reload CA")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			issue := func(side string) ([]byte, []byte) {
				t.Helper()
				var cert, key []byte
				var err error
				if side == "server" {
					cert, key, err = ca.IssueServer("api", nil, []net.IP{net.ParseIP("127.0.0.1")}, now.Add(-time.Hour), now.Add(time.Hour))
				} else {
					cert, key, err = ca.IssueClient("gateway", now.Add(-time.Hour), now.Add(time.Hour))
				}
				if err != nil {
					t.Fatal(err)
				}
				return cert, key
			}
			caPath := writeFile(t, dir, "ca.crt", ca.CertPEM)
			for _, name := range []string{"server", "client"} {
				cert, key := issue(name)
				writeFile(t, dir, name+".crt", cert)
				writeFile(t, dir, name+".key", key)
			}
			serverCfg, err := mtls.ServerTLSConfig(caPath, filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"))
			if err != nil {
				t.Fatal(err)
			}
			clientCfg, err := mtls.ClientTLSConfig(caPath, filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"))
			if err != nil {
				t.Fatal(err)
			}
			url := startMTLSServer(t, serverCfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, r.TLS.PeerCertificates[0].SerialNumber.String())
			}))
			transport := &http.Transport{TLSClientConfig: clientCfg, DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			serials := func() string {
				t.Helper()
				resp, err := client.Get(url)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if side == "client" {
					return string(body)
				}
				return resp.TLS.PeerCertificates[0].SerialNumber.String()
			}
			before := serials()
			cert, key := issue(side)
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				t.Fatal(err)
			}
			want := pair.Leaf.SerialNumber.String()
			replacePEM(t, filepath.Join(dir, side+".key"), key)
			time.Sleep(5100 * time.Millisecond)
			if got := serials(); got != before {
				t.Fatal("half-written renewal displaced old pair")
			}
			replacePEM(t, filepath.Join(dir, side+".crt"), cert)
			deadline := time.Now().Add(7 * time.Second)
			for serials() != want {
				if time.Now().After(deadline) {
					t.Fatal("renewal was not picked up on a new handshake")
				}
				time.Sleep(50 * time.Millisecond)
			}
			replacePEM(t, filepath.Join(dir, side+".crt"), []byte("broken renewal"))
			time.Sleep(5100 * time.Millisecond)
			if got := serials(); got != want {
				t.Fatal("broken renewal displaced good pair")
			}
		})
	}
}
