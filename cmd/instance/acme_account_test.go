package main

import (
	"crypto/ecdsa"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/restmail/restmail/internal/acme"
)

func TestACMEAccountKey_PersistsAndSeparatesDirectories(t *testing.T) {
	dir := t.TempDir()
	first, err := loadACMEAccountKey(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := loadACMEAccountKey(dir, acme.LetsEncryptProduction)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(again) {
		t.Fatal("default and explicit production directory did not reuse the key")
	}
	for _, directory := range []string{acme.LetsEncryptStaging, "https://ca.example.test/directory"} {
		other, err := loadACMEAccountKey(dir, directory)
		if err != nil {
			t.Fatal(err)
		}
		if first.Equal(other) {
			t.Fatal("different CAs share an account key")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("want three account keys and no temporary files, got %d (%v)", len(entries), err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("account key permissions = %o, want 0600", info.Mode().Perm())
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.WriteFile(path, []byte("corrupt account key"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadACMEAccountKey(dir, ""); err == nil {
		t.Fatal("silently replaced a corrupt account key")
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || string(data) != "corrupt account key" {
			t.Fatal("corrupt key was overwritten")
		}
	}
}

func TestACMEAccountKey_ConcurrentCreatorsUseSameKey(t *testing.T) {
	dir := t.TempDir()
	const workers = 8
	keys := make([]*ecdsa.PrivateKey, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			<-start
			keys[i], errs[i] = loadACMEAccountKey(dir, "")
		})
	}
	close(start)
	wg.Wait()
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("creator %d: %v", i, errs[i])
		}
		if !keys[0].Equal(keys[i]) {
			t.Fatal("concurrent creators obtained different account identities")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one key and no temporary files, got %d (%v)", len(entries), err)
	}
}

func TestReadACMEAccountKey_RejectsMalformedKey(t *testing.T) {
	for _, data := range []string{
		"not PEM",
		"-----BEGIN EC PRIVATE KEY-----\nAQID\n-----END EC PRIVATE KEY-----\n",
		"-----BEGIN PRIVATE KEY-----\nAQID\n-----END PRIVATE KEY-----\n",
	} {
		path := filepath.Join(t.TempDir(), "account.key")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readACMEAccountKey(path); err == nil {
			t.Fatal("accepted malformed account key")
		}
	}
}
