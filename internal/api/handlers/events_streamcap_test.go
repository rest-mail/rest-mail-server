package handlers

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestSSEBroker_PerAccountStreamCap is the #204 resource-exhaustion guard: the
// broker caps concurrent SSE streams per account (each stream is a goroutine + FD
// + channel). Filling the cap rejects further streams; releasing one frees
// exactly one slot; a different account is isolated from a saturated one.
func TestSSEBroker_PerAccountStreamCap(t *testing.T) {
	const capN = 3
	b := newSSEBroker(capN)

	const acct = uint(1)

	// Fill up to the cap.
	for i := 0; i < capN; i++ {
		if !b.AcquireStream(acct) {
			t.Fatalf("acquire %d/%d should succeed", i+1, capN)
		}
	}
	// One past the cap is rejected.
	if b.AcquireStream(acct) {
		t.Fatal("acquire beyond cap should be rejected")
	}
	// A different account is unaffected by the first account's saturation.
	if !b.AcquireStream(uint(2)) {
		t.Fatal("a different account should still get a slot")
	}
	// Releasing one slot on the saturated account frees exactly one.
	b.ReleaseStream(acct)
	if !b.AcquireStream(acct) {
		t.Fatal("release should free a slot")
	}
	if b.AcquireStream(acct) {
		t.Fatal("only one slot should have been freed")
	}
}

// TestSSEBroker_DefaultCap confirms NewSSEBroker enforces the positive default
// cap so a plain broker is bounded out of the box.
func TestSSEBroker_DefaultCap(t *testing.T) {
	if DefaultMaxSSEStreamsPerAccount <= 0 {
		t.Fatalf("default cap must be positive, got %d", DefaultMaxSSEStreamsPerAccount)
	}
	b := NewSSEBroker()
	for i := 0; i < DefaultMaxSSEStreamsPerAccount; i++ {
		if !b.AcquireStream(1) {
			t.Fatalf("default-cap broker rejected slot %d within cap", i+1)
		}
	}
	if b.AcquireStream(1) {
		t.Fatal("default-cap broker admitted a stream beyond the cap")
	}
}

// TestSSEBroker_StreamCapConcurrent proves the cap accounting is concurrency-safe:
// under many parallel acquires for one account, exactly cap grants happen, never
// more (run under -race to also catch data races).
func TestSSEBroker_StreamCapConcurrent(t *testing.T) {
	const capN = 10
	b := newSSEBroker(capN)

	var granted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.AcquireStream(1) {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()

	if g := granted.Load(); g != capN {
		t.Fatalf("granted %d slots concurrently, want exactly cap %d", g, capN)
	}
}
