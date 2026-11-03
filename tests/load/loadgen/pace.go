package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errSaturated is recorded when an operation is due but maxInflight are still
// running: the target was not reachable, and the run says so rather than
// quietly sending less.
var errSaturated = errors.New("generator saturated: too many operations in flight")

// paced runs op at rate per second for duration, open loop: operation i is due
// at start + i/rate whether or not earlier ones have finished, so a slow server
// shows up as latency and errors, not as a lower offered rate. It returns the
// time from the first operation until the last one finished.
func paced(ctx context.Context, rate float64, duration time.Duration, maxInflight int, rec *recorder, op func(context.Context) error) time.Duration {
	if rate <= 0 || duration <= 0 {
		return 0
	}
	total := int64(rate * duration.Seconds())
	interval := time.Duration(float64(time.Second) / rate)
	sem := make(chan struct{}, maxInflight)
	var wg sync.WaitGroup
	start := time.Now()
	for i := int64(0); i < total; i++ {
		due := start.Add(time.Duration(i) * interval)
		if wait := time.Until(due); wait > 0 {
			select {
			case <-ctx.Done():
				wg.Wait()
				return time.Since(start)
			case <-time.After(wait):
			}
		}
		select {
		case sem <- struct{}{}:
		default:
			rec.record(0, errSaturated)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			t0 := time.Now()
			err := op(ctx)
			rec.record(time.Since(t0), err)
		}()
	}
	wg.Wait()
	// The achieved rate is over the scheduled window, not the drain after it,
	// unless draining took longer (then the server is the bottleneck and the
	// longer time is the honest denominator).
	elapsed := time.Since(start)
	if elapsed < duration {
		elapsed = duration
	}
	return elapsed
}
