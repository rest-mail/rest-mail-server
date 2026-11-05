package attachmentstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// copying holds the buffers Copy reads through. A message can name a thousand parts of a few bytes
// each, and a buffer per part is 32 KiB for each of them, whatever its size.
var copying = sync.Pool{New: func() any { return new([32 << 10]byte) }}

// Copy is io.Copy through a buffer that outlives the call, for a caller making many short copies.
// A destination's ReadFrom is passed over, because the one on *os.File allocates a buffer of its own
// for a source that isn't a file, and a part being decoded never is. A source that can write itself
// still does.
func Copy(destination io.Writer, source io.Reader) (int64, error) {
	buffer := copying.Get().(*[32 << 10]byte)
	defer copying.Put(buffer)
	return io.CopyBuffer(struct{ io.Writer }{destination}, source, buffer[:])
}

// PutFilesystem stores imported bytes under a content-addressed name. Filenames
// supplied by a message never enter the path. os.Root confines every operation,
// including existing symlinks, and rename publishes only a completely written file.
func PutFilesystem(ctx context.Context, directory string, data []byte) (ref, checksum string, err error) {
	ref, checksum, _, err = PutFilesystemReader(ctx, directory, bytes.NewReader(data))
	return ref, checksum, err
}

// PutFilesystemReader is PutFilesystem for bytes read as they are stored, none of them held in
// memory, so it also answers how many there were. A source that fails publishes nothing.
func PutFilesystemReader(ctx context.Context, directory string, source io.Reader) (ref, checksum string, size int64, err error) {
	return putFilesystemObject(ctx, directory, "imports", source)
}

func putFilesystemObject(ctx context.Context, directory, subdir string, source io.Reader) (ref, checksum string, size int64, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	store, err := OpenFilesystemObjects(directory, subdir)
	if err != nil {
		return "", "", 0, err
	}
	defer store.Close()
	return store.Put(ctx, source)
}

// FilesystemObjects stores objects under one directory, opening it once, for a caller storing many
// of them: each of a message's parts can be a few bytes, and opening and making sure of the directory
// for each is several times what storing a few bytes costs. It's for one caller at a time.
type FilesystemObjects struct {
	directory, subdir string
	root              *os.Root
	hash              hash.Hash
}

// OpenFilesystemImports opens where PutFilesystemReader stores objects.
func OpenFilesystemImports(directory string) (*FilesystemObjects, error) {
	return OpenFilesystemObjects(directory, "imports")
}

// OpenFilesystemObjects makes subdir of directory if it isn't there, and opens it. os.Root confines
// every operation on it, including existing symlinks.
func OpenFilesystemObjects(directory, subdir string) (*FilesystemObjects, error) {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return nil, err
	}
	top, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }()
	if err := top.MkdirAll(subdir, 0755); err != nil {
		return nil, err
	}
	root, err := top.OpenRoot(subdir)
	if err != nil {
		return nil, err
	}
	return &FilesystemObjects{directory: directory, subdir: subdir, root: root, hash: sha256.New()}, nil
}

// Close lets go of the directory.
func (s *FilesystemObjects) Close() { _ = s.root.Close() }

// Put stores what source reads under a name made of its checksum, publishing it only when it's whole.
func (s *FilesystemObjects) Put(ctx context.Context, source io.Reader) (ref, checksum string, size int64, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	temporary := ".tmp-" + rand.Text()
	f, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", "", 0, err
	}
	published := false
	defer func() {
		if !published {
			_ = s.root.Remove(temporary) // only the unpublished temporary file created here
		}
	}()
	s.hash.Reset()
	size, writeErr := Copy(io.MultiWriter(f, s.hash), source)
	closeErr := f.Close()
	if writeErr != nil {
		return "", "", 0, writeErr
	}
	if closeErr != nil {
		return "", "", 0, closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	checksum = hex.EncodeToString(s.hash.Sum(nil))
	object := checksum + ".bin"
	if err := s.root.Rename(temporary, object); err != nil {
		return "", "", 0, err
	}
	published = true
	return filepath.Join(s.directory, s.subdir, object), checksum, size, nil
}
