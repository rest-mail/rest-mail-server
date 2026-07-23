package config

import (
	"crypto/tls"
	"testing"

	"github.com/restmail/restmail/internal/mtls/mtlstest"
)

func TestLoad_InternalMTLSDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.InternalMTLSEnabled {
		t.Error("InternalMTLSEnabled = true, want false by default (non-breaking)")
	}
	if cfg.InternalMTLSPort != DefaultInternalMTLSPort {
		t.Errorf("InternalMTLSPort = %d, want %d", cfg.InternalMTLSPort, DefaultInternalMTLSPort)
	}
	if cfg.InternalMTLSCACert != "" || cfg.InternalMTLSServerCert != "" || cfg.InternalMTLSClientCert != "" {
		t.Error("internal mTLS cert paths should default empty")
	}
	// API_INTERNAL_BASE_URL must default empty, and API_BASE_URL must keep its
	// public default independent of the internal listener.
	if cfg.APIInternalBaseURL != "" {
		t.Errorf("APIInternalBaseURL = %q, want empty by default", cfg.APIInternalBaseURL)
	}
	if cfg.APIBaseURL != "http://localhost:8080" {
		t.Errorf("APIBaseURL = %q, want the public default", cfg.APIBaseURL)
	}
}

func TestLoad_APIInternalBaseURL(t *testing.T) {
	clearEnv(t)
	t.Setenv("API_BASE_URL", "http://api:8080")
	t.Setenv("API_INTERNAL_BASE_URL", "https://api:8443")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// The two URLs are independent: public stays public, internal is separate.
	if cfg.APIBaseURL != "http://api:8080" {
		t.Errorf("APIBaseURL = %q, want http://api:8080 (public)", cfg.APIBaseURL)
	}
	if cfg.APIInternalBaseURL != "https://api:8443" {
		t.Errorf("APIInternalBaseURL = %q, want https://api:8443", cfg.APIInternalBaseURL)
	}
}

func TestLoad_InternalMTLSEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("INTERNAL_MTLS_ENABLED", "true")
	t.Setenv("INTERNAL_MTLS_PORT", "9443")
	t.Setenv("INTERNAL_MTLS_CA_CERT", "/certs/internal-ca.crt")
	t.Setenv("INTERNAL_MTLS_SERVER_CERT", "/certs/internal-server.crt")
	t.Setenv("INTERNAL_MTLS_SERVER_KEY", "/certs/internal-server.key")
	t.Setenv("INTERNAL_MTLS_CLIENT_CERT", "/certs/internal-client.crt")
	t.Setenv("INTERNAL_MTLS_CLIENT_KEY", "/certs/internal-client.key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.InternalMTLSEnabled {
		t.Error("InternalMTLSEnabled = false, want true")
	}
	if cfg.InternalMTLSPort != 9443 {
		t.Errorf("InternalMTLSPort = %d, want 9443", cfg.InternalMTLSPort)
	}
	if cfg.InternalMTLSCACert != "/certs/internal-ca.crt" {
		t.Errorf("InternalMTLSCACert = %q", cfg.InternalMTLSCACert)
	}
	if got := cfg.InternalMTLSAddr(); got != "0.0.0.0:9443" {
		t.Errorf("InternalMTLSAddr() = %q, want 0.0.0.0:9443", got)
	}
}

func TestLoad_InternalMTLSInvalidPort(t *testing.T) {
	for _, port := range []string{"0", "-1", "70000"} {
		clearEnv(t)
		t.Setenv("INTERNAL_MTLS_ENABLED", "true")
		t.Setenv("INTERNAL_MTLS_PORT", port)
		if _, err := Load(); err == nil {
			t.Errorf("Load() with INTERNAL_MTLS_PORT=%s and mTLS enabled: want error, got nil", port)
		}
	}
}

func TestLoad_InternalMTLSInvalidPortIgnoredWhenDisabled(t *testing.T) {
	clearEnv(t)
	// Port is nonsense but the feature is off, so it must not fail startup.
	t.Setenv("INTERNAL_MTLS_PORT", "0")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() with mTLS disabled must ignore the port, got error: %v", err)
	}
}

func TestInternalMTLSServerTLS_ErrorsOnMissingPaths(t *testing.T) {
	cfg := &Config{InternalMTLSEnabled: true} // no cert paths set
	if _, err := cfg.InternalMTLSServerTLS(); err == nil {
		t.Fatal("InternalMTLSServerTLS() with no paths: want error, got nil")
	}
}

func TestInternalMTLSClientTLS_ErrorsOnMissingPaths(t *testing.T) {
	cfg := &Config{InternalMTLSEnabled: true} // no cert paths set
	if _, err := cfg.InternalMTLSClientTLS(); err == nil {
		t.Fatal("InternalMTLSClientTLS() with no paths: want error, got nil")
	}
}

func TestInternalMTLSServerTLS_LoadsRealMaterial(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write material: %v", err)
	}
	cfg := &Config{
		InternalMTLSEnabled:    true,
		InternalMTLSCACert:     p.CACert,
		InternalMTLSServerCert: p.ServerCert,
		InternalMTLSServerKey:  p.ServerKey,
	}
	tc, err := cfg.InternalMTLSServerTLS()
	if err != nil {
		t.Fatalf("InternalMTLSServerTLS() error: %v", err)
	}
	if tc.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", tc.ClientAuth)
	}
}

func TestInternalMTLSClientTLS_LoadsRealMaterial(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write material: %v", err)
	}
	cfg := &Config{
		InternalMTLSEnabled:    true,
		InternalMTLSCACert:     p.CACert,
		InternalMTLSClientCert: p.ClientCert,
		InternalMTLSClientKey:  p.ClientKey,
	}
	if _, err := cfg.InternalMTLSClientTLS(); err != nil {
		t.Fatalf("InternalMTLSClientTLS() error: %v", err)
	}
}
