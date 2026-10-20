package audit

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// openAuditDB gives a test a database of its own, dropped afterwards, with
// the audit log migrated as a server migrates it. Its own, because the log
// cannot be emptied once written, and because the concurrency test needs real
// connections. It skips (never fails) when no database is reachable.
func openAuditDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, _ := strconv.Atoi(envOr("DB_PORT", "5432"))
	cfg := config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: port,
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	admin, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Skipf("audit DB test skipped: no database reachable (%v)", err)
	}
	name := fmt.Sprintf("restmail_audit_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skipf("audit DB test skipped: cannot create a database (%v)", err)
	}
	cfg.DBName = name
	scratch, err := rmdb.Connect(&cfg)
	if err != nil {
		t.Fatalf("connect to scratch database: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := scratch.DB(); err == nil {
			_ = sqlDB.Close()
		}
		admin.Exec("DROP DATABASE IF EXISTS " + name)
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := rmdb.MigrateAuditLog(scratch); err != nil {
		t.Fatalf("migrate the audit log: %v", err)
	}
	// Every start runs it again.
	if err := rmdb.MigrateAuditLog(scratch); err != nil {
		t.Fatalf("migrate the audit log a second time: %v", err)
	}
	return scratch
}

func appendN(t *testing.T, db *gorm.DB, n int) []models.AuditEntry {
	t.Helper()
	out := make([]models.AuditEntry, n)
	for i := range out {
		e := entryAt(0)
		e.OccurredAt = time.Time{}
		if err := Append(db, &e); err != nil {
			t.Fatalf("append %d: %v", i+1, err)
		}
		out[i] = e
	}
	return out
}

func TestAppend_FormsAValidChain(t *testing.T) {
	db := openAuditDB(t)
	written := appendN(t, db, 5)

	for i, e := range written {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d got seq %d", i+1, e.Seq)
		}
	}
	if written[0].PrevHash != GenesisHash {
		t.Errorf("first prev_hash = %s, want 64 zeros", written[0].PrevHash)
	}
	if written[3].PrevHash != written[2].Hash {
		t.Error("an entry's prev_hash is not the hash of the one before")
	}

	res, err := VerifyDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Checked != 5 || res.FirstSeq != 1 || res.LastSeq != 5 || res.LastHash != written[4].Hash {
		t.Fatalf("verify: %+v", res)
	}
}

// Appends from many connections at once still number 1, 2, 3, … with no
// gaps and one line of hashes: the advisory lock serialises them.
func TestAppend_ConcurrentAppendsStayContiguous(t *testing.T) {
	db := openAuditDB(t)
	const workers, each = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				e := entryAt(0)
				if err := Append(db, &e); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}

	var seqs []int64
	if err := db.Model(&models.AuditEntry{}).Order("seq").Pluck("seq", &seqs).Error; err != nil {
		t.Fatal(err)
	}
	if len(seqs) != workers*each {
		t.Fatalf("%d entries, want %d", len(seqs), workers*each)
	}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("seq %d at position %d: not contiguous", seq, i+1)
		}
	}
	res, err := VerifyDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Checked != workers*each {
		t.Fatalf("verify: %+v", res)
	}
}

// Postgres itself refuses to change or remove an entry.
func TestAuditLog_RefusesUpdateDeleteTruncate(t *testing.T) {
	db := openAuditDB(t)
	appendN(t, db, 2)

	for _, stmt := range []string{
		"UPDATE audit_log SET actor_name = 'someone else' WHERE seq = 1",
		"DELETE FROM audit_log WHERE seq = 2",
		"DELETE FROM audit_log",
		"TRUNCATE audit_log",
	} {
		err := db.Exec(stmt).Error
		if err == nil || !strings.Contains(err.Error(), "audit_log is append-only") {
			t.Errorf("%s: err = %v, want the append-only refusal", stmt, err)
		}
	}
	// gorm's own paths meet the same refusal.
	if err := db.Model(&models.AuditEntry{}).Where("seq = ?", 1).Update("outcome", "success").Error; err == nil {
		t.Error("gorm Update succeeded")
	}
	if err := db.Where("seq = ?", 1).Delete(&models.AuditEntry{}).Error; err == nil {
		t.Error("gorm Delete succeeded")
	}

	res, err := VerifyDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Checked != 2 {
		t.Fatalf("after the refused changes: %+v", res)
	}
}

// What Postgres gives back (a timestamptz in the session's zone, a jsonb with
// its keys reordered) still hashes to what was written.
func TestAppend_RoundTripStillVerifies(t *testing.T) {
	db := openAuditDB(t)
	e := entryAt(0)
	e.OccurredAt = time.Date(2026, 10, 17, 23, 59, 59, 999999999, time.FixedZone("UTC+13", 13*3600))
	e.ActorID = nil
	e.Target = models.AuditTarget{"zz": "last", "a": "first", "id": "42", "address": "<x&y>@example.com"}
	e.UserAgent = "Mozilla/5.0 (ü) \"quoted\"   bad:\xff"
	e.ActorName = "nul\x00name"
	if err := Append(db, &e); err != nil {
		t.Fatal(err)
	}

	var got models.AuditEntry
	if err := db.First(&got, "seq = ?", e.Seq).Error; err != nil {
		t.Fatal(err)
	}
	if Hash(&got) != e.Hash || got.Hash != e.Hash {
		t.Fatalf("read back, the entry hashes to %s; written as %s", Hash(&got), e.Hash)
	}
	if !got.OccurredAt.Equal(e.OccurredAt) || got.ActorID != nil || got.Target["address"] != "<x&y>@example.com" {
		t.Errorf("read back differently: %+v", got)
	}
	res, err := VerifyDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("verify: %+v", res)
	}
}

// Append works inside a caller's transaction, as a grant and its entry will
// (#345): they commit together, and a rollback takes the entry with it and
// leaves no gap.
func TestAppend_InsideACallersTransaction(t *testing.T) {
	db := openAuditDB(t)

	tx := db.Begin()
	e := entryAt(0)
	if err := Append(tx, &e); err != nil {
		t.Fatal(err)
	}
	if e.Seq != 1 {
		t.Fatalf("seq %d inside the transaction, want 1", e.Seq)
	}
	tx.Rollback()

	err := db.Transaction(func(tx *gorm.DB) error {
		first, second := entryAt(0), entryAt(0)
		if err := Append(tx, &first); err != nil {
			return err
		}
		if err := Append(tx, &second); err != nil {
			return err
		}
		if first.Seq != 1 || second.Seq != 2 || second.PrevHash != first.Hash {
			t.Errorf("in a transaction: seqs %d, %d", first.Seq, second.Seq)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := VerifyDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Checked != 2 {
		t.Fatalf("verify: %+v", res)
	}
}

// The walk reads in batches and joins them without a seam.
func TestVerifyDB_AcrossBatches(t *testing.T) {
	db := openAuditDB(t)
	// One transaction: a commit per entry is slow and proves nothing here.
	tx := db.Begin()
	appendN(t, tx, verifyBatch+5)
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	res, err := VerifyDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Checked != verifyBatch+5 || res.LastSeq != verifyBatch+5 {
		t.Fatalf("verify: %+v", res)
	}

	// An anchor in the second batch is found there, and an anchor past the
	// end is reported once the last batch is read.
	var anchored models.AuditEntry
	if err := db.Where("seq = ?", verifyBatch+2).First(&anchored).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		anchor Anchor
		ok     bool
	}{
		{Anchor{Seq: anchored.Seq, Hash: anchored.Hash}, true},
		{Anchor{Seq: anchored.Seq, Hash: GenesisHash}, false},
		{Anchor{Seq: verifyBatch + 6, Hash: anchored.Hash}, false},
	} {
		res, err := VerifyDBAnchored(db, &tc.anchor)
		if err != nil {
			t.Fatal(err)
		}
		if res.OK != tc.ok || (!tc.ok && (res.BrokenAtSeq == nil || *res.BrokenAtSeq != tc.anchor.Seq)) {
			t.Errorf("anchor %+v: %+v", tc.anchor, res)
		}
	}
}
