package tlsutil

import (
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func handshakeSerialFor(t *testing.T, config *tls.Config, name string) string {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	_ = serverConn.SetDeadline(time.Now().Add(2 * time.Second))
	_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
	server := tls.Server(serverConn, config)
	client := tls.Client(clientConn, &tls.Config{ServerName: name, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	done := make(chan error, 1)
	go func() { done <- server.Handshake() }()
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return client.ConnectionState().PeerCertificates[0].SerialNumber.String()
}

func awaitHandshakeSerial(t *testing.T, config *tls.Config, name, want string) {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for {
		if got := handshakeSerialFor(t, config, name); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("handshake did not serve serial %s after renewal", want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPrimaryHandshakesReloadAndRetainPair(t *testing.T) {
	for _, name := range []string{"", "mx.example.test"} {
		t.Run("SNI="+name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			old, oldCert, oldKey := selfSigned(t, "mx.example.test")
			renewed, newCert, newKey := selfSignedForPEM(t, "mx.example.test")
			writePair(t, dir, "primary", oldCert, oldKey)
			loader, err := NewFileCertLoader(filepath.Join(dir, "primary.crt"), filepath.Join(dir, "primary.key"))
			if err != nil {
				t.Fatal(err)
			}
			config := &tls.Config{GetCertificate: loader.GetCertificate, MinVersion: tls.VersionTLS12}
			if got := handshakeSerialFor(t, config, name); got != certSerial(t, &old) {
				t.Fatal("wrong initial certificate")
			}
			replaceFile(t, filepath.Join(dir, "primary.key"), newKey)
			replaceFile(t, filepath.Join(dir, "primary.crt"), newCert)
			awaitHandshakeSerial(t, config, name, certSerial(t, &renewed))
			replaceFile(t, filepath.Join(dir, "primary.crt"), []byte("broken renewal"))
			time.Sleep(5100 * time.Millisecond)
			if got := handshakeSerialFor(t, config, name); got != certSerial(t, &renewed) {
				t.Fatal("broken renewal displaced good pair")
			}
		})
	}
}

func TestSNIHandshakeRetainsPairDuringRenewal(t *testing.T) {
	dir := t.TempDir()
	old, oldCert, oldKey := selfSigned(t, "served.test")
	renewed, newCert, newKey := selfSignedForPEM(t, "served.test")
	writePair(t, dir, "served.test", oldCert, oldKey)
	loader := NewSNICertLoader(dir, nil)
	config := &tls.Config{GetCertificate: loader.GetCertificate, MinVersion: tls.VersionTLS12}
	oldSerial := certSerial(t, &old)
	if got := handshakeSerialFor(t, config, "served.test"); got != oldSerial {
		t.Fatal("wrong initial certificate")
	}
	// Invalidation used to throw away the valid pair between file renames.
	replaceFile(t, filepath.Join(dir, "served.test.key"), newKey)
	loader.Invalidate("served.test")
	time.Sleep(5100 * time.Millisecond)
	if got := handshakeSerialFor(t, config, "served.test"); got != oldSerial {
		t.Fatal("half-published pair displaced old certificate")
	}
	replaceFile(t, filepath.Join(dir, "served.test.crt"), newCert)
	awaitHandshakeSerial(t, config, "served.test", certSerial(t, &renewed))
	replaceFile(t, filepath.Join(dir, "served.test.crt"), []byte("broken renewal"))
	loader.Invalidate("served.test")
	time.Sleep(5100 * time.Millisecond)
	if got := handshakeSerialFor(t, config, "served.test"); got != certSerial(t, &renewed) {
		t.Fatal("broken renewal displaced good certificate")
	}
}

func TestSNIHandshakeReloadsSecretSymlink(t *testing.T) {
	dir := t.TempDir()
	old, oldCert, oldKey := selfSigned(t, "served.test")
	renewed, newCert, newKey := selfSignedForPEM(t, "served.test")
	for _, version := range []string{"old", "new"} {
		if err := os.Mkdir(filepath.Join(dir, version), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writePair(t, filepath.Join(dir, "old"), "served.test", oldCert, oldKey)
	writePair(t, filepath.Join(dir, "new"), "served.test", newCert, newKey)
	if err := os.Symlink("old", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"crt", "key"} {
		if err := os.Symlink("..data/served.test."+ext, filepath.Join(dir, "served.test."+ext)); err != nil {
			t.Fatal(err)
		}
	}
	loader := NewSNICertLoader(dir, nil)
	config := &tls.Config{GetCertificate: loader.GetCertificate, MinVersion: tls.VersionTLS12}
	if got := handshakeSerialFor(t, config, "served.test"); got != certSerial(t, &old) {
		t.Fatal("wrong initial certificate")
	}
	// Kubernetes replaces ..data, leaving the public symlinks unchanged. No watcher required.
	if err := os.Symlink("new", filepath.Join(dir, "..next")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..next"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	awaitHandshakeSerial(t, config, "served.test", certSerial(t, &renewed))
}
