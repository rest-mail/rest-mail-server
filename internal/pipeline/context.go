package pipeline

import (
	"context"

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
