// Package exportsession audits an export of an account's data (#2116 AC03b).
// An exporter opens a session before it writes anything, which records the
// export's start, and closes it when it ends, which records whether it
// completed or aborted with the rows and bytes written. Both are account.export
// audit entries naming the actor, the account, the data and the format. A
// session nobody closes is closed as aborted after Timeout.
package exportsession

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Action is the audit action of an export's start and of its end.
const Action = "account.export"

// The results an export entry records.
const (
	Started   = "started"
	Completed = "completed"
	Aborted   = "aborted"
)

// Timeout is how long after its start an export may stay open. An export
// makes no request of the session while it runs, so this is from the start,
// not from the last use: a day is far longer than an export of the largest
// account takes, and a crashed exporter's session is still closed the same day.
const Timeout = 24 * time.Hour

// SweepInterval is how often a Sweeper looks for abandoned sessions.
const SweepInterval = 5 * time.Minute

// ErrNotFound is a session that does not exist, has closed, or belongs to
// another account or opener.
var ErrNotFound = errors.New("export session not found")

// Summary is what a closed export wrote.
type Summary = apitypes.ExportSessionSummary

// Open starts an export of accountID's data by actorID and writes its start
// with entry, the opening request's audit entry, in the same transaction.
func Open(db *gorm.DB, entry *models.AuditEntry, accountID, actorID uint, data, format string, now time.Time) (*models.ExportSession, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	opener, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	s := &models.ExportSession{ID: hex.EncodeToString(raw[:]), MailboxID: accountID, ActorID: actorID,
		Data: data, Format: format, Opener: models.JSONB(opener), StartedAt: now.UTC()}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(s).Error; err != nil {
			return err
		}
		describe(entry, s, Started)
		return audit.Append(tx, entry)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Close ends the session and writes its end with entry, the closing request's
// audit entry: the result, the rows and bytes written and any folders left out.
func Close(db *gorm.DB, id string, accountID, actorID uint, entry *models.AuditEntry, end apitypes.CloseExportSessionBody, now time.Time) (Summary, error) {
	var summary Summary
	err := db.Transaction(func(tx *gorm.DB) error {
		var s models.ExportSession
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND mailbox_id = ? AND actor_id = ?", id, accountID, actorID).Take(&s).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		summary = Summary{ID: s.ID, AccountID: s.MailboxID, Data: s.Data, Format: s.Format, Result: end.Result,
			Rows: end.Rows, Bytes: end.Bytes, SkippedFolders: end.SkippedFolders, StartedAt: s.StartedAt.UTC(), EndedAt: now.UTC()}
		if summary.SkippedFolders == nil {
			summary.SkippedFolders = []string{}
		}
		return finish(tx, &s, entry, &summary, "exporter")
	})
	return summary, err
}

// CloseAbandoned closes every session open since before now-Timeout as
// aborted, each written with the entry of the request that opened it. The
// exporter never reported what it wrote, so its entry has no rows or bytes.
// It returns how many it closed.
func CloseAbandoned(db *gorm.DB, now time.Time) (int, error) {
	var ids []string
	if err := db.Model(&models.ExportSession{}).Where("started_at < ?", now.Add(-Timeout)).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	closed := 0
	for _, id := range ids {
		err := db.Transaction(func(tx *gorm.DB) error {
			var s models.ExportSession
			// SKIP LOCKED leaves a session its exporter is closing to that close.
			err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Where("id = ? AND started_at < ?", id, now.Add(-Timeout)).Take(&s).Error
			if err != nil {
				return err
			}
			var opener models.AuditEntry
			if err := json.Unmarshal(s.Opener, &opener); err != nil {
				return err
			}
			opener.Seq, opener.PrevHash, opener.Hash = 0, "", ""
			opener.OccurredAt = now
			return finish(tx, &s, &opener, &Summary{Result: Aborted, StartedAt: s.StartedAt.UTC(), EndedAt: now.UTC()}, "timeout")
		})
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
		case err != nil:
			return closed, err
		default:
			closed++
		}
	}
	return closed, nil
}

func finish(tx *gorm.DB, s *models.ExportSession, entry *models.AuditEntry, summary *Summary, closedBy string) error {
	if err := tx.Delete(&models.ExportSession{}, "id = ?", s.ID).Error; err != nil {
		return err
	}
	describe(entry, s, summary.Result)
	t := entry.Target
	t["closed_by"] = closedBy
	t["ended_at"] = summary.EndedAt.Format(time.RFC3339)
	if closedBy != "timeout" {
		t["rows"] = strconv.FormatInt(summary.Rows, 10)
		t["bytes"] = strconv.FormatInt(summary.Bytes, 10)
	}
	if len(summary.SkippedFolders) > 0 {
		skipped, _ := json.Marshal(summary.SkippedFolders)
		t["skipped_folders"] = string(skipped)
	}
	return audit.Append(tx, entry)
}

// describe puts what the export is in the entry's target: the account, the
// session, the data, the format, when it started and its result so far.
func describe(e *models.AuditEntry, s *models.ExportSession, result string) {
	e.Action = Action
	if e.Target == nil {
		e.Target = models.AuditTarget{}
	}
	t := e.Target
	t["account_id"] = strconv.FormatUint(uint64(s.MailboxID), 10)
	t["export_session"] = s.ID
	t["data"] = s.Data
	t["format"] = s.Format
	t["result"] = result
	t["started_at"] = s.StartedAt.UTC().Format(time.RFC3339)
}

// Sweeper closes abandoned sessions every SweepInterval.
type Sweeper struct {
	db   *gorm.DB
	stop chan struct{}
}

// NewSweeper is a Sweeper over db.
func NewSweeper(db *gorm.DB) *Sweeper { return &Sweeper{db: db, stop: make(chan struct{})} }

// Start runs the sweeper until Shutdown.
func (s *Sweeper) Start() { go s.run() }

// Shutdown stops the sweeper.
func (s *Sweeper) Shutdown() { close(s.stop) }

func (s *Sweeper) run() {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := CloseAbandoned(s.db, time.Now()); err != nil {
				slog.Error("exportsession: closing abandoned export sessions failed", "error", err)
			}
		case <-s.stop:
			return
		}
	}
}
