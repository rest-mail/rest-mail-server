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
	"runtime"
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

// Many parts through one store cost no buffer each, and each is published or left out on its own.
func TestFilesystemObjectsStoreManyWithOneBuffer(t *testing.T) {
	root := t.TempDir()
	store, err := OpenFilesystemImports(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const parts = 200
	// A reader with no WriteTo, as a part being decoded has none.
	source := func(i int) io.Reader { return struct{ io.Reader }{strings.NewReader(fmt.Sprintf("part %d", i))} }
	var before, after runtime.MemStats
	var allocated uint64
	for i := range parts {
		part := source(i)
		runtime.ReadMemStats(&before)
		ref, checksum, size, err := store.Put(context.Background(), part)
		runtime.ReadMemStats(&after)
		allocated += after.TotalAlloc - before.TotalAlloc
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("part %d", i)
		if size != int64(len(want)) || checksum != fmt.Sprintf("%x", sha256.Sum256([]byte(want))) {
			t.Fatalf("part %d: %d %s", i, size, checksum)
		}
		path, err := ResolveFilesystemPath(root, ref)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("part %d: %q %v", i, got, err)
		}
	}
	// Measure the store, not the path-resolution and read-back oracle, whose os.Root
	// allocations vary by platform. Every part still gets its exact bytes and hash checked.
	if each := allocated / parts; each > 8<<10 {
		t.Fatalf("%d bytes a part", each)
	}
	broken := errors.New("broken")
	if _, _, _, err := store.Put(context.Background(), io.MultiReader(strings.NewReader("part"), iotest.ErrReader(broken))); !errors.Is(err, broken) {
		t.Fatalf("failed source: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "imports"))
	if err != nil || len(entries) != parts {
		t.Fatalf("%d entries for %d parts: %v", len(entries), parts, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("unpublished temporary object retained: %s", entry.Name())
		}
	}
}

// A file writing itself or being written to allocates its own buffer unless Copy keeps it from that.
func TestCopyOfAFileUsesItsOwnBuffer(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "copied"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 100 {
		if _, err := Copy(file, struct{ io.Reader }{strings.NewReader("x")}); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if each := (after.TotalAlloc - before.TotalAlloc) / 100; each > 1<<10 {
		t.Fatalf("%d bytes a copy", each)
	}
}

type copyFastPathWriter struct {
	strings.Builder
	stringsWritten int
	bytesWritten   int
	reads          int
}

func (writer *copyFastPathWriter) Write(content []byte) (int, error) {
	writer.bytesWritten++
	return writer.Builder.Write(content)
}

func (writer *copyFastPathWriter) WriteString(content string) (int, error) {
	writer.stringsWritten++
	return writer.Builder.WriteString(content)
}

func (writer *copyFastPathWriter) ReadFrom(io.Reader) (int64, error) {
	writer.reads++
	return 0, errors.New("ReadFrom must not allocate its own copy buffer")
}

func TestCopyPreservesStringWritesWithoutDestinationReadFrom(t *testing.T) {
	writer := new(copyFastPathWriter)
	written, err := Copy(writer, strings.NewReader("source"))
	if err != nil || written != 6 || writer.String() != "source" {
		t.Fatalf("copied %d bytes: %q, %v", written, writer.String(), err)
	}
	if writer.stringsWritten != 1 || writer.bytesWritten != 0 || writer.reads != 0 {
		t.Fatalf("copy paths: string=%d bytes=%d ReadFrom=%d", writer.stringsWritten, writer.bytesWritten, writer.reads)
	}
}
