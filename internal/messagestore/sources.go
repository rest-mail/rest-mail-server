package messagestore

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MaxHeaderMetadataBytes is the maximum size of the caller's serialized header metadata.
const MaxHeaderMetadataBytes = 64 << 10

const (
	// Leave time for an S3 PUT whose outcome was unknown to the uploader to
	// settle before a sweep can delete its key.
	stageUploadTimeout = 15 * time.Minute
)

var (
	ErrHeaderMetadataTooLarge = errors.New("message source header metadata is larger than the limit")
	ErrSourceUnavailable      = errors.New("message source is not in the required state")
	ErrSourceTransaction      = errors.New("message source references require an owner transaction")
	ErrCorruptSource          = errors.New("stored message source differs from its recorded size or digest")
)

// Sources stores immutable message bytes and tracks which objects have committed owners.
type Sources struct {
	db    *gorm.DB
	store sourceBackend
}

func NewSources(db *gorm.DB, cfg *config.Config) (*Sources, error) {
	backend, err := newBackend(cfg.Messages, cfg.Attachments)
	if err != nil {
		return nil, err
	}
	return &Sources{db: db, store: backend}, nil
}

// WithDB keeps the backend and binds metadata reads to the caller's connection.
func (s *Sources) WithDB(db *gorm.DB) *Sources {
	if s == nil {
		return nil
	}
	return &Sources{db: db, store: s.store}
}

// Stage reserves a key before it starts the upload. The caller supplies the size
// cap for the complete wire source and any already-extracted header metadata.
// The reaper owns cleanup even if saving the metadata or acknowledging the
// reservation fails. Stage must run before the caller opens its publish transaction.
func (s *Sources) Stage(ctx context.Context, source io.Reader, limit int64, headerMetadata []byte) (Object, error) {
	if limit < 0 {
		return Object{}, ErrTooLarge
	}
	if len(headerMetadata) > MaxHeaderMetadataBytes {
		return Object{}, ErrHeaderMetadataTooLarge
	}
	ctx, cancel := context.WithTimeout(ctx, stageUploadTimeout)
	defer cancel()
	db := s.db.WithContext(ctx)
	ref := s.store.newRef()
	err := db.Model(&models.MessageSource{}).Create(map[string]any{
		"ref": ref, "size": 0, "checksum": "", "reference_count": 0,
		"state": models.MessageSourceStaged, "header_metadata": headerMetadata,
		"reap_at": gorm.Expr("clock_timestamp() + interval '1 hour'"),
	}).Error
	if err != nil {
		return Object{}, err
	}
	var object Object
	err = db.Transaction(func(tx *gorm.DB) error {
		// Keep the stage locked across I/O. Even an expired stage can't be
		// reaped while its upload holds this lock.
		var row models.MessageSource
		err := unexpiredStage(tx, ref).Select("ref").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row).Error
		if err != nil {
			return err
		}
		object, err = s.store.putNamed(ctx, ref, source, limit)
		if err != nil {
			return err
		}
		return sourceUpdate(unexpiredStage(tx, ref).Updates(map[string]any{
			"size": object.Size, "checksum": object.Checksum,
		}))
	})
	if err != nil {
		return Object{}, err
	}
	return object, nil
}

// Publish gives a completed stage its initial owners. Insert their rows in the
// same transaction. If Commit returns an ambiguous error, reconcile against the
// committed owner or its idempotency receipt. A failed or rolled back commit
// leaves the stage eligible for reaping when its lease ends.
func (s *Sources) Publish(ctx context.Context, tx *gorm.DB, ref string, references int64) error {
	if !isSourceTransaction(tx) {
		return ErrSourceTransaction
	}
	if references <= 0 {
		return ErrSourceUnavailable
	}
	tx = tx.Session(&gorm.Session{Context: ctx, NewDB: true})
	return sourceUpdate(unexpiredStage(tx, ref).Where("checksum <> ''").Updates(map[string]any{
		"state": models.MessageSourcePublished, "reference_count": references, "reap_at": nil,
	}))
}

// AddReference acquires one owner for a Sent or COPY/MOVE copy, a queue
// recipient, or an in-progress attempt. Record that owner in tx before commit
// so a retry can tell whether it already acquired its reference.
func (s *Sources) AddReference(ctx context.Context, tx *gorm.DB, ref string) error {
	if !isSourceTransaction(tx) {
		return ErrSourceTransaction
	}
	tx = tx.Session(&gorm.Session{Context: ctx, NewDB: true})
	return sourceUpdate(liveSource(tx, ref).UpdateColumn("reference_count", gorm.Expr("reference_count + 1")))
}

// ReleaseReference drops one owner. Remove its ownership in tx so a retry
// can tell whether it already released the reference. Once the last owner
// releases it, the source is permanently eligible for reaping.
func (s *Sources) ReleaseReference(ctx context.Context, tx *gorm.DB, ref string) error {
	return ReleaseReference(ctx, tx, ref)
}

// ReleaseReference drops an owner from a transaction that needs no access to
// the storage backend.
func ReleaseReference(ctx context.Context, tx *gorm.DB, ref string) error {
	if !isSourceTransaction(tx) {
		return ErrSourceTransaction
	}
	tx = tx.Session(&gorm.Session{Context: ctx, NewDB: true})
	return sourceUpdate(liveSource(tx, ref).UpdateColumns(map[string]any{
		"reference_count": gorm.Expr("reference_count - 1"),
		"reap_at":         gorm.Expr("CASE WHEN reference_count = 1 THEN clock_timestamp() ELSE NULL END"),
	}))
}

func isSourceTransaction(tx *gorm.DB) bool {
	if tx == nil {
		return false
	}
	_, ok := tx.Statement.ConnPool.(gorm.TxCommitter)
	return ok
}

func unexpiredStage(db *gorm.DB, ref string) *gorm.DB {
	return db.Model(&models.MessageSource{}).
		Where("ref = ? AND state = ? AND reap_at > clock_timestamp()", ref, models.MessageSourceStaged)
}

func liveSource(db *gorm.DB, ref string) *gorm.DB {
	return db.Model(&models.MessageSource{}).
		Where("ref = ? AND state = ? AND reference_count > 0", ref, models.MessageSourcePublished)
}

func sourceUpdate(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSourceUnavailable
	}
	return nil
}

// Open reads a published source and verifies its size and SHA-256 by the end
// of a complete read. The caller must hold a committed reference throughout
// the read. Closing a partial read does not verify the digest.
func (s *Sources) Open(ctx context.Context, ref string) (io.ReadCloser, int64, error) {
	var row models.MessageSource
	err := liveSource(s.db.WithContext(ctx), ref).Select("size", "checksum").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, ErrSourceUnavailable
	}
	if err != nil {
		return nil, 0, err
	}
	body, err := s.store.Open(ctx, ref)
	if err != nil {
		return nil, 0, err
	}
	if body.Size() != row.Size {
		_ = body.Close()
		return nil, 0, ErrCorruptSource
	}
	return &verifiedSource{
		body: body, reader: contextReader{ctx, body}, hash: sha256.New(),
		left: row.Size, checksum: row.Checksum,
	}, row.Size, nil
}
