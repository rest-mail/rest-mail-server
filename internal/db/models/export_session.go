package models

import "time"

// ExportSession is an export of an account's data in progress (#2116 AC03b).
// Opening one writes an account.export audit entry for its start; closing it
// writes one for its end, completed or aborted, with the rows and bytes the
// exporter wrote, and deletes the row. One the exporter never closes is closed
// as aborted after exportsession.Timeout.
type ExportSession struct {
	// ID is the random handle the exporter closes it with.
	ID        string `gorm:"primaryKey;size:64"`
	MailboxID uint   `gorm:"not null;index"`
	// ActorID is the account that opened it; only that account may close it.
	ActorID uint `gorm:"not null"`
	// Data is what is exported (mail, contacts, calendar or settings) and
	// Format how it is written (maildir, vcard, icalendar or json).
	Data   string `gorm:"size:16;not null"`
	Format string `gorm:"size:16;not null"`
	// Opener is the audit entry of the request that opened it, so a session
	// closed for being abandoned is written with the actor, IP and client
	// that opened it.
	Opener    JSONB     `gorm:"type:jsonb;not null"`
	StartedAt time.Time `gorm:"not null;index"`
}
