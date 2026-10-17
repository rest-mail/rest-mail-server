// Package audit keeps the audit log: an append-only, hash-chained record of
// sign-ins and administrative changes (models.AuditEntry, table audit_log).
//
// Every entry carries the hash of the one before it, so changing, removing or
// inserting an entry anywhere breaks the chain from that point on, and Verify
// finds it. Inside REST Mail nothing can change an entry: Postgres refuses
// UPDATE, DELETE and TRUNCATE on the table. Below REST Mail, whoever runs the
// database could recompute every hash after an edit; anchoring the latest
// hash somewhere they do not control is what catches that, and comes later.
package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// hashVersion is the first element of every hashed array. A change to what is
// hashed, or how, gets a new version.
const hashVersion = "restmail-audit-v1"

// GenesisHash is the prev_hash of the first entry.
var GenesisHash = strings.Repeat("0", 64)

// appendLockKey is the transaction-level advisory lock every append takes, so
// that appends from any number of API replicas form one line: "restaudt".
const appendLockKey int64 = 0x7265737461756474

// maxText caps every free-text field, in characters: a user agent, a request
// id, an identifier or an address typed by whoever is calling.
const maxText = 512

// Hash is an entry's hash: hex SHA-256 over the JSON encoding of
//
//	["restmail-audit-v1", seq, occurred_at, actor_kind, actor_id|null,
//	 actor_name, credential_id|null, session_id, action, outcome, error_code,
//	 method, route, status, target, ip, user_agent, request_id, prev_hash]
//
// as encoding/json writes it with HTML escaping off and no trailing newline.
// occurred_at is RFC 3339 with nanoseconds, in UTC; target is a JSON object
// with sorted keys. It hashes the Go values, never bytes read back from jsonb,
// which Postgres normalises.
func Hash(e *models.AuditEntry) string {
	target := map[string]string(e.Target)
	if target == nil {
		target = map[string]string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Strings, integers, nil and a map of strings: Encode cannot fail.
	_ = enc.Encode([]any{
		hashVersion,
		e.Seq,
		e.OccurredAt.UTC().Format(time.RFC3339Nano),
		e.ActorKind,
		optionalID(e.ActorID),
		e.ActorName,
		optionalID(e.CredentialID),
		e.SessionID,
		e.Action,
		e.Outcome,
		e.ErrorCode,
		e.Method,
		e.Route,
		e.Status,
		target,
		e.IP,
		e.UserAgent,
		e.RequestID,
		e.PrevHash,
	})
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func optionalID(id *uint) any {
	if id == nil {
		return nil
	}
	return *id
}

// normalize puts an entry in the form it is stored in, so that what is hashed
// is exactly what Postgres keeps: the time in UTC to the microsecond, and every
// string valid UTF-8 without NUL (text and jsonb refuse both) and at most
// maxText characters.
func normalize(e *models.AuditEntry) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	e.OccurredAt = e.OccurredAt.UTC().Truncate(time.Microsecond)
	if e.ActorKind == "" {
		e.ActorKind = models.AuditActorAnonymous
	}
	for _, s := range []*string{
		&e.ActorKind, &e.ActorName, &e.SessionID, &e.Action, &e.Outcome, &e.ErrorCode,
		&e.Method, &e.Route, &e.IP, &e.UserAgent, &e.RequestID,
	} {
		*s = clean(*s)
	}
	target := make(models.AuditTarget, len(e.Target))
	for k, v := range e.Target {
		target[clean(k)] = clean(v)
	}
	e.Target = target
}

// clean makes s storable: valid UTF-8, no NUL, at most maxText characters.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\x00", "�")
	if utf8.RuneCountInString(s) > maxText {
		s = string([]rune(s)[:maxText])
	}
	return s
}

// Append adds e to the end of the log: it takes the append lock, reads the
// last entry, and inserts e after it with that entry's hash as its prev_hash.
// It fills in e.Seq, e.PrevHash and e.Hash (and e.OccurredAt when unset).
//
// It runs in a transaction of its own, or inside the caller's when db is one,
// so a change and its audit entry can commit together. The lock is held until
// that transaction ends.
func Append(db *gorm.DB, e *models.AuditEntry) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", appendLockKey).Error; err != nil {
			return err
		}
		var last struct {
			Seq  int64
			Hash string
		}
		if err := tx.Raw("SELECT seq, hash FROM audit_log ORDER BY seq DESC LIMIT 1").Scan(&last).Error; err != nil {
			return err
		}
		normalize(e)
		e.Seq = last.Seq + 1
		e.PrevHash = GenesisHash
		if last.Seq > 0 {
			e.PrevHash = last.Hash
		}
		e.Hash = Hash(e)
		return tx.Create(e).Error
	})
}

// ActorID finds the id of the account a sign-in named, by its address. Nil
// when there is none.
func ActorID(db *gorm.DB, kind, name string) (*uint, error) {
	if kind != models.AuditActorAccount {
		return nil, nil
	}
	var row struct{ ID uint }
	err := db.Model(&models.Mailbox{}).Select("id").Where("address = ?", name).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row.ID, nil
}
