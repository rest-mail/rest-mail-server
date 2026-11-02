package messagestore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// Filesystem keeps objects under a directory, normally storage attached to every container that
// handles mail. os.Root confines every operation, symlinks included.
type Filesystem struct {
	directory string
}

// NewFilesystem is a store under directory, which it makes if need be.
func NewFilesystem(directory string) *Filesystem {
	return &Filesystem{directory: directory}
}

func (f *Filesystem) root() (*os.Root, error) {
	if err := os.MkdirAll(f.directory, 0o750); err != nil {
		return nil, err
	}
	return os.OpenRoot(f.directory)
}

// Put writes source to a temporary file, hashing as it goes, and renames it into place only
// when it is complete, so a reader never sees part of an object.
func (f *Filesystem) Put(ctx context.Context, source io.Reader, limit int64) (Object, error) {
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}
	root, err := f.root()
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = root.Close() }()
	temporary := ".tmp-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = root.Remove(temporary) }() // only the file created above; gone already once renamed
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hash), limitReader(contextReader{ctx, source}, limit))
	closeErr := file.Close()
	if copyErr != nil {
		return Object{}, copyErr
	}
	if closeErr != nil {
		return Object{}, closeErr
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	ref := checksum[:2] + "/" + checksum
	if err := root.MkdirAll(checksum[:2], 0o750); err != nil {
		return Object{}, err
	}
	if err := root.Rename(temporary, ref); err != nil {
		return Object{}, err
	}
	return Object{Ref: ref, Size: size, Checksum: checksum}, nil
}

// contextReader stops a long copy when its request is cancelled.
type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.source.Read(p)
}

func validFilesystemRef(ref string) bool {
	dir, name := path.Split(ref)
	decoded, err := hex.DecodeString(name)
	return err == nil && len(decoded) == sha256.Size && name == strings.ToLower(name) && dir == name[:2]+"/"
}

type fileReader struct {
	*os.File
	size int64
}

func (r fileReader) Size() int64 { return r.size }

// Open reads an object.
func (f *Filesystem) Open(ctx context.Context, ref string) (Reader, error) {
	if !validFilesystemRef(ref) {
		return nil, ErrInvalidRef
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := f.root()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(ref)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("measure the message object: %w", err)
	}
	return fileReader{file, info.Size()}, nil
}

// Delete removes an object.
func (f *Filesystem) Delete(ctx context.Context, ref string) error {
	if !validFilesystemRef(ref) {
		return ErrInvalidRef
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := f.root()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(ref); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
