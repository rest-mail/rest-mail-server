package attachmentstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
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

func TestImportFilesystemReaderAnswersItsSizeAndPublishesOnlyAWholeSource(t *testing.T) {
	root := t.TempDir()
	data := []byte("historical\x00attachment\xff\r\n")
	ref, checksum, size, err := PutFilesystemReader(context.Background(), root, bytes.NewReader(data))
	if err != nil || size != int64(len(data)) || checksum != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatalf("store: %d %q %v", size, checksum, err)
	}
	same, _, err := PutFilesystem(context.Background(), root, data)
	if err != nil || same != ref {
		t.Fatalf("the two ways of storing differ: %q %q %v", ref, same, err)
	}
	broken := errors.New("broken")
	if _, _, _, err := PutFilesystemReader(context.Background(), root, io.MultiReader(strings.NewReader("part"), iotest.ErrReader(broken))); !errors.Is(err, broken) {
		t.Fatalf("failed source: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "imports"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("a failed source left: %v %v", entries, err)
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
