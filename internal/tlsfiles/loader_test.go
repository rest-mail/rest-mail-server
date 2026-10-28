package tlsfiles

import (
	"bytes"
	"crypto/tls"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/mtls/mtlstest"
)

func replace(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path+".new", data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderBoundsChecksAndRecovers(t *testing.T) {
	var logs bytes.Buffer
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLog)
	p, err := mtlstest.Write(t.TempDir(), "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	l, err := New(p.ServerCert, p.ServerKey, CheckInterval)
	if err != nil {
		t.Fatal(err)
	}
	initial := l.Current()
	now := time.Now()
	l.now = func() time.Time { return now }
	l.nextCheck = now.Add(CheckInterval)

	replace(t, p.ServerCert, []byte("truncated certificate"))
	if got := l.Current(); got != initial || l.loadErr != nil {
		t.Fatal("certificate files were checked before the interval elapsed")
	}
	now = now.Add(CheckInterval)
	if got, err := l.GetCertificate(nil); got != initial || err != nil {
		t.Fatal("broken renewal failed to retain the cached pair")
	}
	if l.loadErr == nil {
		t.Fatal("broken renewal was not detected")
	}
	for range 3 {
		now = now.Add(CheckInterval)
		if l.Current() != initial {
			t.Fatal("repeated failure displaced cached pair")
		}
	}
	if got := strings.Count(logs.String(), "level=ERROR"); got != 1 {
		t.Fatalf("persistent renewal failure produced %d error logs, want 1", got)
	}

	renewed, err := mtlstest.Write(t.TempDir(), "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(renewed.ServerCert)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(renewed.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	replace(t, p.ServerKey, keyPEM)
	replace(t, p.ServerCert, certPEM)
	// Failure retries must also be bounded; fixed files wait for the next check.
	if l.Current() != initial || l.loadErr == nil {
		t.Fatal("failed loads were not throttled")
	}
	now = now.Add(CheckInterval)
	updated := l.Current()
	if updated == initial || l.loadErr != nil {
		t.Fatal("valid replacement did not recover")
	}
	if bytes.Equal(updated.Certificate[0], initial.Certificate[0]) {
		t.Fatal("old certificate still loaded")
	}
	if l.Current() != updated {
		t.Fatal("unchanged files replaced cached pair")
	}
	// Another failure after recovery must be visible again, including missing files.
	if err := os.Rename(p.ServerKey, p.ServerKey+".away"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(CheckInterval)
	if got, err := l.GetClientCertificate(nil); got != updated || err != nil {
		t.Fatal("missing key displaced last pair")
	}
	if got := strings.Count(logs.String(), "level=ERROR"); got != 2 {
		t.Fatalf("want a new error after recovery, got %d", got)
	}
}

func TestLoaderDetectsReplacementWithPreservedTimestamp(t *testing.T) {
	p, err := mtlstest.Write(t.TempDir(), "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	l, err := New(p.ServerCert, p.ServerKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	initial := l.Current()
	// Same contents, size, and mtime, but a different inode must still reload.
	info, err := os.Stat(p.ServerCert)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.ServerCert)
	if err != nil {
		t.Fatal(err)
	}
	replace(t, p.ServerCert, data)
	if err := os.Chtimes(p.ServerCert, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if l.Current() == initial {
		t.Fatal("replacement inode was not detected")
	}
	// Readers must all see a complete pair when the next check is concurrent.
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cert, err := l.GetCertificate(nil); cert == nil || err != nil {
				t.Error("concurrent load failed")
			}
		}()
	}
	wg.Wait()
}

func TestLoaderRejectsBadInitialPair(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key"), CheckInterval); err == nil {
		t.Fatal("missing initial pair accepted")
	}
	p, err := mtlstest.Write(dir, "mail.test")
	if err != nil {
		t.Fatal(err)
	}
	replace(t, p.ServerCert, []byte("bad PEM"))
	l, err := New(p.ServerCert, p.ServerKey, CheckInterval)
	if err == nil {
		t.Fatal("invalid initial pair accepted")
	}
	if cert, err := l.GetCertificate(&tls.ClientHelloInfo{}); cert != nil || err == nil {
		t.Fatal("bad initial pair was served")
	}
}
