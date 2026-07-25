package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/restmail/restmail/internal/instance"
)

// refMode returns the permission bits a plain os.WriteFile(mode) produces under
// the current process umask, so assertions compare like-with-like instead of
// hardcoding a value the umask may clear.
func refMode(t *testing.T, mode os.FileMode) os.FileMode {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ref")
	if err := os.WriteFile(p, []byte("x"), mode); err != nil {
		t.Fatalf("write reference file: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat reference file: %v", err)
	}
	return info.Mode().Perm()
}

// TestWriteScaffoldSecretsMode asserts that scaffold writes secrets.env
// owner-only (0600) so the generated DB password / JWT secret / master key are
// never left world-readable, while the secret-free manifest.yml and config.env
// stay 0644. Guards the fix for #195.
func TestWriteScaffoldSecretsMode(t *testing.T) {
	res, err := instance.Scaffold("mail9.test", t.TempDir(), "testbed")
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	target := filepath.Join(t.TempDir(), "mail9.test")
	if err := writeScaffold(target, res); err != nil {
		t.Fatalf("writeScaffold: %v", err)
	}

	cases := map[string]os.FileMode{
		"secrets.env":  0o600,
		"manifest.yml": 0o644,
		"config.env":   0o644,
	}
	for name, mode := range cases {
		info, err := os.Stat(filepath.Join(target, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got, want := info.Mode().Perm(), refMode(t, mode); got != want {
			t.Errorf("%s mode = %04o, want %04o", name, got, want)
		}
	}

	// The security-critical assertion, stated independently of umask: the
	// secrets file carries no group or world permission bits at all.
	info, err := os.Stat(filepath.Join(target, "secrets.env"))
	if err != nil {
		t.Fatalf("stat secrets.env: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("secrets.env is group/world accessible (mode %04o); must be owner-only (#195)", perm)
	}
}
