package queue

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Development's SMTP TLS flag must never expose a message to an unverified
// REST endpoint, even when discovery legitimately names that endpoint (#291).
func TestDeliverRESTMAILAlwaysVerifiesCertificate(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		for _, trust := range []string{"trusted", "untrusted", "wrong hostname"} {
			t.Run(fmt.Sprintf("smtp-insecure=%v/%s", insecure, trust), func(t *testing.T) {
				cert, roots := relayTestCertificate(t, trust == "wrong hostname")
				var posts atomic.Int32
				srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost && r.URL.Path == "/restmail/messages" {
						posts.Add(1)
					}
					rw.WriteHeader(http.StatusCreated)
				}))
				srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
				srv.StartTLS()
				defer srv.Close()
				if trust == "untrusted" {
					roots = x509.NewCertPool()
				}
				w := &Worker{sendDeadline: defaultSendDeadline, allowPrivateDest: true}
				w.SetRESTMAILRootCAs(roots)
				w.SetTLSInsecure(insecure)
				err := w.deliverRESTMAILHTTPS(srv.URL+"/restmail", testItem())
				if trust == "trusted" {
					if err != nil || posts.Load() != 1 {
						t.Fatalf("trusted endpoint: err=%v posts=%d, want successful POST", err, posts.Load())
					}
					return
				}
				var verificationErr *tls.CertificateVerificationError
				if !errors.As(err, &verificationErr) || posts.Load() != 0 {
					t.Fatalf("unverified endpoint: err=%v posts=%d, want certificate failure with no POST", err, posts.Load())
				}
			})
		}
	}
}

func TestRESTMAILVerifiedDiscoveryDoesNotTrustFinalCertificate(t *testing.T) {
	// Use a different certificate from discovery, so trusting the discovery
	// server cannot accidentally trust the final server's self-signed cert.
	cert, _ := relayTestCertificate(t, false)
	var posts atomic.Int32
	final := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		rw.WriteHeader(http.StatusCreated)
	}))
	final.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	final.StartTLS()
	defer final.Close()

	var discoveries atomic.Int32
	discovery, w := discoveryServer(t, func(rw http.ResponseWriter, _ *http.Request) {
		discoveries.Add(1)
		fmt.Fprintf(rw, `{"endpoint":%q}`, final.URL+"/restmail")
	})
	w.SetTLSInsecure(true)
	err := w.deliverRESTMAILDiscovered(discovery.URL+"/.well-known/restmail?domain=example.net", testItem())
	var verificationErr *tls.CertificateVerificationError
	if !errors.As(err, &verificationErr) || discoveries.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("err=%v discovery requests=%d final posts=%d, want verified discovery then rejected final certificate", err, discoveries.Load(), posts.Load())
	}
}
