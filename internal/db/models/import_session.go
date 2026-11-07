package models

import "time"

// ImportSession is an import in progress: the counts an import summary will
// record once it closes (#1498). Each import adds to its folder's counts, and
// closing it writes one messages.import audit entry and deletes the row, so a
// closed import keeps nothing here and a 50k-message import is one row.
type ImportSession struct {
	// ID is the random handle the importer sends with each message.
	ID        string `gorm:"primaryKey;size:64"`
	MailboxID uint   `gorm:"not null;index"`
	// ActorID is the account that opened it; only that account may import
	// into it or close it.
	ActorID uint `gorm:"not null"`
	// Opener is the audit entry of the request that opened it, so a session
	// closed for being idle is written with the actor, IP and client that
	// opened it.
	Opener        JSONB     `gorm:"type:jsonb;not null"`
	ClientName    string    `gorm:"type:text;not null"`
	ClientVersion string    `gorm:"type:text;not null"`
	Folders       JSONB     `gorm:"type:jsonb;not null"`
	StartedAt     time.Time `gorm:"not null"`
	LastSeenAt    time.Time `gorm:"not null;index"`
}
