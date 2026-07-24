package handlers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveAttachmentPath covers the M-5 (CWE-59) path confinement: retrieval
// must stay inside the attachments root even when a symlink inside the tree
// points elsewhere. A temp dir stands in for the production /attachments root so
// the test can plant real files and symlinks.
func TestResolveAttachmentPath(t *testing.T) {
	root := t.TempDir()

	// A legitimate stored file inside a dated subdir.
	subdir := filepath.Join(root, "2026", "07", "24")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	goodFile := filepath.Join(subdir, "abc123-report.pdf")
	if err := os.WriteFile(goodFile, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A secret file OUTSIDE the root, and a symlink inside the root pointing to
	// it — the classic symlink escape.
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(root, "escape")
	if err := os.Symlink(secret, escapeLink); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	// A symlink inside the root pointing to another file inside the root — this
	// must remain allowed.
	inLink := filepath.Join(root, "alias.pdf")
	if err := os.Symlink(goodFile, inLink); err != nil {
		t.Fatal(err)
	}

	t.Run("legitimate file resolves", func(t *testing.T) {
		got, err := resolveAttachmentPath(root, goodFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, _ := filepath.EvalSymlinks(goodFile)
		if got != want {
			t.Fatalf("resolved = %q, want %q", got, want)
		}
	})

	t.Run("symlink within root allowed", func(t *testing.T) {
		got, err := resolveAttachmentPath(root, inLink)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, _ := filepath.EvalSymlinks(goodFile)
		if got != want {
			t.Fatalf("resolved = %q, want %q", got, want)
		}
	})

	t.Run("symlink escaping root rejected", func(t *testing.T) {
		_, err := resolveAttachmentPath(root, escapeLink)
		if !errors.Is(err, errAttachmentPathEscape) {
			t.Fatalf("expected errAttachmentPathEscape, got %v", err)
		}
	})

	t.Run("path outside root rejected", func(t *testing.T) {
		_, err := resolveAttachmentPath(root, secret)
		if !errors.Is(err, errAttachmentPathEscape) {
			t.Fatalf("expected errAttachmentPathEscape, got %v", err)
		}
	})

	t.Run("parent traversal rejected", func(t *testing.T) {
		_, err := resolveAttachmentPath(root, filepath.Join(root, "..", "escape.txt"))
		if !errors.Is(err, errAttachmentPathEscape) {
			t.Fatalf("expected errAttachmentPathEscape, got %v", err)
		}
	})

	t.Run("relative traversal ref rejected", func(t *testing.T) {
		_, err := resolveAttachmentPath(root, "attachments/../../etc/passwd")
		if !errors.Is(err, errAttachmentPathEscape) {
			t.Fatalf("expected errAttachmentPathEscape, got %v", err)
		}
	})

	t.Run("missing file is not an escape", func(t *testing.T) {
		_, err := resolveAttachmentPath(root, filepath.Join(subdir, "does-not-exist"))
		if err == nil {
			t.Fatal("expected an error for a missing file")
		}
		if errors.Is(err, errAttachmentPathEscape) {
			t.Fatalf("missing file should not be reported as an escape, got %v", err)
		}
	})
}
