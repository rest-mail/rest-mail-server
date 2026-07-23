package mtls_test

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/mtls"
	"github.com/restmail/restmail/internal/mtls/mtlstest"
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// doGet dials url with the given client TLS config and returns the status code,
// or an error if the connection/handshake failed.
func doGet(url string, clientTLS *tls.Config) (int, error) {
	c := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLS},
		Timeout:   5 * time.Second,
	}
	resp, err := c.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// TestInternalMTLS_AcceptRejectMatrix is the security-critical matrix: a
// listener built from mtls.ServerTLSConfig (RequireAndVerifyClientCert) must
// ACCEPT a client bearing a valid internal-CA cert and REJECT one with no cert,
// an expired cert, or a cert from a different CA.
func TestInternalMTLS_AcceptRejectMatrix(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	ca, err := mtlstest.NewCA("internal CA")
	if err != nil {
		t.Fatalf("new CA: %v", err)
	}
	srvCert, srvKey, err := ca.IssueServer("api", nil, []net.IP{net.ParseIP("127.0.0.1")}, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("issue server: %v", err)
	}
	caPath := writeFile(t, dir, "ca.crt", ca.CertPEM)
	scPath := writeFile(t, dir, "server.crt", srvCert)
	skPath := writeFile(t, dir, "server.key", srvKey)

	serverCfg, err := mtls.ServerTLSConfig(caPath, scPath, skPath)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ts.TLS = serverCfg
	ts.StartTLS()
	defer ts.Close()

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatal("append CA to pool")
	}

	t.Run("valid client cert is accepted", func(t *testing.T) {
		vc, vk, err := ca.IssueClient("gateway", now.Add(-time.Hour), now.Add(time.Hour))
		if err != nil {
			t.Fatalf("issue client: %v", err)
		}
		ccPath := writeFile(t, dir, "client.crt", vc)
		ckPath := writeFile(t, dir, "client.key", vk)
		// Use the real client builder — this doubles as the "apiclient config"
		// happy-path proof at the TLS layer.
		clientCfg, err := mtls.ClientTLSConfig(caPath, ccPath, ckPath)
		if err != nil {
			t.Fatalf("ClientTLSConfig: %v", err)
		}
		status, err := doGet(ts.URL, clientCfg)
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
	})

	t.Run("no client cert is rejected", func(t *testing.T) {
		if status, err := doGet(ts.URL, &tls.Config{RootCAs: caPool}); err == nil {
			t.Fatalf("expected handshake rejection, got status %d", status)
		}
	})

	t.Run("expired client cert is rejected", func(t *testing.T) {
		ec, ek, err := ca.IssueClient("expired-gateway", now.Add(-2*time.Hour), now.Add(-time.Hour))
		if err != nil {
			t.Fatalf("issue expired client: %v", err)
		}
		pair, err := tls.X509KeyPair(ec, ek)
		if err != nil {
			t.Fatalf("keypair: %v", err)
		}
		cfg := &tls.Config{RootCAs: caPool, Certificates: []tls.Certificate{pair}}
		if status, err := doGet(ts.URL, cfg); err == nil {
			t.Fatalf("expected handshake rejection for expired cert, got status %d", status)
		}
	})

	t.Run("cert from a different CA is rejected", func(t *testing.T) {
		foreign, err := mtlstest.NewCA("foreign CA")
		if err != nil {
			t.Fatalf("foreign CA: %v", err)
		}
		fc, fk, err := foreign.IssueClient("attacker", now.Add(-time.Hour), now.Add(time.Hour))
		if err != nil {
			t.Fatalf("issue foreign client: %v", err)
		}
		pair, err := tls.X509KeyPair(fc, fk)
		if err != nil {
			t.Fatalf("keypair: %v", err)
		}
		cfg := &tls.Config{RootCAs: caPool, Certificates: []tls.Certificate{pair}}
		if status, err := doGet(ts.URL, cfg); err == nil {
			t.Fatalf("expected handshake rejection for foreign cert, got status %d", status)
		}
	})
}

func TestServerTLSConfig_Properties(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write material: %v", err)
	}
	cfg, err := mtls.ServerTLSConfig(p.CACert, p.ServerCert, p.ServerKey)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Error("ClientCAs is nil, want the internal CA pool")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("Certificates = %d, want 1", len(cfg.Certificates))
	}
}

func TestClientTLSConfig_Properties(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write material: %v", err)
	}
	cfg, err := mtls.ClientTLSConfig(p.CACert, p.ClientCert, p.ClientKey)
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs is nil, want the internal CA pool")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("Certificates = %d, want 1", len(cfg.Certificates))
	}
}

func TestServerTLSConfig_MissingPaths(t *testing.T) {
	cases := []struct{ ca, cert, key string }{
		{"", "s.crt", "s.key"},
		{"ca.crt", "", "s.key"},
		{"ca.crt", "s.crt", ""},
	}
	for _, c := range cases {
		if _, err := mtls.ServerTLSConfig(c.ca, c.cert, c.key); err == nil {
			t.Errorf("ServerTLSConfig(%q,%q,%q) = nil error, want error", c.ca, c.cert, c.key)
		}
	}
}

func TestClientTLSConfig_MissingPaths(t *testing.T) {
	cases := []struct{ ca, cert, key string }{
		{"", "c.crt", "c.key"},
		{"ca.crt", "", "c.key"},
		{"ca.crt", "c.crt", ""},
	}
	for _, c := range cases {
		if _, err := mtls.ClientTLSConfig(c.ca, c.cert, c.key); err == nil {
			t.Errorf("ClientTLSConfig(%q,%q,%q) = nil error, want error", c.ca, c.cert, c.key)
		}
	}
}

func TestServerTLSConfig_BadCAFile(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write material: %v", err)
	}
	bogus := writeFile(t, dir, "bogus-ca.crt", []byte("not a pem certificate"))
	if _, err := mtls.ServerTLSConfig(bogus, p.ServerCert, p.ServerKey); err == nil {
		t.Fatal("expected error for CA file with no PEM certificate, got nil")
	}
}

func TestServerTLSConfig_MissingCAFile(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write material: %v", err)
	}
	if _, err := mtls.ServerTLSConfig(filepath.Join(dir, "nope.crt"), p.ServerCert, p.ServerKey); err == nil {
		t.Fatal("expected error for missing CA file, got nil")
	}
}
