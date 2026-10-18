package attachmentstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func filesystemRows(db *gorm.DB, root string) *gorm.DB {
	// Before S3 support, a pipeline could label a filesystem path as s3.
	// Migrate those paths too, under the same filesystem confinement checks.
	return db.Where("storage_type = ? OR (storage_type = ? AND storage_ref LIKE ?)", "filesystem", "s3", root+"/%")
}

// MigrateFilesystem copies and verifies each object before changing its row.
// It keeps original files intact and is safe to resume after a partial failure.
func MigrateFilesystem(ctx context.Context, db *gorm.DB, store *S3, root string) (int, error) {
	if store == nil {
		return 0, fmt.Errorf("S3 attachment storage must be configured before migration")
	}
	db = db.WithContext(ctx)
	var last uint
	migrated := 0
	for {
		var rows []models.Attachment
		if err := filesystemRows(db, root).Where("id > ?", last).Order("id").Limit(100).Find(&rows).Error; err != nil {
			return migrated, err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			last = row.ID
			ref, sum, err := copyFilesystemObject(ctx, store, root, row)
			if err != nil {
				return migrated, fmt.Errorf("attachment %d: %w", row.ID, err)
			}
			result := db.Model(&models.Attachment{}).Where("id = ? AND storage_type = ? AND storage_ref = ?", row.ID, row.StorageType, row.StorageRef).
				Updates(map[string]any{"storage_type": "s3", "storage_ref": ref, "checksum": sum})
			if result.Error != nil {
				return migrated, result.Error
			}
			migrated += int(result.RowsAffected)
		}
	}
	var remaining int64
	if err := filesystemRows(db.Model(&models.Attachment{}), root).Count(&remaining).Error; err != nil {
		return migrated, err
	}
	if remaining != 0 {
		return migrated, fmt.Errorf("filesystem attachments remain; resume migration before removing the volume")
	}
	return migrated, nil
}

func copyFilesystemObject(ctx context.Context, store *S3, root string, row models.Attachment) (string, string, error) {
	path, err := ResolveFilesystemPath(root, row.StorageRef)
	if err != nil {
		return "", "", fmt.Errorf("source attachment is missing or outside the attachment directory")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("cannot open source attachment")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("source attachment is not a regular file")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", "", fmt.Errorf("cannot checksum source attachment")
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	if row.Checksum != "" && row.Checksum != sum {
		return "", "", fmt.Errorf("source attachment checksum does not match its metadata")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	ref, uploadedSum, err := store.PutReader(ctx, file, info.Size())
	if err != nil {
		return "", "", fmt.Errorf("object upload failed: %w", err)
	}
	if uploadedSum != sum {
		return "", "", fmt.Errorf("source attachment changed during upload")
	}
	object, err := store.Open(ctx, ref)
	if err != nil {
		return "", "", fmt.Errorf("cannot verify uploaded attachment: %w", err)
	}
	defer func() { _ = object.Close() }()
	hash.Reset()
	n, err := io.Copy(hash, object)
	if err != nil || n != info.Size() || hex.EncodeToString(hash.Sum(nil)) != sum {
		return "", "", fmt.Errorf("uploaded attachment failed read-back checksum verification")
	}
	return ref, sum, nil
}
