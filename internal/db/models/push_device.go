package models

import "time"

// Push services a device can be registered with.
const (
	PushPlatformAPNs = "apns"
	// PushPlatformWebPush is a browser's push service (RFC 8030), reached
	// with VAPID (RFC 8292) and encrypted to the browser (RFC 8291).
	PushPlatformWebPush = "webpush"
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
	// Token is the push service's address for the device: an APNs token, or
	// a Web Push subscription's endpoint URL. Never sent back.
	Token       string `gorm:"size:1024;not null;uniqueIndex:idx_push_devices_session_token" json:"-"`
	Environment string `gorm:"size:16;not null" json:"environment"`
	// WebPushP256dh and WebPushAuth are a Web Push subscription's keys,
	// base64url: the browser's P-256 public key and its 16-byte secret, which
	// every push is encrypted to (RFC 8291). Empty for APNs. Never sent back.
	WebPushP256dh string    `gorm:"size:128;not null;default:''" json:"-"`
	WebPushAuth   string    `gorm:"size:64;not null;default:''" json:"-"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"-"`
}

func (PushDevice) TableName() string { return "push_devices" }
