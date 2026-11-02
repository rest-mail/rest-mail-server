package messagestore

import (
	"context"
	"errors"
	"io"
	"os"

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
	size, err := io.Copy(file, limitReader(contextReader{ctx, source}, limit))
	if err != nil {
		return Object{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Object{}, err
	}
	ref, checksum, err := s.bucket.PutReader(ctx, file, size)
	if err != nil {
		return Object{}, err
	}
	return Object{Ref: ref, Size: size, Checksum: checksum}, nil
}

type s3Reader struct{ *attachmentstore.Reader }

func (r s3Reader) Size() int64 { return r.Reader.Size }

// Open reads an object, streaming it from the bucket.
func (s *S3) Open(ctx context.Context, ref string) (Reader, error) {
	reader, err := s.bucket.Open(ctx, ref)
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
	return s.bucket.Delete(ctx, ref)
}
