package middleware

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"testing"
)

func gatewayCert(cn string) *x509.Certificate {
	return &x509.Certificate{Subject: pkix.Name{CommonName: cn}}
}

func TestRequireClientCert_AllowsVerifiedCert(t *testing.T) {
	called := false
	gotCN := ""
	h := RequireClientCert(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotCN = ClientCertCN(r)
		w.WriteHeader(http.StatusOK)
	}))

	cert := gatewayCert("rest-mail-gateway")
	req := httptest.NewRequest(http.MethodGet, "/api/mailboxes", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("handler not called for a verified client certificate")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotCN != "rest-mail-gateway" {
		t.Fatalf("ClientCertCN = %q, want rest-mail-gateway", gotCN)
	}
}

func TestRequireClientCert_RejectsNoTLS(t *testing.T) {
	h := RequireClientCert(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run without a client certificate")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/mailboxes", nil) // req.TLS == nil
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestRequireClientCert_RejectsUnverifiedCert covers the VerifyClientCertIfGiven
// shape: the peer presented a certificate but the server did not verify it
// against its client CA (VerifiedChains empty). The middleware must still fail
// closed — presence of a cert is not proof it is trusted.
func TestRequireClientCert_RejectsUnverifiedCert(t *testing.T) {
	h := RequireClientCert(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run for an unverified client certificate")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/mailboxes", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{gatewayCert("attacker")},
		// VerifiedChains deliberately empty.
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestClientCertCN_AbsentWhenNoMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if cn := ClientCertCN(req); cn != "" {
		t.Fatalf("ClientCertCN = %q, want empty", cn)
	}
}
