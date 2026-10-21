package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/restmail/restmail/internal/acme"
)

func TestACMEChecks_OneShot(t *testing.T) {
	want := errors.New("issuance failed")
	calls := 0
	err := runACMEChecks(context.Background(), 0, 0, func(context.Context) error {
		calls++
		return want
	})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("one shot: calls=%d, error=%v; want one call and %v", calls, err, want)
	}
}

func TestACMEChecks_RejectsUnsafeOptions(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		renewBefore, interval time.Duration
	}{
		{"negative interval", time.Hour, -time.Second},
		{"negative renewal window", -time.Second, 0},
		{"unconditional periodic issuance", 0, time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			err := runACMEChecks(context.Background(), tt.renewBefore, tt.interval, func(context.Context) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatalf("unsafe options reached issuer: called=%v, error=%v", called, err)
			}
		})
	}
}

// Virtual time proves failed attempts wait the full interval, slow checks
// cannot overlap or accumulate ticks, and successful checks keep scheduling.
func TestACMEChecks_PeriodicRetriesAndWaitsAfterCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		start := time.Now()
		calls := 0
		err := runACMEChecks(ctx, 720*time.Hour, 12*time.Hour, func(context.Context) error {
			wantElapsed := time.Duration(calls) * 13 * time.Hour
			if elapsed := time.Since(start); elapsed != wantElapsed {
				t.Fatalf("check %d at %s, want %s", calls+1, elapsed, wantElapsed)
			}
			calls++
			time.Sleep(time.Hour) // Slow issuance, entirely in synctest virtual time.
			if calls == 3 {
				cancel()
			}
			if calls == 1 {
				return errors.New("CA temporarily unavailable")
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("periodic checks: calls=%d, error=%v", calls, err)
		}
	})
}

func TestACMEChecks_Cancellation(t *testing.T) {
	for _, phase := range []string{"before check", "during check", "between checks"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if phase == "before check" {
					cancel()
				} else {
					go func() {
						time.Sleep(time.Minute)
						cancel()
					}()
				}
				calls := 0
				start := time.Now()
				err := runACMEChecks(ctx, 720*time.Hour, 12*time.Hour, func(ctx context.Context) error {
					calls++
					if phase == "during check" {
						<-ctx.Done()
						return ctx.Err()
					}
					return nil
				})
				wantCalls, wantElapsed := 1, time.Minute
				if phase == "before check" {
					wantCalls, wantElapsed = 0, 0
				}
				if err != nil || calls != wantCalls || time.Since(start) != wantElapsed {
					t.Fatalf("cancellation: calls=%d, elapsed=%s, error=%v", calls, time.Since(start), err)
				}
			})
		})
	}
}

func TestACMEChecks_ReusedPairReleasesLockBetweenChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		const domain = "mail.example.test"
		now := time.Now()
		cert, key := renewalTestPair(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), domain)
		if err := writeServingPair(dir, domain, cert, key); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		calls := 0
		go func() {
			done <- runACMEChecks(ctx, acme.RenewalWindow, 12*time.Hour, func(ctx context.Context) error {
				calls++
				return renewACME(ctx, dir, "invalid listen address", acme.IssueConfig{Domains: []string{domain}}, acme.RenewalWindow)
			})
		}()
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("initial check calls=%d, want 1", calls)
		}
		// A manual issuer can acquire the same volume lock while the periodic
		// runner waits. Reuse must not have opened the invalid listener above.
		lock, err := lockACMEIssuer(dir)
		if err != nil {
			t.Fatalf("periodic runner retained lock between checks: %v", err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
