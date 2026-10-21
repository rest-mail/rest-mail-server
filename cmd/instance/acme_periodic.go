package main

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// runACMEChecks runs once by default, or checks immediately and then waits the
// full interval after each completed attempt. Failures in periodic mode are
// logged and retried on the next check, never in a tight loop. Each invocation
// of check owns its issuance timeout and releases the volume lock when done.
func runACMEChecks(ctx context.Context, renewBefore, interval time.Duration, check func(context.Context) error) error {
	if interval < 0 {
		return errors.New("-check-interval must not be negative")
	}
	if renewBefore < 0 {
		return errors.New("-renew-before must not be negative")
	}
	if interval > 0 && renewBefore == 0 {
		return errors.New("periodic checks require a positive -renew-before to avoid unnecessary CA orders")
	}
	if interval > 0 {
		slog.Info("acme: periodic certificate checks enabled", "interval", interval, "renew_before", renewBefore)
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := check(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if interval == 0 {
			return err
		}
		if err != nil {
			slog.Error("acme: periodic certificate check failed; retrying after interval", "interval", interval, "error", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
