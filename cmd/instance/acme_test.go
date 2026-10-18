package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

// TestIssueACME_FailureWritesNothing proves a failed issuance (here: no CA
// listening) leaves the certs directory untouched.
func TestIssueACME_FailureWritesNothing(t *testing.T) {
	dir := t.TempDir()
	err := issueACME(context.Background(), dir, "127.0.0.1:0", acme.IssueConfig{
		Directory: "http://127.0.0.1:1/directory",
		Domains:   []string{"mail.example.test"},
	})
	if err == nil {
		t.Fatal("issueACME succeeded without a CA")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("failed issuance wrote %d files", len(entries))
	}
}

func TestIssueACME_RequiresDomains(t *testing.T) {
	if err := issueACME(context.Background(), t.TempDir(), "127.0.0.1:0", acme.IssueConfig{}); err == nil {
		t.Fatal("issueACME accepted no domains")
	}
}
