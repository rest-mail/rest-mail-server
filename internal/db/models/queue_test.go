package models

import (
	"testing"
	"time"
)

// TestOutboundQueueBeforeCreateDefaults verifies the enqueue-safety net: a row
// created without ExpiresAt/MaxRetries (as the API send path did) gets a valid
// deadline and retry budget, so the worker's `expires_at > now` claim query
// can actually pick it up.
func TestOutboundQueueBeforeCreateDefaults(t *testing.T) {
	q := &OutboundQueue{Sender: "a@x.test", Recipient: "b@y.test", Domain: "y.test"}
	if err := q.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if q.ExpiresAt.IsZero() {
		t.Error("ExpiresAt still zero — worker claim query would skip this row forever")
	}
	if !q.ExpiresAt.After(time.Now().Add(QueueLifetime - time.Minute)) {
		t.Errorf("ExpiresAt = %s, want the queue's lifetime, %s, from now", q.ExpiresAt, QueueLifetime)
	}
	if q.MaxRetries != DefaultMaxRetries {
		t.Errorf("MaxRetries = %d, want %d", q.MaxRetries, DefaultMaxRetries)
	}

	// Explicit values must be preserved.
	exp := time.Now().Add(2 * time.Hour)
	q2 := &OutboundQueue{ExpiresAt: exp, MaxRetries: 5}
	if err := q2.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if !q2.ExpiresAt.Equal(exp) {
		t.Error("BeforeCreate overwrote an explicit ExpiresAt")
	}
	if q2.MaxRetries != 5 {
		t.Error("BeforeCreate overwrote an explicit MaxRetries")
	}
}
