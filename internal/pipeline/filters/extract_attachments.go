package filters

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type extractAttConfig struct {
	StorageType string `json:"storage_type"` // "filesystem" or "s3"
	StoragePath string `json:"storage_path"` // base path for filesystem storage
}

type extractAttFilter struct {
	cfg extractAttConfig
	s3  *attachmentstore.S3
}

func init() {
	pipeline.DefaultRegistry.Register("extract_attachments", NewExtractAttachments)
}

func NewExtractAttachments(config []byte) (pipeline.Filter, error) {
	return newExtractAttachments(config, nil)
}

// NewExtractAttachmentsWithS3 binds storage to trusted server configuration.
// A pipeline cannot choose an endpoint, bucket or credentials of its own.
func NewExtractAttachmentsWithS3(store *attachmentstore.S3) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return newExtractAttachments(config, store)
	}
}

func newExtractAttachments(config []byte, store *attachmentstore.S3) (pipeline.Filter, error) {
	cfg := extractAttConfig{
		StorageType: "filesystem",
		StoragePath: "/attachments",
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("invalid attachment filter configuration: %w", err)
		}
	}
	if store != nil {
		cfg.StorageType = "s3"
	} else if cfg.StorageType != "filesystem" {
		return nil, fmt.Errorf("S3 attachment storage must be configured on the server")
	}
	return &extractAttFilter{cfg: cfg, s3: store}, nil
}

func (f *extractAttFilter) Name() string              { return "extract_attachments" }
func (f *extractAttFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *extractAttFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email
	extracted := 0
	db := pipeline.DBFromContext(ctx)

	// Process attachments
	newAttachments := make([]pipeline.Attachment, len(email.Attachments))
	for i, att := range email.Attachments {
		if att.Content != "" && att.Storage == "" {
			ref, checksum, err := f.store(ctx, att, db)
			if err != nil {
				return nil, fmt.Errorf("store attachment: %w", err)
			}
			newAttachments[i] = pipeline.Attachment{
				Filename:    att.Filename,
				ContentType: att.ContentType,
				Size:        att.Size,
				Disposition: att.Disposition,
				Storage:     f.cfg.StorageType,
				Ref:         ref,
				Checksum:    checksum,
			}
			extracted++
		} else {
			newAttachments[i] = att
		}
	}
	modified.Attachments = newAttachments

	// Process inline images
	newInline := make([]pipeline.Attachment, len(email.Inline))
	for i, att := range email.Inline {
		if att.Content != "" && att.Storage == "" {
			ref, checksum, err := f.store(ctx, att, db)
			if err != nil {
				return nil, fmt.Errorf("store inline attachment: %w", err)
			}
			newInline[i] = pipeline.Attachment{
				Filename:    att.Filename,
				ContentType: att.ContentType,
				Size:        att.Size,
				Disposition: att.Disposition,
				ContentID:   att.ContentID,
				Storage:     f.cfg.StorageType,
				Ref:         ref,
				Checksum:    checksum,
			}
			extracted++
		} else {
			newInline[i] = att
		}
	}
	modified.Inline = newInline

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "extract_attachments",
			Result: "transformed",
			Detail: fmt.Sprintf("%d attachments extracted to %s", extracted, f.cfg.StorageType),
		},
	}, nil
}

func (f *extractAttFilter) store(ctx context.Context, att pipeline.Attachment, db *gorm.DB) (string, string, error) {
	data, err := base64.StdEncoding.DecodeString(att.Content)
	if err != nil {
		return "", "", fmt.Errorf("decode base64: %w", err)
	}
	if f.s3 != nil {
		return f.s3.Put(ctx, data)
	}

	// Compute checksum
	hash := sha256.Sum256(data)
	checksum := hex.EncodeToString(hash[:])

	// Check for existing attachment with the same checksum (dedup)
	if db != nil {
		var existing models.Attachment
		if err := db.Where("checksum = ? AND storage_type = ?", checksum, f.cfg.StorageType).
			First(&existing).Error; err == nil {
			return existing.StorageRef, checksum, nil
		}
	}

	// Generate storage path
	now := time.Now()
	dir := filepath.Join(f.cfg.StoragePath,
		fmt.Sprintf("%d/%02d/%02d", now.Year(), now.Month(), now.Day()))

	filename := checksum[:12]
	if att.Filename != "" {
		filename = checksum[:12] + "-" + sanitizeFilename(att.Filename)
	}
	ref := filepath.Join(dir, filename)

	// Write to filesystem
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("mkdir: %w", err)
	}

	if err := os.WriteFile(ref, data, 0644); err != nil {
		return "", "", fmt.Errorf("write: %w", err)
	}

	return ref, checksum, nil
}

func sanitizeFilename(name string) string {
	// Remove path separators and null bytes
	result := make([]byte, 0, len(name))
	for _, b := range []byte(name) {
		if b == '/' || b == '\\' || b == 0 {
			continue
		}
		result = append(result, b)
	}
	if len(result) > 100 {
		result = result[:100]
	}
	return string(result)
}
