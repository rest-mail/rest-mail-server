// Package agentledger keeps each account's assistant ledger (#1423): the same
// append-only, hash-chained record as the audit log (internal/audit), with one
// chain per account, so that an account's history can be checked from what a
// reader of that account is given.
package agentledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// hashVersion is the first element of every hashed array. A change to what is
// hashed, or how, gets a new version.
const hashVersion = "restmail-agent-ledger-v1"

// GenesisHash is the prev_hash of an account's first entry.
var GenesisHash = strings.Repeat("0", 64)

// lockClass is the high half of the advisory lock an append takes, "ledg"; the
// account is the low half, so appends to one account form one line and appends
// to different accounts do not wait for each other.
const lockClass int64 = 0x6c656467

// Hash is an entry's hash: hex SHA-256 over the JSON encoding of
//
//	["restmail-agent-ledger-v1", mailbox_id, seq, occurred_at, actor_kind,
//	 actor_id|null, actor_name, credential_id|null, capability, level, action,
//	 target, undo, undo_until|null, undoes|null, request_id, prev_hash]
//
// as encoding/json writes it with HTML escaping off and no trailing newline.
// Times are RFC 3339 with nanoseconds, in UTC; target and undo are JSON objects
// with sorted keys. It hashes the Go values, never bytes read back from jsonb,
// which Postgres normalises.
func Hash(e *models.AgentLedgerEntry) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Strings, integers, nil and maps of strings: Encode cannot fail.
	_ = enc.Encode([]any{
		hashVersion,
		e.MailboxID,
		e.Seq,
		hashTime(e.OccurredAt),
		e.ActorKind,
		optionalID(e.ActorID),
		e.ActorName,
		optionalID(e.CredentialID),
		e.Capability,
		e.Level,
		e.Action,
		pairs(e.Target),
		pairs(e.Undo),
		optionalTime(e.UndoUntil),
		optionalSeq(e.Undoes),
		e.RequestID,
		e.PrevHash,
	})
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

func hashTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func pairs(p models.AuditTarget) map[string]string {
	if p == nil {
		return map[string]string{}
	}
	return p
}

func optionalID(id *uint) any {
	if id == nil {
		return nil
	}
	return *id
}

func optionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return hashTime(*t)
}

func optionalSeq(seq *int64) any {
	if seq == nil {
		return nil
	}
	return *seq
}

// StoredTime is t as Postgres keeps it, so that what is hashed is what is
// stored: UTC, to the microsecond.
func StoredTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// Lock takes the account's append lock until the transaction ends. A caller
// that decides something from the ledger before appending takes it first, so
// that what it read still holds when it appends.
func Lock(tx *gorm.DB, mailboxID uint) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", lockClass<<32|int64(mailboxID)).Error
}

// Append adds e to the end of its account's ledger: it takes the lock, reads the
// account's last entry, and inserts e after it with that entry's hash as its
// prev_hash. It fills in e.Seq, e.PrevHash and e.Hash (and e.OccurredAt when
// unset). The caller has checked e's text is storable.
//
// It runs in a transaction of its own, or inside the caller's when db is one.
func Append(db *gorm.DB, e *models.AgentLedgerEntry) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := Lock(tx, e.MailboxID); err != nil {
			return err
		}
		var last struct {
			Seq  int64
			Hash string
		}
		if err := tx.Raw("SELECT seq, hash FROM agent_ledger WHERE mailbox_id = ? ORDER BY seq DESC LIMIT 1", e.MailboxID).Scan(&last).Error; err != nil {
			return err
		}
		if e.OccurredAt.IsZero() {
			e.OccurredAt = time.Now()
		}
		e.OccurredAt = StoredTime(e.OccurredAt)
		if e.UndoUntil != nil {
			until := StoredTime(*e.UndoUntil)
			e.UndoUntil = &until
		}
		e.Target, e.Undo = pairs(e.Target), pairs(e.Undo)
		e.Seq = last.Seq + 1
		e.PrevHash = GenesisHash
		if last.Seq > 0 {
			e.PrevHash = last.Hash
		}
		e.Hash = Hash(e)
		return tx.Create(e).Error
	})
}
