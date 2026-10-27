package attachmentstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// PutFilesystem stores imported bytes under a content-addressed name. Filenames
// supplied by a message never enter the path. os.Root confines every operation,
// including existing symlinks, and rename publishes only a completely written file.
func PutFilesystem(ctx context.Context, directory string, data []byte) (ref, checksum string, err error) {
	return putFilesystemObject(ctx, directory, "imports", data)
}

func putFilesystemObject(ctx context.Context, directory, subdir string, data []byte) (ref, checksum string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", "", err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(subdir, 0755); err != nil {
		return "", "", err
	}
	checksum = fmt.Sprintf("%x", sha256.Sum256(data))
	object := filepath.Join(subdir, checksum+".bin")
	temporary := filepath.Join(subdir, ".tmp-"+rand.Text())
	f, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = root.Remove(temporary) }() // only the unpublished temporary file created here
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return "", "", writeErr
	}
	if closeErr != nil {
		return "", "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if err := root.Rename(temporary, object); err != nil {
		return "", "", err
	}
	return filepath.Join(directory, object), checksum, nil
}
