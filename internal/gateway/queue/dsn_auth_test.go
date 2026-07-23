package queue

import (
	"testing"
	"time"
)

// TestBounceSenderAuthorized verifies OSI-25 sender authentication: a DSN is only
// suppressed when there is POSITIVE evidence the queue row's Sender was forged —
// i.e. the row is linked to a stored sent message that belongs to a DIFFERENT
// mailbox than the one the bounce would land in. Absent a linked message the
// bounce proceeds, so legitimate DSNs are never dropped by this check.
func TestBounceSenderAuthorized(t *testing.T) {
	cases := []struct {
		name             string
		hasMessage       bool
		messageMailboxID uint
		senderMailboxID  uint
		want             bool
	}{
		{
			name:             "linked message owned by sender mailbox -> authorized",
			hasMessage:       true,
			messageMailboxID: 7,
			senderMailboxID:  7,
			want:             true,
		},
		{
			name:             "linked message owned by a DIFFERENT mailbox -> forged, reject",
			hasMessage:       true,
			messageMailboxID: 42,
			senderMailboxID:  7,
			want:             false,
		},
		{
			name:            "no linked message -> nothing to contradict, allow (rate-limited elsewhere)",
			hasMessage:      false,
			senderMailboxID: 7,
			want:            true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bounceSenderAuthorized(c.hasMessage, c.messageMailboxID, c.senderMailboxID)
			if got != c.want {
				t.Errorf("bounceSenderAuthorized(%v, %d, %d) = %v, want %v",
					c.hasMessage, c.messageMailboxID, c.senderMailboxID, got, c.want)
			}
		})
	}
}

// TestWithinDSNRateLimit verifies OSI-25 per-recipient rate limiting: bounces are
// accepted until the mailbox reaches the cap within the window, then suppressed;
// a non-positive cap disables limiting entirely.
func TestWithinDSNRateLimit(t *testing.T) {
	cases := []struct {
		name        string
		recentCount int
		maxPerWin   int
		want        bool
	}{
		{"under cap -> allow", 4, 20, true},
		{"one below cap -> allow", 19, 20, true},
		{"at cap -> suppress", 20, 20, false},
		{"over cap -> suppress", 21, 20, false},
		{"zero cap disables limit -> allow", 100000, 0, true},
		{"negative cap disables limit -> allow", 100000, -1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := withinDSNRateLimit(c.recentCount, c.maxPerWin)
			if got != c.want {
				t.Errorf("withinDSNRateLimit(%d, %d) = %v, want %v",
					c.recentCount, c.maxPerWin, got, c.want)
			}
		})
	}
}

// TestSetBounceRateLimit verifies the setter's contract: an explicit cap and
// window are stored, and a non-positive window falls back to the default rather
// than disabling the time bound.
func TestSetBounceRateLimit(t *testing.T) {
	w := &Worker{bounceMax: defaultBounceMaxPerRecipient, bounceWindow: defaultBounceWindow}

	w.SetBounceRateLimit(5, 10*time.Minute)
	if w.bounceMax != 5 || w.bounceWindow != 10*time.Minute {
		t.Errorf("SetBounceRateLimit(5, 10m) -> max=%d window=%s", w.bounceMax, w.bounceWindow)
	}

	w.SetBounceRateLimit(0, 0)
	if w.bounceMax != 0 {
		t.Errorf("cap not stored: max=%d, want 0 (limit disabled)", w.bounceMax)
	}
	if w.bounceWindow != defaultBounceWindow {
		t.Errorf("non-positive window not defaulted: window=%s, want %s", w.bounceWindow, defaultBounceWindow)
	}
}
