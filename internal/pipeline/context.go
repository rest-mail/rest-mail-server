package pipeline

import (
	"context"
	"io"

	"gorm.io/gorm"
)

type contextKey string

const dbContextKey contextKey = "pipeline_db"

// WithDB returns a new context with the given database handle attached.
func WithDB(ctx context.Context, db *gorm.DB) context.Context {
	return context.WithValue(ctx, dbContextKey, db)
}

// DBFromContext extracts the database handle from a pipeline context.
func DBFromContext(ctx context.Context) *gorm.DB {
	db, _ := ctx.Value(dbContextKey).(*gorm.DB)
	return db
}

// AttachmentOpener opens the bytes of a part carried by reference, a stored object its caller
// handed to the pipeline, rather than as content.
type AttachmentOpener interface {
	// Holds says whether att is a part the caller handed in, which Open opens.
	Holds(att Attachment) bool
	// Open reads exactly the part's Size octets, and fails rather than ends on any others.
	Open(ctx context.Context, att Attachment) (io.ReadCloser, error)
}

const attachmentOpenerKey contextKey = "pipeline_attachment_opener"

// WithAttachmentOpener lets the filters that send a message's bytes elsewhere read the parts their
// caller handed to the pipeline by reference. Those parts are read as they are sent, and are never
// held whole in between (#2107).
func WithAttachmentOpener(ctx context.Context, open AttachmentOpener) context.Context {
	return context.WithValue(ctx, attachmentOpenerKey, open)
}

// AttachmentOpenerFromContext is the context's opener, or nil.
func AttachmentOpenerFromContext(ctx context.Context) AttachmentOpener {
	open, _ := ctx.Value(attachmentOpenerKey).(AttachmentOpener)
	return open
}
