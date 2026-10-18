package models

import "time"

// Push services a device can be registered with. Only APNs has a sender so far.
const (
	PushPlatformAPNs = "apns"
)

// APNs environments: production for App Store and TestFlight builds, sandbox
// for builds run from Xcode. Each has its own host, and a token from one is
// refused by the other.
const (
	APNsProduction = "production"
	APNsSandbox    = "sandbox"
)

// PushDevice is a phone a session registered for push (#363). It belongs to
// that session, by its sid, and goes when the session ends. When mail reaches
// the inbox of any mailbox the session lists, the phone is sent a signal to
// fetch it, and nothing of the mail.
type PushDevice struct {
	ID uint `gorm:"primaryKey" json:"-"`
	// SessionID is the sid of the session that registered the device. One
	// session registers a token once: (session_id, token) is unique.
	SessionID string `gorm:"size:64;not null;uniqueIndex:idx_push_devices_session_token" json:"-"`
	// MailboxID is the session's account: its own mailbox, which, with every
	// account it owns or has linked, is what it lists (GET /api/v1/accounts).
	MailboxID uint   `gorm:"not null;index" json:"-"`
	Platform  string `gorm:"size:16;not null" json:"platform"`
	// Token is the push service's address for the phone. Never sent back.
	Token       string    `gorm:"size:256;not null;uniqueIndex:idx_push_devices_session_token" json:"-"`
	Environment string    `gorm:"size:16;not null" json:"environment"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"-"`
}

func (PushDevice) TableName() string { return "push_devices" }
