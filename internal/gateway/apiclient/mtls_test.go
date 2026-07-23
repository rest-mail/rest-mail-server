package apiclient

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/restmail/restmail/internal/mtls"
	"github.com/restmail/restmail/internal/mtls/mtlstest"
)

// newMTLSTestServer starts an httptest server that requires a verified internal
// client certificate and answers CheckMailbox. It returns the server plus the
// paths of the CA + a valid client keypair.
func newMTLSTestServer(t *testing.T) (*httptest.Server, *mtlstest.Paths) {
	t.Helper()
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write mTLS material: %v", err)
	}
	serverCfg, err := mtls.ServerTLSConfig(p.CACert, p.ServerCert, p.ServerKey)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/mailboxes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"exists":true,"mailbox_id":7,"address":"a@b.test"}}`))
	})
	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = serverCfg
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts, p
}

// TestClient_WithTLSConfig_Succeeds proves an apiclient configured with the
// gateway client certificate authenticates to the mTLS-requiring API and gets a
// normal response.
func TestClient_WithTLSConfig_Succeeds(t *testing.T) {
	ts, p := newMTLSTestServer(t)

	clientCfg, err := mtls.ClientTLSConfig(p.CACert, p.ClientCert, p.ClientKey)
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	c := New(ts.URL, WithTLSConfig(clientCfg))

	resp, err := c.CheckMailbox("a@b.test")
	if err != nil {
		t.Fatalf("CheckMailbox with client cert failed: %v", err)
	}
	if !resp.Data.Exists || resp.Data.MailboxID != 7 {
		t.Fatalf("unexpected response: %+v", resp.Data)
	}
}

// TestClient_WithoutClientCert_Rejected proves that a client which trusts the
// server CA but presents NO client certificate is refused — the certificate,
// not mere reachability, is what authenticates the gateway.
func TestClient_WithoutClientCert_Rejected(t *testing.T) {
	ts, p := newMTLSTestServer(t)

	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(p.CACert)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append CA")
	}
	// Trusts the server, but offers no client certificate.
	c := New(ts.URL, WithTLSConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))

	if _, err := c.CheckMailbox("a@b.test"); err == nil {
		t.Fatal("expected CheckMailbox to fail without a client certificate, got nil error")
	}
}
