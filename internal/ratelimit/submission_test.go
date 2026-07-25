package ratelimit

import (
	"testing"
)

// TestSubmissionLimiter_PerAccountKey checks the limiter counts by account key
// so linked From addresses of one account share the cap (a compromised
// credential cannot multiply its allowance by rotating authorized senders).
// Relocated from internal/gateway/smtp when the limiter was extracted into this
// shared package (#184).
func TestSubmissionLimiter_PerAccountKey(t *testing.T) {
	l := NewSubmissionLimiter(2, 100)
	if !l.Allow("acct:1") {
		t.Fatal("first under the cap should be allowed")
	}
	if !l.Allow("acct:1") {
		t.Fatal("second under the cap should be allowed")
	}
	if l.Allow("acct:1") {
		t.Fatal("third over the cap should be denied")
	}
	// A different account is independent.
	if !l.Allow("acct:2") {
		t.Fatal("a different account must have its own budget")
	}
}

// TestSubmissionLimiter_NilAllowsAll documents that a caller without a limiter
// (nil) is never throttled, so unit tests that build a bare session/handler are
// unaffected.
func TestSubmissionLimiter_NilAllowsAll(t *testing.T) {
	var l *SubmissionLimiter
	for i := 0; i < 1000; i++ {
		if !l.Allow("acct:1") {
			t.Fatal("nil limiter must allow every request")
		}
	}
}

// TestSubmissionLimiter_PerTierDisable checks a non-positive per-tier cap
// disables that tier while the other still enforces.
func TestSubmissionLimiter_PerTierDisable(t *testing.T) {
	// Minute tier disabled (0), hour tier caps at 2. Each Allow call records one
	// submission, so the two calls are distinct despite looking identical.
	l := NewSubmissionLimiter(0, 2)
	for i := 1; i <= 2; i++ {
		if !l.Allow("acct:1") {
			t.Fatalf("submission %d under the hourly cap should be allowed", i)
		}
	}
	if l.Allow("acct:1") {
		t.Fatal("third over the hourly cap should be denied even with the minute tier disabled")
	}

	// Both tiers disabled: unlimited.
	u := NewSubmissionLimiter(0, 0)
	for i := 0; i < 1000; i++ {
		if !u.Allow("acct:1") {
			t.Fatal("both tiers disabled must allow every request")
		}
	}
}
