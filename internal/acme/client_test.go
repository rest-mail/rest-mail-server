package acme

import (
	"encoding/pem"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

func TestNeedsRenewal(t *testing.T) {
	tests := []struct {
		name     string
		notAfter time.Time
		want     bool
	}{
		{
			name:     "expires in 60 days - no renewal needed",
			notAfter: time.Now().Add(60 * 24 * time.Hour),
			want:     false,
		},
		{
			name:     "expires in 31 days - no renewal needed",
			notAfter: time.Now().Add(31 * 24 * time.Hour),
			want:     false,
		},
		{
			name:     "expires in 30 days - renewal needed (boundary)",
			notAfter: time.Now().Add(30 * 24 * time.Hour),
			want:     true,
		},
		{
			name:     "expires in 29 days - renewal needed",
			notAfter: time.Now().Add(29 * 24 * time.Hour),
			want:     true,
		},
		{
			name:     "expires in 7 days - renewal needed",
			notAfter: time.Now().Add(7 * 24 * time.Hour),
			want:     true,
		},
		{
			name:     "expires in 1 hour - renewal needed",
			notAfter: time.Now().Add(1 * time.Hour),
			want:     true,
		},
		{
			name:     "already expired - renewal needed",
			notAfter: time.Now().Add(-24 * time.Hour),
			want:     true,
		},
		{
			name:     "expires in 90 days - no renewal needed",
			notAfter: time.Now().Add(90 * 24 * time.Hour),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cert := &models.Certificate{
				NotAfter: tt.notAfter,
			}
			got := NeedsRenewal(cert)
			if got != tt.want {
				t.Errorf("NeedsRenewal() = %v, want %v (notAfter=%v, timeUntil=%v)",
					got, tt.want, tt.notAfter, time.Until(tt.notAfter))
			}
		})
	}
}

func TestSplitCertKey(t *testing.T) {
	// Create sample PEM data with a certificate and key.
	certBlock := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("fake-cert-data-for-testing"),
	}
	keyBlock := &pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: []byte("fake-key-data-for-testing"),
	}

	combined := append(pem.EncodeToMemory(certBlock), pem.EncodeToMemory(keyBlock)...)

	certPEM, keyPEM, err := splitCertKey(combined)
	if err != nil {
		t.Fatalf("splitCertKey() error = %v", err)
	}

	// Verify we can parse the returned PEM blocks.
	parsedCert, _ := pem.Decode([]byte(certPEM))
	if parsedCert == nil {
		t.Fatal("failed to decode cert PEM from splitCertKey result")
	}
	if parsedCert.Type != "CERTIFICATE" {
		t.Errorf("cert block type = %q, want %q", parsedCert.Type, "CERTIFICATE")
	}

	parsedKey, _ := pem.Decode([]byte(keyPEM))
	if parsedKey == nil {
		t.Fatal("failed to decode key PEM from splitCertKey result")
	}
	if parsedKey.Type != "EC PRIVATE KEY" {
		t.Errorf("key block type = %q, want %q", parsedKey.Type, "EC PRIVATE KEY")
	}
}

func TestSplitCertKeyMultipleCerts(t *testing.T) {
	// Simulate a full chain: leaf + intermediate.
	leafBlock := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("leaf-cert-data"),
	}
	intermediateBlock := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("intermediate-cert-data"),
	}
	keyBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: []byte("rsa-key-data"),
	}

	combined := append(pem.EncodeToMemory(leafBlock), pem.EncodeToMemory(intermediateBlock)...)
	combined = append(combined, pem.EncodeToMemory(keyBlock)...)

	certPEM, keyPEM, err := splitCertKey(combined)
	if err != nil {
		t.Fatalf("splitCertKey() error = %v", err)
	}

	// Count certificate blocks.
	certCount := 0
	rest := []byte(certPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			certCount++
		}
	}
	if certCount != 2 {
		t.Errorf("expected 2 certificate blocks, got %d", certCount)
	}

	// Verify key.
	parsedKey, _ := pem.Decode([]byte(keyPEM))
	if parsedKey == nil {
		t.Fatal("failed to decode key PEM")
	}
	if parsedKey.Type != "RSA PRIVATE KEY" {
		t.Errorf("key block type = %q, want %q", parsedKey.Type, "RSA PRIVATE KEY")
	}
}

func TestSplitCertKeyNoCert(t *testing.T) {
	keyBlock := &pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: []byte("key-data"),
	}
	data := pem.EncodeToMemory(keyBlock)

	_, _, err := splitCertKey(data)
	if err == nil {
		t.Fatal("expected error when no certificate found, got nil")
	}
}

func TestSplitCertKeyNoKey(t *testing.T) {
	certBlock := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("cert-data"),
	}
	data := pem.EncodeToMemory(certBlock)

	_, _, err := splitCertKey(data)
	if err == nil {
		t.Fatal("expected error when no private key found, got nil")
	}
}

func TestSplitCertKeyEmptyInput(t *testing.T) {
	_, _, err := splitCertKey([]byte{})
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
}

func TestSplitCertKeyPrivateKeyType(t *testing.T) {
	// Test with PKCS#8 PRIVATE KEY type (used by some tools).
	certBlock := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("cert-data"),
	}
	keyBlock := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: []byte("pkcs8-key-data"),
	}

	combined := append(pem.EncodeToMemory(certBlock), pem.EncodeToMemory(keyBlock)...)

	certPEM, keyPEM, err := splitCertKey(combined)
	if err != nil {
		t.Fatalf("splitCertKey() error = %v", err)
	}

	if certPEM == "" {
		t.Error("certPEM should not be empty")
	}

	parsedKey, _ := pem.Decode([]byte(keyPEM))
	if parsedKey == nil {
		t.Fatal("failed to decode key PEM")
	}
	if parsedKey.Type != "PRIVATE KEY" {
		t.Errorf("key block type = %q, want %q", parsedKey.Type, "PRIVATE KEY")
	}
}

func TestEncodeCertAndKey(t *testing.T) {
	// Generate a real ECDSA key for this test.
	key, err := generateTestKey()
	if err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}

	// Create a fake DER chain with one "certificate".
	derChain := [][]byte{
		[]byte("fake-der-cert-1"),
		[]byte("fake-der-cert-2"),
	}

	certPEM, keyPEM, err := encodeCertAndKey(derChain, key)
	if err != nil {
		t.Fatalf("encodeCertAndKey() error = %v", err)
	}

	// Verify cert PEM contains 2 blocks.
	certCount := 0
	rest := []byte(certPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			t.Errorf("unexpected block type in certPEM: %s", block.Type)
		}
		certCount++
	}
	if certCount != 2 {
		t.Errorf("expected 2 CERTIFICATE blocks, got %d", certCount)
	}

	// Verify key PEM.
	keyBlockParsed, _ := pem.Decode([]byte(keyPEM))
	if keyBlockParsed == nil {
		t.Fatal("failed to decode keyPEM")
	}
	if keyBlockParsed.Type != "EC PRIVATE KEY" {
		t.Errorf("key block type = %q, want %q", keyBlockParsed.Type, "EC PRIVATE KEY")
	}
}

func TestNewClient(t *testing.T) {
	// Test that NewClient sets the correct directory URLs.
	tests := []struct {
		name      string
		cfg       ClientConfig
		wantDir   string
	}{
		{
			name: "default production directory",
			cfg: ClientConfig{
				Email: "test@example.com",
			},
			wantDir: LetsEncryptProduction,
		},
		{
			name: "staging overrides directory",
			cfg: ClientConfig{
				Email:   "test@example.com",
				Staging: true,
			},
			wantDir: LetsEncryptStaging,
		},
		{
			name: "custom directory",
			cfg: ClientConfig{
				Email:     "test@example.com",
				Directory: "https://custom-acme.example.com/directory",
			},
			wantDir: "https://custom-acme.example.com/directory",
		},
		{
			name: "staging takes precedence over custom directory",
			cfg: ClientConfig{
				Email:     "test@example.com",
				Directory: "https://custom-acme.example.com/directory",
				Staging:   true,
			},
			wantDir: LetsEncryptStaging,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.cfg)
			if client.directory != tt.wantDir {
				t.Errorf("directory = %q, want %q", client.directory, tt.wantDir)
			}
			if client.email != tt.cfg.Email {
				t.Errorf("email = %q, want %q", client.email, tt.cfg.Email)
			}
			if client.manager == nil {
				t.Error("manager should not be nil")
			}
		})
	}
}

func TestRenewalWindowConstant(t *testing.T) {
	// Verify the renewal window is exactly 30 days.
	expected := 30 * 24 * time.Hour
	if RenewalWindow != expected {
		t.Errorf("RenewalWindow = %v, want %v", RenewalWindow, expected)
	}
}
