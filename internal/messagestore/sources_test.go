package messagestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

type sourceOwner struct {
	Name string `gorm:"primaryKey"`
	Ref  string
}

func TestMain(m *testing.M) {
	os.Exit(testdb.Finish(m.Run()))
}

func sourceDB(t *testing.T) *gorm.DB {
	t.Helper()
	env := func(name, fallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		return fallback
	}
	port, err := strconv.Atoi(env("DB_PORT", "5432"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		DBHost: env("DB_HOST", "localhost"), DBPort: port, DBName: env("DB_NAME", "restmail"),
		DBUser: env("DB_USER", "restmail"), DBPass: env("DB_PASS", "restmail"),
	}
	return testdb.Clone(t, &cfg, "source", testdb.Setup{Name: "server-schema", Run: rmdb.AutoMigrate}, testdb.Models(&sourceOwner{}))
}

func stageSource(t *testing.T, sources *Sources) Object {
	t.Helper()
	object, err := sources.Stage(t.Context(), strings.NewReader("original"), 8, []byte("Subject: test"))
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func publishSource(t *testing.T, sources *Sources, ref string, owners int64) {
	t.Helper()
	if err := sources.db.Transaction(func(tx *gorm.DB) error {
		return sources.Publish(t.Context(), tx, ref, owners)
	}); err != nil {
		t.Fatal(err)
	}
}

func sourceRow(t *testing.T, sources *Sources, ref string) models.MessageSource {
	t.Helper()
	var row models.MessageSource
	if err := sources.db.Take(&row, "ref = ?", ref).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func expireSource(t *testing.T, sources *Sources, ref string) {
	t.Helper()
	result := sources.db.Model(&models.MessageSource{}).Where("ref = ?", ref).
		UpdateColumn("reap_at", gorm.Expr("clock_timestamp() - interval '1 second'"))
	if err := sourceUpdate(result); err != nil {
		t.Fatal(err)
	}
}

func assertStored(t *testing.T, sources *Sources, ref string, wantPresent bool) {
	t.Helper()
	reader, err := sources.store.Open(t.Context(), ref)
	if err == nil {
		_ = reader.Close()
	}
	if wantPresent && err != nil || !wantPresent && !errors.Is(err, ErrNotFound) {
		t.Fatalf("object %s, want present=%v, got %v", ref, wantPresent, err)
	}
}

func TestSourceStagePublishAndRead(t *testing.T) {
	adapters(t, func(t *testing.T, store Store) {
		sources := &Sources{db: sourceDB(t), store: store.(sourceBackend)}
		ctx := t.Context()
		object := stageSource(t, sources)
		row := sourceRow(t, sources, object.Ref)
		if row.Size != 8 || row.Checksum != object.Checksum || string(row.HeaderMetadata) != "Subject: test" ||
			row.State != models.MessageSourceStaged || row.ReferenceCount != 0 || row.ReapAt == nil {
			t.Fatalf("staged row = %+v", row)
		}
		if _, _, err := sources.Open(ctx, object.Ref); !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("opened staged source: %v", err)
		}
		if err := sources.Publish(ctx, sources.db, object.Ref, 1); !errors.Is(err, ErrSourceTransaction) {
			t.Fatalf("publish used an autocommit connection: %v", err)
		}
		publishSource(t, sources, object.Ref, 3)
		row = sourceRow(t, sources, object.Ref)
		if row.State != models.MessageSourcePublished || row.ReferenceCount != 3 || row.ReapAt != nil {
			t.Fatalf("published row = %+v", row)
		}
		reader, size, err := sources.Open(ctx, object.Ref)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil || size != 8 || string(data) != "original" {
			t.Fatalf("read = %q, size %d, err %v", data, size, err)
		}

		if _, err := sources.store.putNamed(ctx, object.Ref, strings.NewReader("short"), 8); err != nil {
			t.Fatal(err)
		}
		if reader, _, err := sources.Open(ctx, object.Ref); !errors.Is(err, ErrCorruptSource) {
			if reader != nil {
				_ = reader.Close()
			}
			t.Fatalf("open with a different object size: %v", err)
		}

		// Simulate corrupt storage returning the same byte length.
		if _, err := sources.store.putNamed(ctx, object.Ref, strings.NewReader("tampered"), 8); err != nil {
			t.Fatal(err)
		}
		reader, _, err = sources.Open(ctx, object.Ref)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		if _, err := io.ReadFull(reader, make([]byte, size)); !errors.Is(err, ErrCorruptSource) {
			t.Fatalf("exact-size read of corrupt source: %v", err)
		}
	})
}

func TestSourceStageLimitsAndAllocation(t *testing.T) {
	directory := t.TempDir()
	sources := &Sources{db: sourceDB(t), store: NewFilesystem(directory)}
	ctx := t.Context()
	if _, err := sources.Stage(ctx, strings.NewReader(""), 0, make([]byte, MaxHeaderMetadataBytes+1)); !errors.Is(err, ErrHeaderMetadataTooLarge) {
		t.Fatalf("large header metadata: %v", err)
	}
	if _, err := sources.Stage(ctx, strings.NewReader("abc"), 2, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large source: %v", err)
	}
	var partial models.MessageSource
	if err := sources.db.Take(&partial).Error; err != nil {
		t.Fatal(err)
	}
	assertStored(t, sources, partial.Ref, false)
	if err := sources.db.Transaction(func(tx *gorm.DB) error {
		return sources.Publish(ctx, tx, partial.Ref, 1)
	}); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("publish of partial upload: %v", err)
	}

	const size = 32 << 20
	used := allocated(func() {
		object, err := sources.Stage(ctx, &endless{left: size}, size, nil)
		if err != nil || object.Size != size {
			t.Fatalf("large stage = %+v, %v", object, err)
		}
	})
	t.Logf("staging %d bytes allocated %d bytes", size, used)
	if used > 4<<20 {
		t.Fatalf("staging %d bytes allocated %d bytes", size, used)
	}

	// Simulate a process dying with a reserved name before the rename.
	temporary := filepath.Join(directory, temporaryName(partial.Ref))
	if err := os.WriteFile(temporary, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	expireSource(t, sources, partial.Ref)
	if removed, err := sources.Reap(ctx); err != nil || removed != 1 {
		t.Fatalf("reap = %d, %v", removed, err)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file remained: %v", err)
	}
}

// lostCommitReply simulates losing the connection around COMMIT. The caller
// receives the same error whether or not the server committed.
type lostCommitReply struct {
	gorm.Tx
	committed bool
}

func (c *lostCommitReply) Commit() error {
	finish := c.Tx.Rollback
	if c.committed {
		finish = c.Tx.Commit
	}
	if err := finish(); err != nil {
		return err
	}
	return driver.ErrBadConn
}

func TestSourcePublishAtomicity(t *testing.T) {
	adapters(t, func(t *testing.T, store Store) {
		sources := &Sources{db: sourceDB(t), store: store.(sourceBackend)}
		for _, scenario := range []struct {
			name      string
			ambiguous bool
			committed bool
		}{
			{name: "explicit rollback"},
			{name: "lost commit reply, rolled back", ambiguous: true},
			{name: "lost commit reply, committed", ambiguous: true, committed: true},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				object := stageSource(t, sources)
				errRollback := errors.New("caller failed before committing")
				wantErr := errRollback
				if scenario.ambiguous {
					wantErr = driver.ErrBadConn
				}
				err := sources.db.Transaction(func(tx *gorm.DB) error {
					if err := sources.Publish(t.Context(), tx, object.Ref, 1); err != nil {
						return err
					}
					if err := tx.Create(&sourceOwner{Name: scenario.name, Ref: object.Ref}).Error; err != nil {
						return err
					}
					if !scenario.ambiguous {
						return errRollback
					}
					tx.Statement.ConnPool = &lostCommitReply{Tx: tx.Statement.ConnPool.(gorm.Tx), committed: scenario.committed}
					return nil
				})
				if !errors.Is(err, wantErr) {
					t.Fatalf("publish transaction = %v, want %v", err, wantErr)
				}
				// Resolve the outcome from a fresh connection and the owner
				// record written in the caller's transaction.
				var owner sourceOwner
				err = sources.db.Take(&owner, "name = ?", scenario.name).Error
				row := sourceRow(t, sources, object.Ref)
				if scenario.committed {
					if err != nil || owner.Ref != object.Ref || row.ReferenceCount != 1 || row.ReapAt != nil {
						t.Fatalf("committed owner = %+v, source = %+v, error = %v", owner, row, err)
					}
				} else {
					if !errors.Is(err, gorm.ErrRecordNotFound) || row.State != models.MessageSourceStaged || row.ReferenceCount != 0 || row.ReapAt == nil {
						t.Fatalf("rolled-back owner = %+v, source = %+v, error = %v", owner, row, err)
					}
					expireSource(t, sources, object.Ref)
				}
				if _, err := sources.Reap(t.Context()); err != nil {
					t.Fatal(err)
				}
				assertStored(t, sources, object.Ref, scenario.committed)
			})
		}
	})
}

func TestSourceReferencesConcurrent(t *testing.T) {
	sources := &Sources{db: sourceDB(t), store: NewFilesystem(t.TempDir())}
	ref := stageSource(t, sources).Ref
	publishSource(t, sources, ref, 1)
	const recipients = 24
	for _, phase := range []struct {
		change func(*gorm.DB) error
		want   int64
	}{
		{func(tx *gorm.DB) error { return sources.AddReference(t.Context(), tx, ref) }, recipients + 1},
		{func(tx *gorm.DB) error { return sources.ReleaseReference(t.Context(), tx, ref) }, 1},
	} {
		var wg sync.WaitGroup
		for range recipients {
			wg.Go(func() {
				if err := sources.db.Transaction(phase.change); err != nil {
					t.Errorf("change source reference: %v", err)
				}
			})
		}
		wg.Wait()
		if row := sourceRow(t, sources, ref); row.ReferenceCount != phase.want || row.ReapAt != nil {
			t.Fatalf("source after concurrent changes = %+v, want %d refs", row, phase.want)
		}
	}
}

type observedDeletion struct {
	sourceBackend
	beforeDelete func(string)
}

func (s observedDeletion) Delete(ctx context.Context, ref string) error {
	s.beforeDelete(ref)
	return s.sourceBackend.Delete(ctx, ref)
}

func TestSourceReaperRacesLastRelease(t *testing.T) {
	adapters(t, func(t *testing.T, store Store) {
		sources := &Sources{db: sourceDB(t), store: store.(sourceBackend)}
		sources.store = observedDeletion{sources.store, func(ref string) {
			// This query uses another connection if the deletion claim is
			// still in a transaction, and sees only committed state.
			row := sourceRow(t, sources, ref)
			if row.State != models.MessageSourceDeleting || row.ReferenceCount != 0 {
				t.Fatalf("object deletion started before retiring the source: %+v", row)
			}
		}}
		ctx := t.Context()
		ref := stageSource(t, sources).Ref
		publishSource(t, sources, ref, 1)
		live := stageSource(t, sources).Ref
		publishSource(t, sources, live, 1)
		fresh := stageSource(t, sources).Ref
		expired := stageSource(t, sources).Ref
		expireSource(t, sources, expired)
		tx := sources.db.Begin()
		defer tx.Rollback()
		if err := sources.ReleaseReference(ctx, tx, ref); err != nil {
			t.Fatal(err)
		}
		if removed, err := sources.Reap(ctx); err != nil || removed != 1 {
			t.Fatalf("reap during last release = %d, %v", removed, err)
		}
		assertStored(t, sources, ref, true)
		assertStored(t, sources, fresh, true)
		assertStored(t, sources, expired, false)
		if err := tx.Commit().Error; err != nil {
			t.Fatal(err)
		}
		for _, change := range []func(*gorm.DB) error{
			func(tx *gorm.DB) error { return sources.AddReference(ctx, tx, ref) },
			func(tx *gorm.DB) error { return sources.ReleaseReference(ctx, tx, ref) },
			func(tx *gorm.DB) error { return sources.Publish(ctx, tx, expired, 1) },
		} {
			if err := sources.db.Transaction(change); !errors.Is(err, ErrSourceUnavailable) {
				t.Fatalf("modified a dead source: %v", err)
			}
		}
		if removed, err := sources.Reap(ctx); err != nil || removed != 1 {
			t.Fatalf("reap after last release = %d, %v", removed, err)
		}
		assertStored(t, sources, ref, false)
		assertStored(t, sources, fresh, true)
		assertStored(t, sources, live, true)
	})
}

func TestSourceReaperRetriesS3Deletion(t *testing.T) {
	fake := s3test.New(t)
	cfg := config.Config{
		Attachments: fake.Config, Messages: config.MessageStorageConfig{S3Prefix: "messages/", SpoolDir: t.TempDir()},
	}
	sources, err := NewSources(sourceDB(t), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	ref := stageSource(t, sources).Ref
	expireSource(t, sources, ref)
	fake.Failure.Store(http.StatusForbidden)
	if removed, err := sources.Reap(t.Context()); removed != 0 || err == nil {
		t.Fatalf("reap with failed S3 delete = %d, %v", removed, err)
	}
	fake.Failure.Store(0)
	row := sourceRow(t, sources, ref)
	if row.State != models.MessageSourceDeleting || row.ReapAt == nil || !row.ReapAt.After(time.Now()) {
		t.Fatalf("source is missing its deletion retry: %+v", row)
	}
	assertStored(t, sources, ref, true)
	if err := sources.db.Transaction(func(tx *gorm.DB) error {
		return sources.Publish(t.Context(), tx, ref, 1)
	}); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("publish could revive a stage pending a deletion retry: %v", err)
	}
	expireSource(t, sources, ref)
	if removed, err := sources.Reap(t.Context()); removed != 1 || err != nil {
		t.Fatalf("reap after S3 recovered = %d, %v", removed, err)
	}
	assertStored(t, sources, ref, false)
}

func TestVerifiedSourceChecksExactReads(t *testing.T) {
	const digest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	for _, test := range []struct {
		name    string
		payload string
		err     error
	}{
		{name: "valid", payload: "abc"},
		{name: "corrupt", payload: "abd", err: ErrCorruptSource},
		{name: "truncated", payload: "ab", err: ErrCorruptSource},
		{name: "too long", payload: "abcd", err: ErrCorruptSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &verifiedSource{
				reader: strings.NewReader(test.payload), hash: sha256.New(), left: 3, checksum: digest,
			}
			buf := make([]byte, 3)
			_, err := io.ReadFull(source, buf)
			if !errors.Is(err, test.err) || err == nil && !bytes.Equal(buf, []byte("abc")) {
				t.Fatalf("read %q with error %v, want %v", buf, err, test.err)
			}
		})
	}
}
