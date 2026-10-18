package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCA is a minimal RFC 8555 server: enough of the protocol for
// golang.org/x/crypto/acme to register, order, answer HTTP-01 and finalize. It
// does not verify JWS signatures, but it does validate every HTTP-01 challenge
// the way a real CA does: it fetches the key authorization from the responder
// and compares it with token + "." + the account key's thumbprint.
type fakeCA struct {
	t         *testing.T
	srv       *httptest.Server
	responder http.Handler // stands in for http://<domain>/ on port 80
	caCert    *x509.Certificate
	caKey     *ecdsa.PrivateKey

	mu         sync.Mutex
	thumbprint string
	fetched    map[string]string // domain → key authorization the CA fetched
	authzs     map[string]*fakeAuthz
	domains    []string
	issued     []byte
	nextID     int
}

type fakeAuthz struct {
	domain, token string
	valid         bool
	invalid       bool // validated and failed
}

func newFakeCA(t *testing.T, responder http.Handler) *fakeCA {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake ACME CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(der)
	ca := &fakeCA{t: t, responder: responder, caCert: caCert, caKey: caKey,
		fetched: map[string]string{}, authzs: map[string]*fakeAuthz{}}
	ca.srv = httptest.NewServer(http.HandlerFunc(ca.serve))
	t.Cleanup(ca.srv.Close)
	return ca
}

func (ca *fakeCA) url(path string) string { return ca.srv.URL + path }

func (ca *fakeCA) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Replay-Nonce", fmt.Sprintf("nonce-%d", time.Now().UnixNano()))
	if r.URL.Path == "/directory" {
		writeJSON(w, http.StatusOK, map[string]string{
			"newNonce": ca.url("/new-nonce"), "newAccount": ca.url("/new-account"),
			"newOrder": ca.url("/new-order"), "revokeCert": ca.url("/revoke"), "keyChange": ca.url("/key-change"),
		})
		return
	}
	if r.URL.Path == "/new-nonce" {
		w.WriteHeader(http.StatusOK)
		return
	}
	protected, payload := ca.decodeJWS(r)

	ca.mu.Lock()
	defer ca.mu.Unlock()
	switch {
	case r.URL.Path == "/new-account":
		var hdr struct {
			JWK struct{ Crv, Kty, X, Y string } `json:"jwk"`
		}
		_ = json.Unmarshal(protected, &hdr)
		canon := fmt.Sprintf(`{"crv":"%s","kty":"%s","x":"%s","y":"%s"}`, hdr.JWK.Crv, hdr.JWK.Kty, hdr.JWK.X, hdr.JWK.Y)
		sum := sha256.Sum256([]byte(canon))
		ca.thumbprint = base64.RawURLEncoding.EncodeToString(sum[:])
		w.Header().Set("Location", ca.url("/account/1"))
		writeJSON(w, http.StatusCreated, map[string]any{"status": "valid"})
	case r.URL.Path == "/new-order":
		var req struct {
			Identifiers []struct{ Type, Value string } `json:"identifiers"`
		}
		_ = json.Unmarshal(payload, &req)
		ca.domains = nil
		for _, id := range req.Identifiers {
			ca.nextID++
			id2 := fmt.Sprint(ca.nextID)
			ca.authzs[id2] = &fakeAuthz{domain: id.Value, token: "token-" + id2}
			ca.domains = append(ca.domains, id.Value)
		}
		w.Header().Set("Location", ca.url("/order/1"))
		writeJSON(w, http.StatusCreated, ca.order())
	case r.URL.Path == "/order/1":
		writeJSON(w, http.StatusOK, ca.order())
	case strings.HasPrefix(r.URL.Path, "/authz/"):
		writeJSON(w, http.StatusOK, ca.authz(strings.TrimPrefix(r.URL.Path, "/authz/")))
	case strings.HasPrefix(r.URL.Path, "/chal/"):
		id := strings.TrimPrefix(r.URL.Path, "/chal/")
		a := ca.authzs[id]
		// Validate like a CA: GET http://<domain>/.well-known/acme-challenge/<token>.
		rec := httptest.NewRecorder()
		ca.responder.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+a.domain+challengePathPrefix+a.token, nil))
		ca.fetched[a.domain] = rec.Body.String()
		a.valid = rec.Code == http.StatusOK && rec.Body.String() == a.token+"."+ca.thumbprint
		a.invalid = !a.valid
		writeJSON(w, http.StatusOK, ca.authz(id)["challenges"].([]map[string]any)[0])
	case r.URL.Path == "/finalize":
		var req struct{ CSR string }
		_ = json.Unmarshal(payload, &req)
		csrDER, _ := base64.RawURLEncoding.DecodeString(req.CSR)
		csr, err := x509.ParseCertificateRequest(csrDER)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "urn:ietf:params:acme:error:badCSR"})
			return
		}
		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(2), Subject: csr.Subject, DNSNames: csr.DNSNames,
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca.caCert, csr.PublicKey, ca.caKey)
		if err != nil {
			ca.t.Errorf("fake CA sign: %v", err)
		}
		ca.issued = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.caCert.Raw})...)
		w.Header().Set("Location", ca.url("/order/1"))
		writeJSON(w, http.StatusOK, ca.order())
	case r.URL.Path == "/cert/1":
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(ca.issued)
	default:
		http.NotFound(w, r)
	}
}

func (ca *fakeCA) order() map[string]any {
	status, urls := "ready", []string{}
	for id, a := range ca.authzs {
		urls = append(urls, ca.url("/authz/"+id))
		if !a.valid {
			status = "pending"
		}
	}
	o := map[string]any{"status": status, "authorizations": urls, "finalize": ca.url("/finalize")}
	if ca.issued != nil {
		o["status"], o["certificate"] = "valid", ca.url("/cert/1")
	}
	return o
}

func (ca *fakeCA) authz(id string) map[string]any {
	a := ca.authzs[id]
	status := "pending"
	if a.valid {
		status = "valid"
	} else if a.invalid {
		status = "invalid"
	}
	return map[string]any{
		"status":     status,
		"identifier": map[string]string{"type": "dns", "value": a.domain},
		"challenges": []map[string]any{{"type": "http-01", "url": ca.url("/chal/" + id), "token": a.token, "status": status}},
	}
}

func (ca *fakeCA) decodeJWS(r *http.Request) (protected, payload []byte) {
	body, _ := io.ReadAll(r.Body)
	var jws struct{ Protected, Payload string }
	if err := json.Unmarshal(body, &jws); err != nil {
		ca.t.Errorf("fake CA: %s %s: body is not a JWS: %v", r.Method, r.URL.Path, err)
	}
	protected, _ = base64.RawURLEncoding.DecodeString(jws.Protected)
	payload, _ = base64.RawURLEncoding.DecodeString(jws.Payload)
	return protected, payload
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func TestIssue_HTTP01AgainstFakeCA(t *testing.T) {
	responder := NewHTTP01Responder()
	ca := newFakeCA(t, responder)
	domains := []string{"mail.example.test", "autoconfig.example.test"}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	certPEM, keyPEM, err := Issue(ctx, IssueConfig{Directory: ca.url("/directory"), Email: "hostmaster@example.test", Domains: domains}, responder)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Every name was validated by fetching the right key authorization.
	for _, d := range domains {
		if got := ca.fetched[d]; !strings.HasSuffix(got, "."+ca.thumbprint) || ca.thumbprint == "" {
			t.Errorf("CA fetched %q for %s, want <token>.%s", got, d, ca.thumbprint)
		}
	}
	// The pair is a usable serving cert for every name, chained to the CA.
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatalf("issued pair does not load: %v", err)
	}
	if len(pair.Certificate) != 2 {
		t.Fatalf("chain has %d certificates, want leaf + CA", len(pair.Certificate))
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(ca.caCert)
	for _, d := range domains {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: d, Roots: pool}); err != nil {
			t.Errorf("leaf does not verify for %s: %v", d, err)
		}
	}
	// Challenge responses are withdrawn once answered.
	rec := httptest.NewRecorder()
	responder.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, challengePathPrefix+"token-1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("answered challenge still served: %d", rec.Code)
	}
}

func TestIssue_FailsWhenChallengeIsNotServed(t *testing.T) {
	// The CA reaches a server that is not the responder, as when port 80 is
	// routed elsewhere: validation fails and Issue returns no pair.
	responder := NewHTTP01Responder()
	ca := newFakeCA(t, http.NotFoundHandler())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	certPEM, keyPEM, err := Issue(ctx, IssueConfig{Directory: ca.url("/directory"), Domains: []string{"mail.example.test"}}, responder)
	if err == nil || certPEM != "" || keyPEM != "" {
		t.Fatalf("Issue = (%d bytes, %d bytes, %v), want an error and no pair", len(certPEM), len(keyPEM), err)
	}
}

func TestHTTP01Responder_ServesOnlyPendingTokens(t *testing.T) {
	r := NewHTTP01Responder()
	r.set("abc", "abc.thumb")
	for path, want := range map[string]int{
		challengePathPrefix + "abc":   http.StatusOK,
		challengePathPrefix + "other": http.StatusNotFound,
		"/abc":                        http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}
