package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/attachmentstore/s3test"
	"github.com/restmail/restmail/internal/db/models"
)

func TestS3AttachmentMigrationPreservesFilesAndResumes(t *testing.T) {
	db := scratchDatabase(t)
	if err := db.AutoMigrate(&models.Attachment{}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "existing.bin")
	data := []byte("existing private bytes\x00\xff")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	row := models.Attachment{MessageID: 1, StorageType: "filesystem", StorageRef: path, SizeBytes: int64(len(data))}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	fake := s3test.New(t)
	store := attachmentstore.NewS3(fake.Config)
	fake.Failure.Store(403)
	if _, err := attachmentstore.MigrateFilesystem(context.Background(), db, store, root); err == nil {
		t.Fatal("failed upload changed metadata")
	}
	var saved models.Attachment
	if err := db.First(&saved, row.ID).Error; err != nil || saved.StorageRef != path || saved.StorageType != "filesystem" {
		t.Fatalf("failed migration changed source: %+v %v", saved, err)
	}
	fake.Failure.Store(0)
	n, err := attachmentstore.MigrateFilesystem(context.Background(), db, store, root)
	if err != nil || n != 1 {
		t.Fatalf("migration: %d %v", n, err)
	}
	if err := db.First(&saved, row.ID).Error; err != nil || saved.StorageType != "s3" || saved.Checksum == "" || saved.StorageRef != "attachments/"+saved.Checksum {
		t.Fatalf("metadata: %+v %v", saved, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(data) {
		t.Fatalf("source file changed: %q %v", got, err)
	}
	if n, err := attachmentstore.MigrateFilesystem(context.Background(), db, store, root); err != nil || n != 0 {
		t.Fatalf("resume was not idempotent: %d %v", n, err)
	}
	// A bad checksum must prevent the metadata cutover, even if upload works.
	bad := models.Attachment{MessageID: 2, StorageType: "filesystem", StorageRef: path, Checksum: strings.Repeat("0", 64)}
	if err := db.Create(&bad).Error; err != nil {
		t.Fatal(err)
	}
	before := fake.Requests.Load()
	if _, err := attachmentstore.MigrateFilesystem(context.Background(), db, store, root); err == nil || fake.Requests.Load() != before {
		t.Fatal("corrupt source reached storage or was accepted")
	}
}

func TestS3AttachmentStorageOperatorProbe(t *testing.T) {
	fake := s3test.New(t)
	if err := verifyAttachmentStorage(context.Background(), attachmentstore.NewS3(fake.Config)); err != nil {
		t.Fatal(err)
	}
	fake.Failure.Store(403)
	if err := verifyAttachmentStorage(context.Background(), attachmentstore.NewS3(fake.Config)); err == nil {
		t.Fatal("denied storage probe passed")
	}
}
