// Package importsession audits an import as one summary, not one entry per
// message (#1498). An importer opens a session, sends each message with its
// id, and closes it; closing writes one messages.import audit entry with the
// counts per folder. A session nobody closes is closed after IdleTimeout.
package importsession

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

// Action is the audit action of an import summary.
const Action = "messages.import"

// IdleTimeout is how long a session may go without an import or a close
// before it is closed and its summary written. An hour is twice RFC 3501's
// 30-minute autologout, so an IMAP connection is gone well before its session
// is swept, and a slow migration pausing between folders is not cut in two.
const IdleTimeout = time.Hour

// SweepInterval is how often a Sweeper looks for idle sessions.
const SweepInterval = time.Minute

// ErrNotFound is a session that does not exist, has closed, or belongs to
// another account or opener.
var ErrNotFound = errors.New("import session not found")

// Outcome is what one import attempt did.
type Outcome int

const (
	Imported Outcome = iota
	Deduplicated
	Refused
)

// Attempt is one import attempt as a summary counts it: what it did, the octets
// it stored, and whether a leading mbox From_ separator line was dropped from it.
type Attempt struct {
	Outcome               Outcome
	Bytes                 int64
	MboxSeparatorStripped bool
}

// FolderCounts is one folder's part of a summary.
type FolderCounts = apitypes.ImportFolderCounts

// Summary is what a closed session did.
type Summary = apitypes.ImportSessionSummary

// Open starts a session for accountID, opened by actorID. opener is the
// audit entry of the opening request; it is kept to write an idle close.
func Open(db *gorm.DB, opener *models.AuditEntry, accountID, actorID uint, clientName, clientVersion string, now time.Time) (*models.ImportSession, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(opener)
	if err != nil {
		return nil, err
	}
	s := &models.ImportSession{ID: hex.EncodeToString(raw[:]), MailboxID: accountID, ActorID: actorID,
		Opener: models.JSONB(encoded), ClientName: clientName, ClientVersion: clientVersion,
		Folders: models.JSONB("{}"), StartedAt: now.UTC(), LastSeenAt: now.UTC()}
	if err := db.Create(s).Error; err != nil {
		return nil, err
	}
	return s, nil
}

// lock reads the session for update, for accountID and actorID only.
func lock(tx *gorm.DB, id string, accountID, actorID uint) (*models.ImportSession, error) {
	var s models.ImportSession
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND mailbox_id = ? AND actor_id = ?", id, accountID, actorID).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &s, err
}

// Count adds one attempt to the session's folder. Called in the import's own
// transaction, the row lock serializes a session's imports, and a rolled-back
// import is not counted.
func Count(tx *gorm.DB, id string, accountID, actorID uint, folder string, a Attempt, now time.Time) error {
	s, err := lock(tx, id, accountID, actorID)
	if err != nil {
		return err
	}
	folders := map[string]FolderCounts{}
	if err := json.Unmarshal(s.Folders, &folders); err != nil {
		return err
	}
	counts := folders[folder]
	add(&counts, a)
	folders[folder] = counts
	encoded, err := json.Marshal(folders)
	if err != nil {
		return err
	}
	return tx.Model(&models.ImportSession{}).Where("id = ?", s.ID).
		Updates(map[string]any{"folders": models.JSONB(encoded), "last_seen_at": now.UTC()}).Error
}

func add(c *FolderCounts, a Attempt) {
	if a.MboxSeparatorStripped {
		c.MboxSeparatorStripped++
	}
	switch a.Outcome {
	case Imported:
		c.Imported++
		c.Bytes += a.Bytes
	case Deduplicated:
		c.Deduplicated++
	default:
		c.Refused++
	}
}

// Close ends the session and writes its summary with entry, the closing
// request's audit entry.
func Close(db *gorm.DB, id string, accountID, actorID uint, entry *models.AuditEntry, now time.Time) (Summary, error) {
	var summary Summary
	err := db.Transaction(func(tx *gorm.DB) error {
		s, err := lock(tx, id, accountID, actorID)
		if err != nil {
			return err
		}
		summary, err = finish(tx, s, entry, "closed", now)
		return err
	})
	return summary, err
}

// CloseIdle closes every session idle since before now-IdleTimeout, each
// written with the entry of the request that opened it. It returns how many
// it closed.
func CloseIdle(db *gorm.DB, now time.Time) (int, error) {
	var ids []string
	if err := db.Model(&models.ImportSession{}).Where("last_seen_at < ?", now.Add(-IdleTimeout)).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	closed := 0
	for _, id := range ids {
		err := db.Transaction(func(tx *gorm.DB) error {
			var s models.ImportSession
			// SKIP LOCKED leaves a session an import is counting into to the next pass.
			err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Where("id = ? AND last_seen_at < ?", id, now.Add(-IdleTimeout)).Take(&s).Error
			if err != nil {
				return err
			}
			var opener models.AuditEntry
			if err := json.Unmarshal(s.Opener, &opener); err != nil {
				return err
			}
			opener.Seq, opener.PrevHash, opener.Hash = 0, "", ""
			opener.OccurredAt = now
			_, err = finish(tx, &s, &opener, "idle", now)
			return err
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

// Single writes the summary of an import sent without a session: one message
// in one folder, so nothing imported goes unaudited.
func Single(tx *gorm.DB, entry *models.AuditEntry, accountID uint, folder string, a Attempt) error {
	var counts FolderCounts
	add(&counts, a)
	at := entry.OccurredAt.UTC()
	summary := Summary{AccountID: accountID, StartedAt: at, EndedAt: at, Folders: map[string]FolderCounts{folder: counts}}
	totals(&summary)
	describe(entry, summary, "single")
	return audit.Append(tx, entry)
}

func finish(tx *gorm.DB, s *models.ImportSession, entry *models.AuditEntry, closedBy string, now time.Time) (Summary, error) {
	summary := Summary{ID: s.ID, AccountID: s.MailboxID, ClientName: s.ClientName, ClientVersion: s.ClientVersion,
		StartedAt: s.StartedAt.UTC(), EndedAt: now.UTC(), Folders: map[string]FolderCounts{}}
	if err := json.Unmarshal(s.Folders, &summary.Folders); err != nil {
		return summary, err
	}
	totals(&summary)
	if err := tx.Delete(&models.ImportSession{}, "id = ?", s.ID).Error; err != nil {
		return summary, err
	}
	describe(entry, summary, closedBy)
	return summary, audit.Append(tx, entry)
}

func totals(s *Summary) {
	for _, c := range s.Folders {
		s.Imported += c.Imported
		s.Deduplicated += c.Deduplicated
		s.Refused += c.Refused
		s.Bytes += c.Bytes
		s.MboxSeparatorStripped += c.MboxSeparatorStripped
	}
}

// describe puts the summary in the entry's target: totals, client, times and
// the per-folder counts as one JSON value. Never one field per message.
func describe(e *models.AuditEntry, s Summary, closedBy string) {
	e.Action = Action
	if e.Target == nil {
		e.Target = models.AuditTarget{}
	}
	t := e.Target
	t["account_id"] = strconv.FormatUint(uint64(s.AccountID), 10)
	if s.ID != "" {
		t["import_session"] = s.ID
	}
	t["closed_by"] = closedBy
	if s.ClientName != "" {
		t["client_name"] = s.ClientName
	}
	if s.ClientVersion != "" {
		t["client_version"] = s.ClientVersion
	}
	t["started_at"] = s.StartedAt.Format(time.RFC3339)
	t["ended_at"] = s.EndedAt.Format(time.RFC3339)
	t["imported"] = strconv.FormatInt(s.Imported, 10)
	t["deduplicated"] = strconv.FormatInt(s.Deduplicated, 10)
	t["refused"] = strconv.FormatInt(s.Refused, 10)
	t["bytes"] = strconv.FormatInt(s.Bytes, 10)
	t["mbox_separator_stripped"] = strconv.FormatInt(s.MboxSeparatorStripped, 10)
	folders, _ := json.Marshal(s.Folders) // map keys marshal sorted
	t["folders"] = string(folders)
}

// Sweeper closes idle sessions every SweepInterval.
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
			if _, err := CloseIdle(s.db, time.Now()); err != nil {
				slog.Error("importsession: closing idle import sessions failed", "error", err)
			}
		case <-s.stop:
			return
		}
	}
}
