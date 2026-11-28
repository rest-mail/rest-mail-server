package attachmentstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"path/filepath"
)

func copyNamespace() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

// PutIndependent stores a copy under its own object identity. Unlike Put, it
// never shares a deletion lifetime with another message containing equal bytes.
func (s *S3) PutIndependent(ctx context.Context, data []byte) (string, string, error) {
	return s.PutIndependentReader(ctx, bytes.NewReader(data), int64(len(data)))
}

// PutIndependentReader is PutIndependent for bytes read as they are stored, none of them held
// in memory. The source is read twice, for its checksum and then for the upload, and must read
// the same both times.
func (s *S3) PutIndependentReader(ctx context.Context, source io.ReadSeeker, size int64) (string, string, error) {
	namespace, err := copyNamespace()
	if err != nil {
		return "", "", err
	}
	copy := *s
	copy.prefix += "copies/" + namespace + "/"
	return copy.PutReader(ctx, source, size)
}

// PutIndependentFilesystem gives a forwarded part its own confined directory.
func PutIndependentFilesystem(ctx context.Context, root string, data []byte) (string, string, error) {
	ref, checksum, _, err := PutIndependentFilesystemReader(ctx, root, bytes.NewReader(data))
	return ref, checksum, err
}

// PutIndependentFilesystemReader is PutIndependentFilesystem for bytes read as they are
// stored, none of them held in memory, so it also answers how many there were.
func PutIndependentFilesystemReader(ctx context.Context, root string, source io.Reader) (ref, checksum string, size int64, err error) {
	namespace, err := copyNamespace()
	if err != nil {
		return "", "", 0, err
	}
	return putFilesystemObject(ctx, root, filepath.Join("copies", namespace), source)
}
