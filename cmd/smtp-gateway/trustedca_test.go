package main

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCACertSharesOperatorTrustWithRESTMAIL(t *testing.T) {
	transport := http.DefaultTransport.(*http.Transport)
	original := transport.TLSClientConfig
	t.Cleanup(func() { transport.TLSClientConfig = original })
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := loadCACert(path)
	if roots == nil {
		t.Fatal("operator CA was not loaded")
	}
	if _, err := srv.Certificate().Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1"}); err != nil {
		t.Fatalf("returned RESTMAIL trust pool does not trust operator certificate: %v", err)
	}
	if transport.TLSClientConfig.RootCAs != roots {
		t.Fatal("RESTMAIL and default HTTP transport must share operator trust")
	}
}

func TestLoadCACertWithoutBundleUsesSystemRoots(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.crt")} {
		if roots := loadCACert(path); roots != nil {
			t.Fatalf("loadCACert(%q) = non-nil, want system-root default", path)
		}
	}
}
