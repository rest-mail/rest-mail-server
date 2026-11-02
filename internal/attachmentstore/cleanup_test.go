package attachmentstore_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func cleanupDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		testdb.Unreachable(t, "no database configuration: %v", err)
	}
	var ping *sql.DB
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{})
	if err == nil {
		ping, err = db.DB()
	}
	if err == nil {
		err = ping.Ping()
	}
	if err != nil {
		testdb.Unreachable(t, "no database: %v", err)
	}
	if err := db.AutoMigrate(&models.Attachment{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// A deleted message's attachment goes; one another row still refers to stays, and
// so does a young one a delivery may be about to refer to (#1388).
func TestCleanerRemovesOnlyWhatNothingRefersTo(t *testing.T) {
	db := cleanupDB(t)
	root := t.TempDir()
	old := time.Now().Add(-30 * 24 * time.Hour)
	file := func(name string, modified time.Time) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		return path
	}
	shared := file("2026/01/01/shared", old)
	orphan := file("imports/orphan.bin", old)
	young := file("imports/young.bin", time.Now())

	fake := s3test.New(t)
	store := attachmentstore.NewS3(fake.Config)
	ctx := context.Background()
	sharedRef, _, err := store.Put(ctx, []byte("shared object"))
	if err != nil {
		t.Fatal(err)
	}
	orphanRef, _, err := store.Put(ctx, []byte("orphan object"))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []models.Attachment{
		{MessageID: 1, StorageType: "filesystem", StorageRef: shared},
		{MessageID: 2, StorageType: "filesystem", StorageRef: shared},
		{MessageID: 3, StorageType: "s3", StorageRef: sharedRef},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	result, err := attachmentstore.NewCleaner(db, store, root, attachmentstore.DefaultCleanupGrace, time.Hour).Sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 2 || result.Failed != 0 || result.Bytes == 0 {
		t.Fatalf("result = %+v, want the two orphans removed", result)
	}
	for path, want := range map[string]bool{shared: true, young: true, orphan: false} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", path, err == nil, want)
		}
	}
	if _, err := store.Open(ctx, sharedRef); err != nil {
		t.Errorf("the shared object went: %v", err)
	}
	if _, err := store.Open(ctx, orphanRef); err == nil {
		t.Error("the orphan object stayed")
	}
}

// A failed lookup removes nothing.
func TestCleanerKeepsEverythingWhenTheLookupFails(t *testing.T) {
	db := cleanupDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "orphan")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := attachmentstore.NewCleaner(db, nil, root, time.Hour, time.Hour).Sweep(canceled); err == nil {
		t.Fatal("a cancelled sweep reported success")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file went: %v", err)
	}
}
