package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteKeyPEM_Perms proves private keys land on disk as 0600. Before the
// fix, keys were created with os.Create (umask-derived, typically 0644) and
// only tightened afterwards by an os.Chmod whose error was discarded, leaving a
// creation-time window in which the key was world-readable.
func TestWriteKeyPEM_Perms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.key")

	if err := writeKeyPEM(path, "EC PRIVATE KEY", []byte("not-a-real-key")); err != nil {
		t.Fatalf("writeKeyPEM: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("key perms = %o, want 0600", got)
	}
}

// TestWriteKeyPEM_TightensPreexisting proves that a key path left over from a
// previous run with wider permissions is tightened to 0600 before any key
// material is written (O_CREATE does not re-apply the mode to an existing file).
func TestWriteKeyPEM_TightensPreexisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.key")

	// Pre-create the file world-readable.
	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatalf("pre-create: %v", err)
	}

	if err := writeKeyPEM(path, "EC PRIVATE KEY", []byte("not-a-real-key")); err != nil {
		t.Fatalf("writeKeyPEM: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("key perms = %o, want 0600 (pre-existing wider mode not tightened)", got)
	}
}

// TestWriteKeyPEM_SurfacesError proves a write failure is returned rather than
// swallowed. Targeting a path under a non-existent directory makes the
// create fail regardless of the running uid (so it is reliable under root CI).
func TestWriteKeyPEM_SurfacesError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist", "server.key")

	if err := writeKeyPEM(path, "EC PRIVATE KEY", []byte("not-a-real-key")); err == nil {
		t.Fatal("writeKeyPEM returned nil error for an unwritable path; the error was swallowed")
	}
}
