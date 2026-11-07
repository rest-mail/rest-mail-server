package attachmentstore

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
	"github.com/rest-mail/rest-mail-server/internal/tracing"
	"gorm.io/gorm"
)

// DefaultCleanupGrace is how old an unreferenced object must be before it goes.
// A delivery writes its object before its row, so a young object may be about to
// be referred to; a restored backup or a rolled-back transaction may also refer
// to one again.
const DefaultCleanupGrace = 7 * 24 * time.Hour

// cleanupBatch is how many references one query checks.
const cleanupBatch = 500

// errStillInUse says a delivery touched an object since the sweep listed it.
var errStillInUse = errors.New("attachment object was used during the sweep")

// CleanupResult says what one sweep did.
type CleanupResult struct {
	Removed int
	Bytes   int64
	Failed  int
}

// Cleaner removes stored attachment objects no attachment row refers to (#1388).
// Identical attachments share one S3 object, so a row's deletion can't remove its
// object: only finding no row at all can. It sweeps the S3 store and the filesystem
// tree both, because files stay behind after a migration to S3.
type Cleaner struct {
	// held answers which of the given references an attachment row holds.
	held     func(ctx context.Context, refs []string) ([]string, error)
	store    *S3
	root     string
	grace    time.Duration
	interval time.Duration
	now      func() time.Time

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// NewCleaner returns a cleaner for the given stores; store may be nil, and root may
// not exist on an install that never used it.
func NewCleaner(db *gorm.DB, store *S3, root string, grace, interval time.Duration) *Cleaner {
	held := func(ctx context.Context, refs []string) ([]string, error) {
		var held []string
		err := db.WithContext(ctx).Table("attachments").Where("storage_ref IN ?", refs).Distinct().Pluck("storage_ref", &held).Error
		return held, err
	}
	return &Cleaner{held: held, store: store, root: root, grace: grace, interval: interval, now: time.Now,
		stop: make(chan struct{}), done: make(chan struct{})}
}

// Start sweeps every interval until Stop.
func (c *Cleaner) Start() {
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-ticker.C:
				_ = tracing.Job(context.Background(), "job.attachment_cleanup", func(ctx context.Context) error {
					ctx, cancel := context.WithTimeout(ctx, c.interval)
					defer cancel()
					result, err := c.Sweep(ctx)
					if err != nil {
						slog.ErrorContext(ctx, "attachment cleanup failed", "error", err, "removed", result.Removed, "bytes", result.Bytes)
						return err
					}
					slog.InfoContext(ctx, "attachment cleanup finished", "removed", result.Removed, "bytes", result.Bytes, "failed", result.Failed)
					return nil
				})
			}
		}
	}()
}

// Stop ends the sweeps and waits for the current one.
func (c *Cleaner) Stop() {
	c.once.Do(func() { close(c.stop) })
	<-c.done
}

// Sweep removes every unreferenced object past the grace period. A failed lookup
// stops it: an object is only removed once a query has said nothing refers to it.
func (c *Cleaner) Sweep(ctx context.Context) (CleanupResult, error) {
	var result CleanupResult
	cutoff := c.now().Add(-c.grace)
	if c.store != nil {
		if err := c.sweepS3(ctx, cutoff, &result); err != nil {
			return result, err
		}
	}
	if err := c.sweepFilesystem(ctx, cutoff, &result); err != nil {
		return result, err
	}
	return result, nil
}

type candidate struct {
	ref  string
	size int64
}

func (c *Cleaner) sweepS3(ctx context.Context, cutoff time.Time, result *CleanupResult) error {
	pages := s3.NewListObjectsV2Paginator(c.store.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.store.bucket), Prefix: aws.String(c.store.prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		batch := make([]candidate, 0, len(page.Contents))
		for _, object := range page.Contents {
			ref := aws.ToString(object.Key)
			// Only names this store writes: anything else in the bucket isn't ours.
			if !c.store.validRef(ref) || object.LastModified == nil || !object.LastModified.Before(cutoff) {
				continue
			}
			batch = append(batch, candidate{ref, aws.ToInt64(object.Size)})
		}
		if err := c.removeUnreferenced(ctx, batch, result, func(item candidate) error {
			// Look again just before deleting: an identical delivery rewrites the object first.
			modified, err := c.store.lastModified(ctx, item.ref)
			if err != nil {
				return err
			}
			if !modified.Before(cutoff) {
				return errStillInUse
			}
			return c.store.Delete(ctx, item.ref)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cleaner) sweepFilesystem(ctx context.Context, cutoff time.Time, result *CleanupResult) error {
	if c.root == "" {
		return nil
	}
	var batch []candidate
	remove := func(item candidate) error {
		// Look again just before removing: a delivery reusing this file touches it first.
		info, err := os.Lstat(item.ref)
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			return errStillInUse
		}
		return os.Remove(item.ref)
	}
	err := filepath.WalkDir(c.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// A symlink could point out of the tree; only regular files are ours to remove.
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}
		batch = append(batch, candidate{path, info.Size()})
		if len(batch) < cleanupBatch {
			return nil
		}
		err = c.removeUnreferenced(ctx, batch, result, remove)
		batch = batch[:0]
		return err
	})
	if err != nil {
		return err
	}
	return c.removeUnreferenced(ctx, batch, result, remove)
}

// removeUnreferenced asks which of a batch's references rows still hold, then removes
// the rest. One object that won't go is counted, and the sweep moves on.
func (c *Cleaner) removeUnreferenced(ctx context.Context, batch []candidate, result *CleanupResult, remove func(candidate) error) error {
	if len(batch) == 0 {
		return nil
	}
	refs := make([]string, len(batch))
	for i, item := range batch {
		refs[i] = item.ref
	}
	held, err := c.held(ctx, refs)
	if err != nil {
		return err
	}
	referenced := make(map[string]bool, len(held))
	for _, ref := range held {
		referenced[ref] = true
	}
	for _, item := range batch {
		if referenced[item.ref] {
			continue
		}
		if err := remove(item); err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errStillInUse) {
				continue
			}
			slog.WarnContext(ctx, "attachment cleanup could not remove an object", "error", err)
			result.Failed++
			continue
		}
		result.Removed++
		result.Bytes += item.size
		metrics.AttachmentCleanupRemoved.Inc()
		metrics.AttachmentCleanupBytes.Add(float64(item.size))
	}
	return nil
}
