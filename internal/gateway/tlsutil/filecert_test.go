package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func writePair(t *testing.T, dir, name string, certPEM, keyPEM []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".crt"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func replaceFile(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func certSerial(t *testing.T, cert *tls.Certificate) string {
	t.Helper()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.String()
}

func TestFileCertLoaderReloadsCompletePair(t *testing.T) {
	dir := t.TempDir()
	_, oldCert, oldKey := selfSigned(t, "mx.example.test")
	_, newCert, newKey := selfSignedForPEM(t, "mx.example.test", "imap.example.test")
	writePair(t, dir, "mx.example.test", oldCert, oldKey)
	certPath := filepath.Join(dir, "mx.example.test.crt")
	keyPath := filepath.Join(dir, "mx.example.test.key")
	loader, err := NewFileCertLoader(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	initial := loader.Current()
	sni := NewSNICertLoader(dir, initial)
	sni.SetFallbackCurrent(loader.Current)
	sni.SetPrimaryPaths(certPath, keyPath)
	db := NewDBCertLoader(nil, "", initial)
	db.SetFallbackCurrent(loader.Current)
	if got, err := sni.GetCertificate(&tls.ClientHelloInfo{ServerName: "mx.example.test"}); err != nil || got != initial {
		t.Fatalf("initial primary SNI certificate = %v, %v", got, err)
	}

	// ACME publishes the key first. A handshake in between must retain the
	// complete old pair, then a handshake after the cert rename sees the new one.
	replaceFile(t, keyPath, newKey)
	if got := loader.Current(); got != initial {
		t.Fatal("incomplete replacement displaced the valid certificate")
	}
	replaceFile(t, certPath, newCert)
	updated := loader.Current()
	if updated == initial {
		t.Fatal("certificate was not reloaded after publication")
	}
	if got := loader.Current(); got != updated {
		t.Fatal("unchanged pair was reloaded")
	}
	if certSerial(t, initial) == certSerial(t, updated) {
		t.Fatal("the certificate serial did not change")
	}
	if !certCovers(updated, "imap.example.test") {
		t.Fatal("renewed SAN not served")
	}
	if _, err := loader.GetCertificate(&tls.ClientHelloInfo{ServerName: "unrelated.test"}); err == nil {
		t.Fatal("primary loader served a certificate for an unrelated SNI name")
	}
	for _, name := range []string{"mx.example.test", "imap.example.test", ""} {
		got, err := sni.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
		if err != nil || got != updated {
			t.Fatalf("SNI %q did not serve renewed primary pair: %v, %v", name, got, err)
		}
	}
	if got, err := db.GetCertificate(&tls.ClientHelloInfo{}); err != nil || got != updated {
		t.Fatalf("DB loader no-SNI fallback did not refresh: %v, %v", got, err)
	}

	// A later broken replacement must not evict the good pair.
	replaceFile(t, certPath, []byte("broken"))
	if got := loader.Current(); got != updated {
		t.Fatal("invalid replacement displaced the valid certificate")
	}
}

func TestFileCertLoaderNoSNIHandshakeUsesRenewedPair(t *testing.T) {
	dir := t.TempDir()
	_, oldCert, oldKey := selfSigned(t, "mx.example.test")
	_, newCert, newKey := selfSignedForPEM(t, "mx.example.test", "imap.example.test")
	writePair(t, dir, "mx.example.test", oldCert, oldKey)
	loader, err := NewFileCertLoader(filepath.Join(dir, "mx.example.test.crt"), filepath.Join(dir, "mx.example.test.key"))
	if err != nil {
		t.Fatal(err)
	}
	config := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: loader.GetCertificate,
	}
	handshakeSerial := func() string {
		t.Helper()
		serverConn, clientConn := net.Pipe()
		server := tls.Server(serverConn, config)
		client := tls.Client(clientConn, &tls.Config{
			MinVersion: tls.VersionTLS12,
			// This test deliberately sends no SNI; the generated certs are local.
			InsecureSkipVerify: true,
		})
		serverDone := make(chan error, 1)
		go func() { serverDone <- server.Handshake() }()
		if err := client.Handshake(); err != nil {
			t.Fatal(err)
		}
		serial := client.ConnectionState().PeerCertificates[0].SerialNumber.String()
		_ = clientConn.Close()
		_ = serverConn.Close()
		if err := <-serverDone; err != nil {
			t.Fatal(err)
		}
		return serial
	}
	before := handshakeSerial()
	replaceFile(t, filepath.Join(dir, "mx.example.test.key"), newKey)
	replaceFile(t, filepath.Join(dir, "mx.example.test.crt"), newCert)
	if after := handshakeSerial(); after == before {
		t.Fatal("no-SNI TLS handshake served the startup certificate after renewal")
	}
}

func selfSignedForPEM(t *testing.T, names ...string) (tls.Certificate, []byte, []byte) {
	t.Helper()
	cert := selfSignedFor(t, names...)
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return cert,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
