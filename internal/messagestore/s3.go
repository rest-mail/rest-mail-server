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

// Store keeps objects in a bucket. A signed upload has to state its length, so a message goes
// to a temporary file first, under spoolDir, and from there to the bucket.
type Store struct {
	bucket   *attachmentstore.S3
	spoolDir string
}

// spool streams source to a temporary file, hashing as it goes and holding it to limit. The
// answer is the file at its start, its size and checksum, and a function that removes it.
func (s *Store) spool(ctx context.Context, source io.Reader, limit int64) (*os.File, int64, string, func(), error) {
	if s.spoolDir != "" {
		if err := os.MkdirAll(s.spoolDir, 0o750); err != nil {
			return nil, 0, "", nil, err
		}
	}
	file, err := os.CreateTemp(s.spoolDir, "restmail-message-*")
	if err != nil {
		return nil, 0, "", nil, err
	}
	done := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), limitReader(contextReader{ctx, source}, limit))
	if err != nil {
		done()
		return nil, 0, "", nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		done()
		return nil, 0, "", nil, err
	}
	return file, size, hex.EncodeToString(hash.Sum(nil)), done, nil
}

// Put streams source into the bucket, taking at most limit bytes. An object appears only once
// all of it has arrived, and past limit it is ErrTooLarge and nothing is kept.
func (s *Store) Put(ctx context.Context, source io.Reader, limit int64) (Object, error) {
	file, size, checksum, done, err := s.spool(ctx, source, limit)
	if err != nil {
		return Object{}, err
	}
	defer done()
	ref := s.bucket.Prefix() + newName(time.Now())
	if err := s.bucket.PutNamed(ctx, ref, file, size); err != nil {
		return Object{}, err
	}
	return Object{Ref: ref, Size: size, Checksum: checksum}, nil
}

type s3Reader struct{ *attachmentstore.Reader }

func (r s3Reader) Size() int64 { return r.Reader.Size }

// Open reads an object, streaming it from the bucket. A missing one is ErrNotFound.
func (s *Store) Open(ctx context.Context, ref string) (Reader, error) {
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

// Delete removes an object. A missing one is not an error.
func (s *Store) Delete(ctx context.Context, ref string) error {
	if !s.validRef(ref) {
		return ErrInvalidRef
	}
	return s.bucket.DeleteNamed(ctx, ref)
}

func (s *Store) validRef(ref string) bool {
	name, inside := strings.CutPrefix(ref, s.bucket.Prefix())
	return inside && objectName.MatchString(name)
}
