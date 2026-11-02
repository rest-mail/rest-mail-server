package messagestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
)

// S3 keeps objects in a bucket. A signed upload has to state its length, so a message goes to a
// temporary file first, under spoolDir, and from there to the bucket.
type S3 struct {
	bucket   *attachmentstore.S3
	spoolDir string
}

func newS3(bucket *attachmentstore.S3, spoolDir string) *S3 {
	return &S3{bucket: bucket, spoolDir: spoolDir}
}

// Put streams source to a temporary file and uploads it from there.
func (s *S3) Put(ctx context.Context, source io.Reader, limit int64) (Object, error) {
	if s.spoolDir != "" {
		if err := os.MkdirAll(s.spoolDir, 0o750); err != nil {
			return Object{}, err
		}
	}
	file, err := os.CreateTemp(s.spoolDir, "restmail-message-*")
	if err != nil {
		return Object{}, err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), limitReader(contextReader{ctx, source}, limit))
	if err != nil {
		return Object{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Object{}, err
	}
	ref := s.bucket.Prefix() + newName(time.Now())
	if err := s.bucket.PutNamed(ctx, ref, file, size); err != nil {
		return Object{}, err
	}
	return Object{Ref: ref, Size: size, Checksum: hex.EncodeToString(hash.Sum(nil))}, nil
}

type s3Reader struct{ *attachmentstore.Reader }

func (r s3Reader) Size() int64 { return r.Reader.Size }

// Open reads an object, streaming it from the bucket.
func (s *S3) Open(ctx context.Context, ref string) (Reader, error) {
	if !s.validRef(ref) {
		return nil, ErrInvalidRef
	}
	reader, err := s.bucket.OpenNamed(ctx, ref)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s3Reader{reader}, nil
}

// Delete removes an object.
func (s *S3) Delete(ctx context.Context, ref string) error {
	if !s.validRef(ref) {
		return ErrInvalidRef
	}
	return s.bucket.DeleteNamed(ctx, ref)
}

func (s *S3) validRef(ref string) bool {
	name, inside := strings.CutPrefix(ref, s.bucket.Prefix())
	return inside && objectName.MatchString(name)
}
