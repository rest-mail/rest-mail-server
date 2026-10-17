package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/config"
)

func migrateAttachments(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return fail(stderr, exitUsage, "bad_flag", "migrate-attachments takes no arguments")
	}
	database, cfg, status := openDatabase(stderr)
	if status != exitOK {
		return status
	}
	defer closeDatabase(database)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	n, err := attachmentstore.MigrateFilesystem(ctx, database, attachmentstore.NewS3(cfg.Attachments), attachmentstore.FilesystemRoot)
	if err != nil {
		return fail(stderr, exitFailed, "attachment_migration", err.Error())
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"migrated": n, "filesystem_remaining": 0, "source_files_retained": true})
	return exitOK
}

func checkAttachmentStorage(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		return fail(stderr, exitUsage, "bad_flag", "check-attachment-storage takes no arguments")
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(stderr, exitFailed, "config", err.Error())
	}
	store := attachmentstore.NewS3(cfg.Attachments)
	if store == nil {
		return fail(stderr, exitUsage, "bad_config", "S3 attachment storage is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := verifyAttachmentStorage(ctx, store); err != nil {
		return fail(stderr, exitFailed, "attachment_storage", err.Error())
	}
	_ = json.NewEncoder(stdout).Encode(map[string]any{"backend": "s3", "put": true, "get": true, "range": true, "delete": true})
	return exitOK
}

func verifyAttachmentStorage(ctx context.Context, store *attachmentstore.S3) error {
	probe := make([]byte, 64)
	if _, err := rand.Read(probe); err != nil {
		return err
	}
	ref, _, err := store.Put(ctx, probe)
	if err != nil {
		return fmt.Errorf("attachment storage write failed: %w", err)
	}
	// The random probe cannot collide with a mail object. Clean it up even if
	// verification fails, without deleting any real attachment.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = store.Delete(cleanup, ref)
	}()
	file, err := store.Open(ctx, ref)
	if err != nil {
		return fmt.Errorf("attachment storage read failed: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(data, probe) {
		return fmt.Errorf("attachment storage round trip failed")
	}
	if _, err := file.Seek(13, io.SeekStart); err != nil {
		return err
	}
	data, err = io.ReadAll(file)
	if err != nil || !bytes.Equal(data, probe[13:]) {
		return fmt.Errorf("attachment storage range read failed")
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := store.Delete(ctx, ref); err != nil {
		return fmt.Errorf("attachment storage probe cleanup failed: %w", err)
	}
	return nil
}
