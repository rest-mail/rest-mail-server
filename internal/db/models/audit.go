package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Who an audit entry says acted.
const (
	AuditActorMailbox   = "mailbox"
	AuditActorAdmin     = "admin"
	AuditActorSystem    = "system"
	AuditActorAnonymous = "anonymous"
)

// How an audited request ended, from its HTTP status.
const (
	AuditOutcomeSuccess = "success" // 2xx
	AuditOutcomeDenied  = "denied"  // 401, 403, 429
	AuditOutcomeFailed  = "failed"  // any other 4xx
	AuditOutcomeError   = "error"   // 5xx
)

// AuditEntry is one entry of the audit log: who did what, through which
// route, and how it ended. The log only ever grows. Each entry carries the
// hash of the one before it, so an edit, a deletion or an insertion breaks the
// chain (internal/audit verifies it), and Postgres refuses UPDATE, DELETE and
// TRUNCATE on the table (see db.MigrateAuditLog).
//
// Entries are written only by audit.Append, which assigns Seq and the hashes.
type AuditEntry struct {
	// Seq is assigned by the writer: 1, 2, 3, … with no gaps. Not a database
	// sequence, which would leave gaps behind rolled-back transactions.
	Seq        int64     `gorm:"primaryKey;autoIncrement:false" json:"seq"`
	OccurredAt time.Time `gorm:"not null;index" json:"occurred_at"`
	// ActorKind is one of the AuditActor* kinds. A sign-in names the kind of
	// account it was for (username: admin, email: mailbox) even when it fails;
	// ActorID is set only once the account is proven.
	ActorKind string `gorm:"size:16;not null;index:idx_audit_log_actor" json:"actor_kind"`
	ActorID   *uint  `gorm:"index:idx_audit_log_actor" json:"actor_id"`
	// ActorName is the address or username at the time.
	ActorName string `gorm:"type:text;not null" json:"actor_name"`
	// CredentialID is the credential used: the app password a request was
	// made with, or the one refused when its secret matched. Nil for a session.
	CredentialID *uint `json:"credential_id"`
	// SessionID is the jti of the token the request carried.
	SessionID string `gorm:"type:text;not null" json:"session_id"`
	Action    string `gorm:"size:64;not null;index" json:"action"`
	Outcome   string `gorm:"size:16;not null;index" json:"outcome"`
	// ErrorCode is the {"error":{"code"}} of an error response.
	ErrorCode string `gorm:"type:text;not null" json:"error_code"`
	Method    string `gorm:"size:16;not null" json:"method"`
	// Route is chi's route pattern, e.g. /api/v1/admin/domains/{id}.
	Route  string `gorm:"type:text;not null" json:"route"`
	Status int    `gorm:"not null" json:"status"`
	// Target is the route's URL parameters, plus the body fields a route names.
	Target    AuditTarget `gorm:"type:jsonb;not null" json:"target"`
	IP        string      `gorm:"type:text;not null" json:"ip"`
	UserAgent string      `gorm:"type:text;not null" json:"user_agent"`
	RequestID string      `gorm:"type:text;not null" json:"request_id"`
	// PrevHash is the Hash of the entry before (64 zeros for the first), and
	// Hash is this entry's: hex SHA-256 (see audit.Hash).
	PrevHash string `gorm:"size:64;not null" json:"prev_hash"`
	Hash     string `gorm:"size:64;not null;uniqueIndex" json:"hash"`
}

func (AuditEntry) TableName() string { return "audit_log" }

// AuditTarget is what an audited request acted on, as string pairs. A map of
// strings, so encoding/json sorts its keys and the hash never depends on how
// Postgres stores the jsonb.
type AuditTarget map[string]string

func (t AuditTarget) Value() (driver.Value, error) {
	if t == nil {
		return "{}", nil
	}
	b, err := json.Marshal(map[string]string(t))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (t *AuditTarget) Scan(value interface{}) error {
	var b []byte
	switch v := value.(type) {
	case nil:
		*t = AuditTarget{}
		return nil
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("unsupported type: %T", value)
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*t = m
	return nil
}
