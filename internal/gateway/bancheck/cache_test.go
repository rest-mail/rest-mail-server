package bancheck

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestCachedChecker_ReturnsLookupResult proves the wrapper passes a successful
// lookup's verdict straight through.
func TestCachedChecker_ReturnsLookupResult(t *testing.T) {
	banned := newCachedChecker(func(ip, proto string) (bool, error) { return true, nil }, time.Minute)
	if !banned("1.2.3.4", "smtp") {
		t.Error("expected banned=true to pass through")
	}
	clean := newCachedChecker(func(ip, proto string) (bool, error) { return false, nil }, time.Minute)
	if clean("1.2.3.4", "smtp") {
		t.Error("expected banned=false to pass through")
	}
}

// TestCachedChecker_CachesWithinTTL proves a repeated check for the same ip/proto
// within the TTL is served from cache, collapsing the per-connection DB hit.
func TestCachedChecker_CachesWithinTTL(t *testing.T) {
	var calls atomic.Int32
	checker := newCachedChecker(func(ip, proto string) (bool, error) {
		calls.Add(1)
		return false, nil
	}, time.Minute)

	for i := 0; i < 5; i++ {
		checker("1.2.3.4", "smtp")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("lookup called %d times, want 1 (subsequent checks must hit cache)", got)
	}
	// A different key is a distinct lookup.
	checker("5.6.7.8", "smtp")
	if got := calls.Load(); got != 2 {
		t.Errorf("lookup called %d times after a new key, want 2", got)
	}
}

// TestCachedChecker_FailsClosedOnErrorWithNoCache is the core of the fix: on a DB
// error for an ip never seen before, the checker must fail CLOSED (report banned)
// rather than fail open. A fail-open implementation returns false here and would
// silently admit a banned attacker during a DB outage.
func TestCachedChecker_FailsClosedOnErrorWithNoCache(t *testing.T) {
	checker := newCachedChecker(func(ip, proto string) (bool, error) {
		return false, errors.New("db down")
	}, time.Minute)
	if !checker("1.2.3.4", "smtp") {
		t.Error("expected fail-closed (banned=true) on DB error with no cached result")
	}
}

// TestCachedChecker_ServesLastKnownOnError proves that when a prior result is
// cached, a later DB error serves that last-known verdict rather than failing
// closed — so a transient blip does not lock out a known-good IP (nor un-ban a
// known-bad one). ttl=0 forces every call to re-query, isolating the error path.
func TestCachedChecker_ServesLastKnownOnError(t *testing.T) {
	var fail atomic.Bool
	checker := newCachedChecker(func(ip, proto string) (bool, error) {
		if fail.Load() {
			return false, errors.New("db down")
		}
		return false, nil // known-good
	}, 0)

	if checker("1.2.3.4", "smtp") {
		t.Fatal("precondition: first lookup should report not-banned")
	}
	fail.Store(true)
	if checker("1.2.3.4", "smtp") {
		t.Error("expected last-known not-banned to be served on DB error, not fail-closed")
	}
}
