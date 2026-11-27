package messagestore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateMessage inserts a new owner whose reference was already acquired in
// tx, typically by Publish. The insert and reference must commit together.
func (s *Sources) CreateMessage(ctx context.Context, tx *gorm.DB, message *models.Message) error {
	if !isSourceTransaction(tx) {
		return ErrSourceTransaction
	}
	if message.SourceRef == nil {
		return ErrSourceUnavailable
	}
	tx = tx.Session(&gorm.Session{Context: ctx, NewDB: true})
	open := func() (io.ReadCloser, int64, error) {
		return s.open(ctx, tx, *message.SourceRef)
	}
	return message.CreateWithSource(tx, func(m *models.Message) error {
		reader, size, err := open()
		if err != nil {
			return err
		}
		defer reader.Close()
		counter := sourceCounter{}
		m.SealedHash, err = m.SealDigestFrom(io.TeeReader(reader, &counter))
		if err != nil {
			return err
		}
		m.RawSize = int(size)
		if int64(m.RawSize) != size {
			return ErrTooLarge
		}
		sizes := counter.wireSizes(m.RawSize)
		if counter.needsConversion {
			sizes, err = s.measureConverted(open)
			if err != nil {
				return err
			}
		}
		m.DowngradedSize, m.POP3UTF8Size = sizes.Downgraded, sizes.POP3UTF8
		m.IMAPSize, m.IMAPUTF8Size = sizes.IMAP, sizes.IMAPUTF8
		return nil
	})
}

// CopyMessage inserts a clone of a locked row with its original sizes and
// seal. The copy starts with the same source and sealed content as its owner.
func CopyMessage(ctx context.Context, tx *gorm.DB, message *models.Message) error {
	if message.SourceRef == nil {
		return ErrSourceUnavailable
	}
	if err := addReference(ctx, tx, *message.SourceRef); err != nil {
		return err
	}
	tx = tx.Session(&gorm.Session{Context: ctx, NewDB: true})
	return message.CreateWithSource(tx.Omit("Mailbox"), func(*models.Message) error { return nil })
}

// OpenMessage pins the message's ownership until the body closes. In
// particular, S3 starts its GET lazily, after Open has checked its length.
func (s *Sources) OpenMessage(ctx context.Context, id uint, ref string) (io.ReadCloser, int64, error) {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	var message models.Message
	err := tx.Select("id").Where("id = ? AND source_ref = ?", id, ref).
		Clauses(clause.Locking{Strength: "SHARE"}).Take(&message).Error
	if err != nil {
		_ = tx.Rollback().Error
		return nil, 0, err
	}
	reader, size, err := s.open(ctx, tx, ref)
	if err != nil {
		_ = tx.Rollback().Error
		return nil, 0, err
	}
	return &lockedSource{ReadCloser: reader, tx: tx}, size, nil
}

type lockedSource struct {
	io.ReadCloser
	tx *gorm.DB
}

func (s *lockedSource) Close() error {
	err := s.ReadCloser.Close()
	_ = s.tx.Rollback().Error
	return err
}

// MessageSourceHeaders loads the bounded RFC 5322 header section for a
// referenced message. Inline messages and deleted owners have no result.
func MessageSourceHeaders(db *gorm.DB, id uint) ([]byte, error) {
	var row models.MessageSource
	err := db.Select("message_sources.header_metadata").
		Joins("JOIN messages ON messages.source_ref = message_sources.ref").
		Where("messages.id = ?", id).Take(&row).Error
	return row.HeaderMetadata, err
}

type sourceCounter struct {
	bareLF          int
	last            byte
	needsConversion bool
}

func (c *sourceCounter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' && c.last != '\r' {
			c.bareLF++
		}
		c.needsConversion = c.needsConversion || b == 0 || b >= 0x80
		c.last = b
	}
	return len(p), nil
}

func (c *sourceCounter) wireSizes(size int) rmail.WireSizes {
	pop3 := size + c.bareLF
	if size != 0 && c.last != '\n' {
		pop3 += 2
	}
	return rmail.WireSizes{Downgraded: pop3, POP3UTF8: pop3, IMAP: size, IMAPUTF8: size}
}

// The IMAP converter needs random access to handle nested or ambiguous MIME
// boundaries. For sources that require conversion, spool the verified bytes
// and let the same sizing code walk a read-only mapping without a heap copy.
func (s *Sources) measureConverted(open func() (io.ReadCloser, int64, error)) (sizes rmail.WireSizes, err error) {
	reader, size, err := open()
	if err != nil {
		return sizes, err
	}
	defer reader.Close()
	if s.spoolDir != "" {
		if err := os.MkdirAll(s.spoolDir, 0o750); err != nil {
			return sizes, err
		}
	}
	file, err := os.CreateTemp(s.spoolDir, "restmail-measure-*")
	if err != nil {
		return sizes, err
	}
	defer file.Close()
	if err := os.Remove(file.Name()); err != nil {
		defer os.Remove(file.Name())
	}
	n, err := io.Copy(file, reader)
	if err != nil {
		return sizes, err
	}
	if n != size || int64(int(n)) != n || n == 0 {
		return sizes, fmt.Errorf("cannot measure message wire sizes: %w", ErrCorruptSource)
	}
	mapped, err := syscall.Mmap(int(file.Fd()), 0, int(n), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return sizes, err
	}
	defer func() { err = errors.Join(err, syscall.Munmap(mapped)) }()
	return rmail.MeasureWireSizes(unsafe.String(unsafe.SliceData(mapped), len(mapped))), nil
}
