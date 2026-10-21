package main

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

// Reproduce the persistent files left if a process exits between the two
// publication renames: old certificate, new key, and a hard-linked old key.
func interruptedACMEPair(t *testing.T, dir, name string, validFor time.Duration) (cert, key, stage string) {
	t.Helper()
	now := time.Now()
	cert, key = renewalTestPair(t, now.Add(-time.Hour), now.Add(validFor), name)
	if err := writeServingPair(dir, name, cert, key); err != nil {
		t.Fatal(err)
	}
	stage, err := os.MkdirTemp(dir, ".acme-pair-")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, name+".key")
	if err := os.Link(keyPath, filepath.Join(stage, "previous.key")); err != nil {
		t.Fatal(err)
	}
	newCert, newKey := renewalTestPair(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), name)
	if err := os.WriteFile(filepath.Join(stage, "new.crt"), []byte(newCert), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(keyPath, []byte(newKey), 0600); err != nil {
		t.Fatal(err)
	}
	return cert, key, stage
}

func assertACMEServingFiles(t *testing.T, dir, name, cert, key string) {
	t.Helper()
	for suffix, want := range map[string]string{".crt": cert, ".key": key} {
		got, err := os.ReadFile(filepath.Join(dir, name+suffix))
		if err != nil || string(got) != want {
			t.Fatalf("serving %s differs from expected pair: %v", suffix, err)
		}
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key")); err != nil {
		t.Fatalf("serving pair does not load: %v", err)
	}
}

func TestRenewACME_RecoversInterruptedPublicationBeforeReuse(t *testing.T) {
	const name = "mail.example.test"
	for _, missingKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "mismatched key", true: "missing key"}[missingKey], func(t *testing.T) {
			dir := t.TempDir()
			cert, key, stage := interruptedACMEPair(t, dir, name, 90*24*time.Hour)
			if missingKey {
				if err := os.Remove(filepath.Join(dir, name+".key")); err != nil {
					t.Fatal(err)
				}
			}
			// An invalid listener proves recovery and reuse finish without
			// opening a listener, creating an account, or reaching the CA.
			if err := renewACME(context.Background(), dir, "invalid listen address", acme.IssueConfig{
				Domains: []string{name}, Directory: "http://127.0.0.1:1/directory",
			}, acme.RenewalWindow); err != nil {
				t.Fatalf("recovery did not allow reuse: %v", err)
			}
			assertACMEServingFiles(t, dir, name, cert, key)
			info, err := os.Stat(filepath.Join(dir, name+".key"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("recovered key permissions: %v (%v)", info, err)
			}
			if backup, err := os.ReadFile(filepath.Join(stage, "previous.key")); err != nil || string(backup) != key {
				t.Fatalf("recovery did not retain backup: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 4 {
				t.Fatalf("want pair, staging directory and lock only: %v (%v)", entries, err)
			}
		})
	}
}

func TestRenewACME_RecoveryPreservesPairWhenIssuanceFails(t *testing.T) {
	const name = "mail.example.test"
	for _, window := range []time.Duration{0, acme.RenewalWindow} {
		t.Run(window.String(), func(t *testing.T) {
			dir := t.TempDir()
			cert, key, _ := interruptedACMEPair(t, dir, name, 24*time.Hour)
			err := renewACME(context.Background(), dir, "127.0.0.1:0", acme.IssueConfig{
				Domains: []string{name}, Directory: "http://127.0.0.1:1/directory",
			}, window)
			if err == nil {
				t.Fatal("issuance succeeded without a CA")
			}
			assertACMEServingFiles(t, dir, name, cert, key)
		})
	}
}

func TestRecoverACME_CompletePairIsNotRolledBack(t *testing.T) {
	const name = "mail.example.test"
	dir := t.TempDir()
	_, _, stage := interruptedACMEPair(t, dir, name, 24*time.Hour)
	// The certificate rename also completed, but the process exited before
	// cleaning up the stage directory. The new pair must remain untouched.
	cert, err := os.ReadFile(filepath.Join(stage, "new.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(stage, "new.crt"), filepath.Join(dir, name+".crt")); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(dir, name+".key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverACMEServingKey(dir, name); err != nil {
		t.Fatal(err)
	}
	assertACMEServingFiles(t, dir, name, string(cert), string(key))
}

func TestRecoverACME_IgnoresUnrelatedOrDamagedBackups(t *testing.T) {
	const name = "mail.example.test"
	dir := t.TempDir()
	_, _, stage := interruptedACMEPair(t, dir, name, 24*time.Hour)
	_, otherKey := renewalTestPair(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), "other.example.test")
	for _, backup := range []string{otherKey, "damaged"} {
		// Replace the hard link atomically, preserving the original inode.
		if err := writeFileAtomic(filepath.Join(stage, "previous.key"), []byte(backup), 0600); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(filepath.Join(dir, name+".key"))
		if err != nil {
			t.Fatal(err)
		}
		if err := recoverACMEServingKey(dir, name); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(filepath.Join(dir, name+".key"))
		if err != nil || string(after) != string(before) {
			t.Fatalf("unrelated backup changed serving key: %v", err)
		}
	}
}

func TestRenewACME_RecoveryRequiresIssuerLock(t *testing.T) {
	const name = "mail.example.test"
	dir := t.TempDir()
	_, _, _ = interruptedACMEPair(t, dir, name, 90*24*time.Hour)
	before, err := os.ReadFile(filepath.Join(dir, name+".key"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := lockACMEIssuer(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	err = renewACME(context.Background(), dir, "invalid listen address", acme.IssueConfig{Domains: []string{name}}, acme.RenewalWindow)
	if err == nil || !strings.Contains(err.Error(), "another ACME issuer") {
		t.Fatalf("expected lock refusal, got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, name+".key"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("recovery ran without the issuer lock: %v", err)
	}
}
