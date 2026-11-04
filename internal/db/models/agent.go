package models

import "time"

// How far an assistant may go on one capability (#1423). LevelSuggest is what
// every capability starts at: an account with no AgentTrust row suggests.
const (
	AgentLevelSuggest = "suggest"
	AgentLevelApprove = "approve"
	AgentLevelAuto    = "auto"
)

// The capabilities an assistant holds a level on, one permission each.
const (
	AgentCapabilitySendMail        = "send_mail"
	AgentCapabilityFileNewsletters = "file_newsletters"
)

// AgentTrust is an account's level on one capability. A level says how far an
// assistant may go on its own; it never gives anything the grants do not.
type AgentTrust struct {
	MailboxID  uint      `gorm:"primaryKey;autoIncrement:false"`
	Capability string    `gorm:"primaryKey;size:64"`
	Level      string    `gorm:"size:16;not null"`
	UpdatedAt  time.Time `gorm:"not null"`

	Mailbox Mailbox `gorm:"foreignKey:MailboxID;constraint:OnDelete:CASCADE"`
}

func (AgentTrust) TableName() string { return "agent_trust" }

// AgentLedgerEntry is one action in an account's assistant ledger. It has the
// audit log's shape: entries are ordered and attributed, each carries the hash
// of the one before it in the same account, and Postgres refuses UPDATE, DELETE
// and TRUNCATE on the table (see db.MigrateAgentLedger). It outlives its
// account, as an audit entry does.
//
// Entries are written only by agentledger.Append, which assigns Seq and the
// hashes.
type AgentLedgerEntry struct {
	MailboxID uint `gorm:"primaryKey;autoIncrement:false"`
	// Seq counts within the account: 1, 2, 3, … with no gaps.
	Seq        int64     `gorm:"primaryKey;autoIncrement:false"`
	OccurredAt time.Time `gorm:"not null"`
	ActorKind  string    `gorm:"size:16;not null"`
	ActorID    *uint
	ActorName  string `gorm:"type:text;not null"`
	// CredentialID is the credential the entry was written with; nil for a
	// session.
	CredentialID *uint
	Capability   string `gorm:"size:64;not null;index"`
	// Level is the level the action ran at: a suggestion, an action a person
	// approved, or one taken automatically.
	Level  string      `gorm:"size:16;not null"`
	Action string      `gorm:"size:64;not null"`
	Target AuditTarget `gorm:"type:jsonb;not null"`
	// Undo says what reverses the action, as string pairs. Empty when nothing
	// does.
	Undo AuditTarget `gorm:"type:jsonb;not null"`
	// UndoUntil closes the entry's undo window; nil when it has none.
	UndoUntil *time.Time
	// Undoes is the Seq of the entry this one undoes. One entry undoes another
	// at most once.
	Undoes    *int64
	RequestID string `gorm:"type:text;not null"`
	// PrevHash is the Hash of the account's entry before (64 zeros for its
	// first), and Hash is this entry's: hex SHA-256 (see agentledger.Hash).
	PrevHash string `gorm:"size:64;not null"`
	Hash     string `gorm:"size:64;not null;uniqueIndex"`
}

func (AgentLedgerEntry) TableName() string { return "agent_ledger" }
