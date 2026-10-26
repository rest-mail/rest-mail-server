package attachmentstore

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/attachmentstore/s3test"
)

func TestIndependentFilesystemCopiesHaveSeparateDeletionLifetimes(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("part\xff\x00")} {
		root := t.TempDir()
		original, sum, err := PutFilesystem(t.Context(), root, data)
		if err != nil {
			t.Fatal(err)
		}
		copy, copySum, err := PutIndependentFilesystem(t.Context(), root, data)
		if err != nil {
			t.Fatal(err)
		}
		other, _, err := PutIndependentFilesystem(t.Context(), root, data)
		if err != nil {
			t.Fatal(err)
		}
		if copy == original || copy == other || sum != copySum {
			t.Fatal("independent copy shares object identity or changes checksum")
		}
		// Every path belongs to this test's temporary store.
		if err := os.Remove(original); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(copy)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("deleting the equal original broke its forward")
		}
		if err := os.Remove(copy); err != nil {
			t.Fatal(err)
		}
		got, err = os.ReadFile(other)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("deleting a forward broke another copy")
		}
	}
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "copies")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PutIndependentFilesystem(t.Context(), root, []byte("x")); err == nil {
		t.Fatal("copy directory symlink escaped storage")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("outside directory changed")
	}
}

func TestIndependentS3CopiesHaveSeparateDeletionLifetimes(t *testing.T) {
	f := s3test.New(t)
	s := NewS3(f.Config)
	data := []byte("part\xff\x00")
	original, sum, err := s.Put(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	copy, copySum, err := s.PutIndependent(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.PutIndependent(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	if copy == original || copy == other || sum != copySum {
		t.Fatal("copy shares original object identity")
	}
	if err := s.Delete(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	file, err := s.Open(t.Context(), copy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("deleting the original broke the forward")
	}
	if err := s.Delete(t.Context(), copy); err != nil {
		t.Fatal(err)
	}
	file, err = s.Open(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	before := f.Requests.Load()
	for _, ref := range []string{s.prefix + "copies/../" + sum, s.prefix + "copies/" + strings.Repeat("A", 32) + "/" + sum, s.prefix + "copies/" + strings.Repeat("0", 32) + "/../" + sum} {
		if _, err := s.Open(t.Context(), ref); err == nil {
			t.Fatal("invalid copy reference accepted")
		}
	}
	if f.Requests.Load() != before {
		t.Fatal("invalid copy reference reached storage")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := s.PutIndependent(ctx, data); err == nil {
		t.Fatal("canceled copy uploaded")
	}
}
