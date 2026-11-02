// Package messagestore holds the original bytes of email and uploads on disk or in an object
// store, so they never have to sit in memory (#1437). An email is untrusted text: it is streamed
// here as it arrives, and from here on whatever needs it reads what it needs by reference.
//
// Every object gets a new name of the store's own: the day and time it arrived, then 128 random
// bits. So objects are recorded in order, can't be guessed, and can be removed by age, one
// message's removal never touches another's, and nothing a sender writes reaches a path or a key.
package messagestore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

// ErrTooLarge says a source went past the size Put was allowed to take. Nothing is kept.
var ErrTooLarge = errors.New("message is larger than the limit")

// ErrNotFound says a reference names no object.
var ErrNotFound = errors.New("message object not found")

// ErrInvalidRef says a reference could not have come from this store.
var ErrInvalidRef = errors.New("invalid message object reference")

// Object is what a store holds for one message: where it is, and how to check it.
type Object struct {
	Ref      string
	Size     int64
	Checksum string // SHA-256, in hex
}

// Reader reads an object, from any position, without holding it in memory.
type Reader interface {
	io.ReadSeekCloser
	// Size is the object's length.
	Size() int64
}

// Store is where messages are kept. Both adapters behave alike; which one an installation uses
// is its choice.
type Store interface {
	// Put streams source into the store, taking at most limit bytes. An object appears only
	// once all of it has been written, and past limit it is ErrTooLarge and nothing is kept.
	Put(ctx context.Context, source io.Reader, limit int64) (Object, error)
	// Open reads an object. A missing one is ErrNotFound.
	Open(ctx context.Context, ref string) (Reader, error)
	// Delete removes an object. A missing one is not an error.
	Delete(ctx context.Context, ref string) error
}

// New is the store the configuration names: S3 where attachments are in S3, and the filesystem
// otherwise. Its objects sit beside attachments, never among them, because the attachment
// cleaner removes what no attachment refers to.
func New(c config.MessageStorageConfig, attachments config.AttachmentStorageConfig) (Store, error) {
	switch attachments.Type {
	case "s3":
		attachments.S3Prefix = c.S3Prefix
		return newS3(attachmentstore.NewS3(attachments), c.SpoolDir), nil
	case "filesystem":
		return NewFilesystem(c.Directory), nil
	}
	return nil, fmt.Errorf("unknown storage type %q", attachments.Type)
}

// objectName matches newName's names, and nothing else can reach a path or a key.
var objectName = regexp.MustCompile(`^[0-9]{4}/[0-9]{2}/[0-9]{2}/[0-9a-f]{12}-[A-Z2-7]{26}$`)

// newName is a name for an object arriving at now: in order of arrival, and unguessable.
func newName(now time.Time) string {
	now = now.UTC()
	return fmt.Sprintf("%s/%012x-%s", now.Format("2006/01/02"), now.UnixMilli(), rand.Text())
}

// limited reads source, failing with ErrTooLarge once it passes limit.
type limited struct {
	source io.Reader
	left   int64
}

func limitReader(source io.Reader, limit int64) *limited {
	return &limited{source: source, left: limit}
}

func (l *limited) Read(p []byte) (int, error) {
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.source.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return 0, ErrTooLarge
	}
	return n, err
}
