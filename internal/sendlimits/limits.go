// Package sendlimits accounts for submissions across protocol processes and
// replicas. Counters survive restarts; a transaction-level advisory lock makes
// checking every layer and reserving all of them one atomic operation.
package sendlimits

import (
	"context"
	"github.com/restmail/restmail/internal/db/models"
	"time"
)

type humanReleaseKey struct{}

// WithHumanRelease marks an internally authorized human-2FA release. It is a
// context marker, never EmailJSON metadata a caller or transform could forge.
// Only per-account pipeline rate limits honor it; domain/server accounting
// remains in Reserve and still charges the deliberately released submission.
func WithHumanRelease(ctx context.Context) context.Context {
	return context.WithValue(ctx, humanReleaseKey{}, true)
}
func IsHumanRelease(ctx context.Context) bool {
	allowed, _ := ctx.Value(humanReleaseKey{}).(bool)
	return allowed
}

func values(v models.SendLimitValues) [4]int64 {
	return [4]int64{v.MessagesPerHour, v.MessagesPerDay, v.RecipientsPerHour, v.RecipientsPerDay}
}
func fromValues(v [4]int64) models.SendLimitValues {
	return models.SendLimitValues{MessagesPerHour: v[0], MessagesPerDay: v[1], RecipientsPerHour: v[2], RecipientsPerDay: v[3]}
}

// Inherit applies the live parent ceiling even to an old override that became
// too high after its parent's limit was reduced.
func Inherit(parent models.SendLimitValues, override *models.SendLimitValues) models.SendLimitValues {
	if override == nil {
		return parent
	}
	p, c := values(parent), values(*override)
	for i := range p {
		if p[i] > 0 && (c[i] == 0 || c[i] > p[i]) {
			c[i] = p[i]
		}
	}
	return fromValues(c)
}
func Within(parent, child models.SendLimitValues) bool {
	p, c := values(parent), values(child)
	for i := range p {
		if c[i] < 0 || (p[i] > 0 && (c[i] == 0 || c[i] > p[i])) {
			return false
		}
	}
	return true
}
func Exceeded(cap models.SendLimitValues, hour, day models.SendLimitCounter, recipients int64) string {
	limits := values(cap)
	used := [4]int64{hour.Messages, day.Messages, hour.Recipients, day.Recipients}
	added := [4]int64{1, 1, recipients, recipients}
	names := [4]string{"messages_per_hour", "messages_per_day", "recipients_per_hour", "recipients_per_day"}
	for i, n := range limits {
		if n > 0 && (added[i] > n || used[i] > n-added[i]) {
			return names[i]
		}
	}
	return ""
}
func Bucket(now time.Time, window time.Duration) time.Time { return now.UTC().Truncate(window) }
