package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

// TestWriteServingPair proves the ACME provider lands the pair where the manual
// provider expects it, <primary>.{crt,key}, with the key 0600 and no temporary
// files left behind.
func TestWriteServingPair(t *testing.T) {
	dir := t.TempDir()
	if err := writeServingPair(dir, "mail.example.test", "CERT", "KEY"); err != nil {
		t.Fatalf("writeServingPair: %v", err)
	}
	for name, want := range map[string]struct {
		body string
		perm os.FileMode
	}{
		"mail.example.test.crt": {"CERT", 0644},
		"mail.example.test.key": {"KEY", 0600},
	} {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want.body {
			t.Fatalf("%s = %q (%v), want %q", name, got, err, want.body)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != want.perm {
			t.Errorf("%s perms = %o, want %o", name, info.Mode().Perm(), want.perm)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("dir holds %d entries, want only the pair", len(entries))
	}
}

// A certificate publication failure must not leave the new key behind. A
// directory at the certificate destination deterministically makes rename fail,
// including when the tests run as root.
func TestWriteServingPair_CertificateFailureRestoresKey(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint("existing-key=", existing), func(t *testing.T) {
			dir := t.TempDir()
			const name = "mail.example.test"
			keyPath := filepath.Join(dir, name+".key")
			certPath := filepath.Join(dir, name+".crt")
			var previous os.FileInfo
			if existing {
				if err := os.WriteFile(keyPath, []byte("OLD KEY"), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				previous, err = os.Stat(keyPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(certPath, 0700); err != nil {
				t.Fatal(err)
			}
			if err := writeServingPair(dir, name, "NEW CERT", "NEW KEY"); err == nil {
				t.Fatal("publication succeeded over a directory")
			}
			got, err := os.ReadFile(keyPath)
			if existing {
				if err != nil || string(got) != "OLD KEY" {
					t.Fatalf("previous key not restored: %q (%v)", got, err)
				}
				info, err := os.Stat(keyPath)
				if err != nil || !os.SameFile(previous, info) || info.Mode().Perm() != 0600 {
					t.Fatalf("rollback changed key inode or permissions: %v (%v)", info, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("failed first issuance left a key: %q (%v)", got, err)
			}
			entries, err := os.ReadDir(dir)
			wantEntries := 1
			if existing {
				wantEntries++
			}
			if err != nil || len(entries) != wantEntries {
				t.Fatalf("temporary files left after failure: %v (%v)", entries, err)
			}
		})
	}
}

func TestWriteServingPair_KeyFailurePreservesCertificate(t *testing.T) {
	dir := t.TempDir()
	const name = "mail.example.test"
	certPath := filepath.Join(dir, name+".crt")
	if err := os.WriteFile(certPath, []byte("OLD CERT"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, name+".key"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeServingPair(dir, name, "NEW CERT", "NEW KEY"); err == nil {
		t.Fatal("publication succeeded over a key directory")
	}
	got, err := os.ReadFile(certPath)
	if err != nil || string(got) != "OLD CERT" {
		t.Fatalf("key failure changed certificate: %q (%v)", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary files left after key failure: %v (%v)", entries, err)
	}
}

func TestWriteServingPair_RotatesUsablePair(t *testing.T) {
	dir := t.TempDir()
	const name = "mail.example.test"
	now := time.Now()
	oldCert, oldKey := renewalTestPair(t, now.Add(-time.Hour), now.Add(24*time.Hour), name)
	newCert, newKey := renewalTestPair(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), name)
	if err := writeServingPair(dir, name, oldCert, oldKey); err != nil {
		t.Fatal(err)
	}
	if err := writeServingPair(dir, name, newCert, newKey); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")); err != nil {
		t.Fatalf("renewed pair cannot be served: %v", err)
	}
	for suffix, want := range map[string]string{".crt": newCert, ".key": newKey} {
		got, err := os.ReadFile(filepath.Join(dir, name+suffix))
		if err != nil || string(got) != want {
			t.Fatalf("renewed %s not published: %v", suffix, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary files left after renewal: %v (%v)", entries, err)
	}
}

// A failed issuance preserves the account identity but writes no serving pair.
func TestIssueACME_FailurePreservesAccountOnly(t *testing.T) {
	dir := t.TempDir()
	err := issueACME(context.Background(), dir, "127.0.0.1:0", acme.IssueConfig{
		Directory: "http://127.0.0.1:1/directory",
		Domains:   []string{"mail.example.test"},
	})
	if err == nil {
		t.Fatal("issueACME succeeded without a CA")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want only the account key after failure, got %d files (%v)", len(entries), err)
	}
	if _, err := readACMEAccountKey(filepath.Join(dir, entries[0].Name())); err != nil {
		t.Fatalf("account key not preserved: %v", err)
	}
}

func TestIssueACME_RequiresDomains(t *testing.T) {
	if err := issueACME(context.Background(), t.TempDir(), "127.0.0.1:0", acme.IssueConfig{}); err == nil {
		t.Fatal("issueACME accepted no domains")
	}
}
