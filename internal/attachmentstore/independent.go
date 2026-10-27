package attachmentstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	namespace, err := copyNamespace()
	if err != nil {
		return "", "", err
	}
	copy := *s
	copy.prefix += "copies/" + namespace + "/"
	return copy.Put(ctx, data)
}

// PutIndependentFilesystem gives a forwarded part its own confined directory.
func PutIndependentFilesystem(ctx context.Context, root string, data []byte) (string, string, error) {
	namespace, err := copyNamespace()
	if err != nil {
		return "", "", err
	}
	return putFilesystemObject(ctx, root, filepath.Join("copies", namespace), data)
}
