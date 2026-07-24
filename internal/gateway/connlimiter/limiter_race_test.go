package connlimiter

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// TestAcceptConcurrentPerIPLimit hammers Accept from many goroutines that share
// a single IP and asserts the per-IP cap is never exceeded.
//
// Before M-2 was fixed, Accept performed a check-then-increment
// (counter.Load() < max, then counter.Add(1)) that was not atomic as a unit:
// concurrent callers could each observe a sub-limit count and all increment
// past MaxPerIP (CWE-362). Each iteration uses a fresh limiter so the test
// reproduces the logical race reliably. Run with -race.
func TestAcceptConcurrentPerIPLimit(t *testing.T) {
	const (
		maxPerIP   = 10
		goroutines = 200
		iterations = 50
	)

	for iter := 0; iter < iterations; iter++ {
		l := New(Config{MaxPerIP: maxPerIP, MaxGlobal: 1_000_000})

		var accepted atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if l.Accept("10.0.0.1") {
					accepted.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		if got := accepted.Load(); got != maxPerIP {
			t.Fatalf("iter %d: per-IP accepted = %d, want exactly %d (limit breached by race)",
				iter, got, maxPerIP)
		}
	}
}

// TestAcceptConcurrentGlobalLimit hammers Accept from many goroutines that each
// use a distinct IP (so the per-IP cap never binds) and asserts the global cap
// is never exceeded under concurrency. Companion to the per-IP test for the same
// M-2 check-then-increment race on the global counter. Run with -race.
func TestAcceptConcurrentGlobalLimit(t *testing.T) {
	const (
		maxGlobal  = 50
		goroutines = 500
		iterations = 50
	)

	for iter := 0; iter < iterations; iter++ {
		l := New(Config{MaxPerIP: 1_000_000, MaxGlobal: maxGlobal})

		var accepted atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < goroutines; i++ {
			ip := fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256)
			wg.Add(1)
			go func(ip string) {
				defer wg.Done()
				<-start
				if l.Accept(ip) {
					accepted.Add(1)
				}
			}(ip)
		}
		close(start)
		wg.Wait()

		if got := accepted.Load(); got != maxGlobal {
			t.Fatalf("iter %d: global accepted = %d, want exactly %d (limit breached by race)",
				iter, got, maxGlobal)
		}
	}
}
