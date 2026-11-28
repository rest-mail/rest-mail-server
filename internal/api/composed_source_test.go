package api

import (
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
	"gorm.io/gorm"
)

type testSourceLedger struct {
	stage map[string]any
}

func (l *testSourceLedger) query(q string) (driver.Rows, bool) {
	if strings.Contains(q, `FROM "message_sources"`) {
		return importRows(l.stage), true
	}
	return nil, false
}

func (l *testSourceLedger) exec(q string, args []driver.NamedValue) bool {
	switch {
	case strings.HasPrefix(q, `INSERT INTO "message_sources"`):
		// The lease is a SQL expression, so ref is the second bound value.
		l.stage = map[string]any{"ref": args[1].Value}
	case strings.HasPrefix(q, `UPDATE "message_sources" SET "checksum"`):
		l.stage = map[string]any{
			"ref": l.stage["ref"], "checksum": args[0].Value,
			"header_metadata": args[1].Value, "size": args[2].Value,
		}
	case strings.HasPrefix(q, `UPDATE "message_sources" SET "reap_at"`):
	case strings.HasPrefix(q, `DELETE FROM "message_sources"`):
		l.stage = nil
	default:
		return false
	}
	return true
}

func setTestMessageStorage(t *testing.T, cfg *config.Config) {
	t.Helper()
	if cfg.Attachments.Type == "" {
		cfg.Attachments.Type = "filesystem"
	}
	if cfg.Attachments.Type == "filesystem" && cfg.Messages.Directory == "" {
		cfg.Messages.Directory = t.TempDir()
	}
	if cfg.Attachments.Type == "s3" && cfg.Messages.S3Prefix == "" {
		cfg.Messages.S3Prefix = "messages/"
	}
}

func testMessageSources(t *testing.T, db *gorm.DB, cfg *config.Config) *messagestore.Sources {
	t.Helper()
	sources, err := messagestore.NewSources(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return sources
}

func filesystemTestSources(t *testing.T, db *gorm.DB) *messagestore.Sources {
	t.Helper()
	var cfg config.Config
	setTestMessageStorage(t, &cfg)
	return testMessageSources(t, db, &cfg)
}

func readTestSource(t *testing.T, sources *messagestore.Sources, ref string) string {
	t.Helper()
	body, size, err := sources.Open(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(body)
	if err := errors.Join(readErr, body.Close()); err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || int64(len(raw)) != size {
		t.Fatalf("source read %d bytes, expected %d", len(raw), size)
	}
	return string(raw)
}

func (f *credentialFixture) sentSource(message models.Message) string {
	f.t.Helper()
	if message.SourceRef == nil {
		f.t.Fatal("Sent copy has no stored source reference")
	}
	return readTestSource(f.t, testMessageSources(f.t, f.db, f.cfg), *message.SourceRef)
}

func sameSourceRef(a, b *string) bool {
	return a != nil && b != nil && *a == *b
}
