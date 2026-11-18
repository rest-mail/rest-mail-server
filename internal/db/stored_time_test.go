package db_test

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

// storedTimeRow holds each kind of timestamp a create can carry.
type storedTimeRow struct {
	ID         uint `gorm:"primaryKey"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	HappenedAt time.Time
	SeenAt     *time.Time
	NeverSet   time.Time
	NeverSeen  *time.Time
	Unstored   time.Time `gorm:"-"`
}

// finerThanStored is finer than a timestamp column holds, on every platform.
var finerThanStored = time.Date(2026, 11, 18, 12, 0, 0, 123456789, time.UTC)

const asStored = 123456000

func openStoredTimeDB(t *testing.T) *gorm.DB {
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
	cfg := config.Config{DBHost: env("DB_HOST", "localhost"), DBPort: port,
		DBName: env("DB_NAME", "restmail"), DBUser: env("DB_USER", "restmail"), DBPass: env("DB_PASS", "restmail")}
	clone := testdb.Clone(t, &cfg, "storedtime", testdb.Models(&storedTimeRow{}))
	cfg.DBName = clone.Migrator().CurrentDatabase()
	database, err := rmdb.ConnectWithClock(&cfg, func() time.Time { return finerThanStored })
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return database
}

func assertStoredInstant(t *testing.T, name string, got time.Time) {
	t.Helper()
	if want := finerThanStored.Truncate(time.Microsecond); !got.Equal(want) || got.Nanosecond() != asStored {
		t.Errorf("%s = %s, want %s", name, got.UTC().Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

// assertMatchesRead proves the row a create handed back is the row a later read returns.
func assertMatchesRead(t *testing.T, database *gorm.DB, created storedTimeRow) {
	t.Helper()
	var read storedTimeRow
	if err := database.First(&read, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		name          string
		created, read time.Time
	}{
		{"created_at", created.CreatedAt, read.CreatedAt},
		{"updated_at", created.UpdatedAt, read.UpdatedAt},
		{"happened_at", created.HappenedAt, read.HappenedAt},
		{"seen_at", *created.SeenAt, *read.SeenAt},
	} {
		assertStoredInstant(t, "created "+pair.name, pair.created)
		if !pair.created.Equal(pair.read) {
			t.Errorf("%s: create returned %s, read returned %s", pair.name,
				pair.created.UTC().Format(time.RFC3339Nano), pair.read.UTC().Format(time.RFC3339Nano))
		}
	}
	if !created.NeverSet.IsZero() || created.NeverSeen != nil || read.NeverSeen != nil {
		t.Errorf("unset timestamps changed: %+v, %+v", created, read)
	}
}

func newStoredTimeRow() storedTimeRow {
	seen := finerThanStored
	return storedTimeRow{HappenedAt: finerThanStored, SeenAt: &seen, Unstored: finerThanStored}
}

func TestCreateReturnsTheTimestampsALaterReadReturns(t *testing.T) {
	database := openStoredTimeDB(t)

	t.Run("one row", func(t *testing.T) {
		row := newStoredTimeRow()
		original := row.SeenAt
		if err := database.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		assertMatchesRead(t, database, row)
		if !row.Unstored.Equal(finerThanStored) {
			t.Errorf("a field that is not stored changed: %s", row.Unstored)
		}
		if !original.Equal(finerThanStored) {
			t.Errorf("the caller's own time changed: %s", original)
		}
	})
	t.Run("rows", func(t *testing.T) {
		rows := []storedTimeRow{newStoredTimeRow(), newStoredTimeRow()}
		if err := database.Create(&rows).Error; err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			assertMatchesRead(t, database, row)
		}
	})
	t.Run("row pointers in batches", func(t *testing.T) {
		first, second := newStoredTimeRow(), newStoredTimeRow()
		rows := []*storedTimeRow{&first, &second}
		if err := database.CreateInBatches(rows, 1).Error; err != nil {
			t.Fatal(err)
		}
		assertMatchesRead(t, database, first)
		assertMatchesRead(t, database, second)
	})
	t.Run("inside a transaction", func(t *testing.T) {
		row := newStoredTimeRow()
		if err := database.Transaction(func(tx *gorm.DB) error { return tx.Create(&row).Error }); err != nil {
			t.Fatal(err)
		}
		assertMatchesRead(t, database, row)
	})
}

// An update stamps updated_at from the same clock, so its answer matches a later read too.
func TestUpdateReturnsTheUpdatedAtALaterReadReturns(t *testing.T) {
	database := openStoredTimeDB(t)
	row := newStoredTimeRow()
	if err := database.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&row).Update("never_set", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)).Error; err != nil {
		t.Fatal(err)
	}
	assertStoredInstant(t, "updated_at", row.UpdatedAt)
	var read storedTimeRow
	if err := database.First(&read, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !read.UpdatedAt.Equal(row.UpdatedAt) {
		t.Errorf("update returned %s, read returned %s", row.UpdatedAt, read.UpdatedAt)
	}
}
