package attachmentstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestImportFilesystemPreservesEmptyAndBinaryObjects(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("historical\x00attachment\xff\r\n")} {
		t.Run(fmt.Sprintf("bytes=%d", len(data)), func(t *testing.T) {
			root := t.TempDir()
			ref, checksum, err := PutFilesystem(context.Background(), root, data)
			if err != nil || checksum != fmt.Sprintf("%x", sha256.Sum256(data)) {
				t.Fatalf("store: %q %v", checksum, err)
			}
			path, err := ResolveFilesystemPath(root, ref)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("object bytes changed: %q %v", got, err)
			}
			second, _, err := PutFilesystem(context.Background(), root, data)
			if err != nil || second != ref {
				t.Fatalf("repeated object: %q %v", second, err)
			}
			entries, err := os.ReadDir(filepath.Join(root, "imports"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("unpublished temporary objects retained: %v %v", entries, err)
			}
		})
	}
}

func TestImportFilesystemRefusesCanceledAndEscapingWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := PutFilesystem(ctx, filepath.Join(t.TempDir(), "absent"), []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write: %v", err)
	}
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "imports")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PutFilesystem(context.Background(), root, []byte("x")); err == nil {
		t.Fatal("write followed a directory symlink outside the store")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("external directory changed: %v %v", entries, err)
	}
}
