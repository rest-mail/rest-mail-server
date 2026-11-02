package models

import "time"

// Inbound message states. A message in InboundFailed has run out of attempts and stays for an
// operator to look at; one that finished has no row at all.
const (
	InboundPending    = "pending"
	InboundProcessing = "processing"
	InboundFailed     = "failed"
)

// InboundMessage is one recipient's copy of an accepted message, waiting for its content checks
// and delivery (#1437). The sender has already been told 250, so the server owns it: the row and
// the stored object outlive a crash. The bytes are in the message store and never here, and the
// recipients of one message share its object.
type InboundMessage struct {
	ID uint `gorm:"primaryKey"`
	// ObjectRef, Size and Checksum say where the bytes are and how to check them.
	ObjectRef string `gorm:"size:128;not null;uniqueIndex:idx_inbound_object_recipient,priority:1"`
	Size      int64  `gorm:"not null"`
	Checksum  string `gorm:"size:64;not null"`
	// The envelope, which the bytes do not hold.
	MailFrom  string `gorm:"size:255;not null"`
	Recipient string `gorm:"size:255;not null;uniqueIndex:idx_inbound_object_recipient,priority:2"`
	ClientIP  string `gorm:"size:64"`
	HeloName  string `gorm:"size:255"`
	// How it arrived.
	ReceivedTLS *bool  `gorm:"column:received_tls"`
	TLSVersion  string `gorm:"column:tls_version;size:16"`
	TLSCipher   string `gorm:"column:tls_cipher;size:64"`
	RequireTLS  bool   `gorm:"column:require_tls;not null;default:false"`

	// PipelineState is what the required checks left for the content checks.
	PipelineState JSONB `gorm:"type:jsonb"`

	Status      string     `gorm:"size:16;not null;index:idx_inbound_ready,priority:1"`
	Attempts    int        `gorm:"not null;default:0"`
	NextAttempt time.Time  `gorm:"not null;index:idx_inbound_ready,priority:2"`
	LastAttempt *time.Time ``
	LastError   string     `gorm:"type:text"`
	CreatedAt   time.Time  `gorm:"not null"`
}

func (InboundMessage) TableName() string { return "inbound_messages" }
