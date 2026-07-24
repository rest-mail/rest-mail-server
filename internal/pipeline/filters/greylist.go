package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type greylistConfig struct {
	DelayMinutes int `json:"delay_minutes"`
	// TTLDays is the entry lifetime: how long an auto-whitelisted (passed) triple
	// is honored before it is reconsidered, and how long an un-retried entry
	// lives before the purge worker collects it. Defaults to
	// models.DefaultGreylistTTLDays when omitted or non-positive.
	TTLDays int `json:"ttl_days"`
	// WhitelistAfterPass controls what happens once the greylist delay is
	// satisfied. When true (default, standard greylisting) the triple is
	// auto-whitelisted until the TTL horizon so subsequent messages pass without
	// waiting. When false (strict mode) the pass is not persisted: the entry is
	// dropped so a later message from the same triple is greylisted afresh.
	WhitelistAfterPass bool `json:"whitelist_after_pass"`
}

type greylistFilter struct {
	db  *gorm.DB
	cfg greylistConfig
}

func NewGreylist(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		cfg := greylistConfig{
			DelayMinutes:       5,
			TTLDays:            models.DefaultGreylistTTLDays,
			WhitelistAfterPass: true,
		}
		if len(config) > 0 {
			_ = json.Unmarshal(config, &cfg)
		}
		if cfg.TTLDays <= 0 {
			cfg.TTLDays = models.DefaultGreylistTTLDays
		}
		return &greylistFilter{db: db, cfg: cfg}, nil
	}
}

func (f *greylistFilter) Name() string              { return "greylist" }
func (f *greylistFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

// ttl is the configured entry lifetime as a duration.
func (f *greylistFilter) ttl() time.Duration {
	return time.Duration(f.cfg.TTLDays) * 24 * time.Hour
}

func (f *greylistFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	sender := email.Envelope.MailFrom
	clientIP := email.Envelope.ClientIP

	if sender == "" || clientIP == "" {
		return continueResult("greylist", "no sender or client IP"), nil
	}

	rcpt := ""
	if len(email.Envelope.RcptTo) > 0 {
		rcpt = email.Envelope.RcptTo[0]
	}

	// Check for an existing greylist entry for this triple.
	var entry struct {
		ID         uint
		Sender     string
		Recipient  string
		SourceIP   string
		FirstSeen  time.Time
		RetryAfter time.Time
		Passed     bool
		ExpiresAt  *time.Time
	}

	err := f.db.Table("greylist_entries").
		Where("sender = ? AND recipient = ? AND source_ip = ?", sender, rcpt, clientIP).
		First(&entry).Error

	now := time.Now()

	if err == gorm.ErrRecordNotFound {
		// First time seeing this triple — record it (with its TTL horizon) and defer.
		return f.deferNew(sender, rcpt, clientIP, now)
	}
	if err != nil {
		// Surface the lookup error rather than swallowing it: the pipeline engine
		// fails closed (defers) on a filter error by default, so a database problem
		// temp-fails the message instead of letting it silently bypass greylisting.
		return nil, fmt.Errorf("greylist lookup: %w", err)
	}

	// A stale entry past its TTL horizon (the purge worker has not collected it
	// yet) is treated as a brand-new triple: re-greylist so an auto-whitelist is
	// reconsidered rather than honored indefinitely.
	if entry.ExpiresAt != nil && now.After(*entry.ExpiresAt) {
		return f.reGreylist(entry.ID, now, "whitelist expired")
	}

	if entry.Passed {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "greylist",
				Result: "pass",
				Detail: "previously passed greylisting",
			},
		}, nil
	}

	if now.Before(entry.RetryAfter) {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionDefer,
			Log: pipeline.FilterLog{
				Filter: "greylist",
				Result: "defer",
				Detail: fmt.Sprintf("retry too early, wait until %s", entry.RetryAfter.Format(time.RFC3339)),
			},
		}, nil
	}

	// Enough time has passed — allow through.
	if f.cfg.WhitelistAfterPass {
		// Auto-whitelist until the TTL horizon (not forever): mark passed and
		// (re)stamp the expiry so the triple is reconsidered after ttl_days.
		expires := now.Add(f.ttl())
		if err := f.db.Table("greylist_entries").
			Where("id = ?", entry.ID).
			Updates(map[string]interface{}{
				"passed":     true,
				"expires_at": expires,
			}).Error; err != nil {
			return nil, fmt.Errorf("greylist mark passed: %w", err)
		}
	} else {
		// Strict mode: do not persist a whitelist. Drop the entry so a later
		// message from this triple is greylisted afresh.
		if err := f.db.Table("greylist_entries").
			Where("id = ?", entry.ID).
			Delete(&models.GreylistEntry{}).Error; err != nil {
			return nil, fmt.Errorf("greylist clear entry: %w", err)
		}
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "greylist",
			Result: "pass",
			Detail: "greylist delay satisfied",
		},
	}, nil
}

// deferNew records a first-contact entry (stamped with its TTL horizon) and
// defers the message.
func (f *greylistFilter) deferNew(sender, rcpt, clientIP string, now time.Time) (*pipeline.FilterResult, error) {
	retryAfter := now.Add(time.Duration(f.cfg.DelayMinutes) * time.Minute)
	expires := now.Add(f.ttl())
	if err := f.db.Table("greylist_entries").Create(map[string]interface{}{
		"sender":      sender,
		"recipient":   rcpt,
		"source_ip":   clientIP,
		"first_seen":  now,
		"retry_after": retryAfter,
		"passed":      false,
		"expires_at":  expires,
		"created_at":  now,
	}).Error; err != nil {
		return nil, fmt.Errorf("greylist create entry: %w", err)
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionDefer,
		Log: pipeline.FilterLog{
			Filter: "greylist",
			Result: "defer",
			Detail: fmt.Sprintf("first attempt, retry after %s", retryAfter.Format(time.RFC3339)),
		},
	}, nil
}

// reGreylist resets an existing (stale) entry back to an un-passed challenge and
// defers, so an expired auto-whitelist is reconsidered from scratch.
func (f *greylistFilter) reGreylist(id uint, now time.Time, reason string) (*pipeline.FilterResult, error) {
	retryAfter := now.Add(time.Duration(f.cfg.DelayMinutes) * time.Minute)
	expires := now.Add(f.ttl())
	if err := f.db.Table("greylist_entries").
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"first_seen":  now,
			"retry_after": retryAfter,
			"passed":      false,
			"expires_at":  expires,
		}).Error; err != nil {
		return nil, fmt.Errorf("greylist re-greylist entry: %w", err)
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionDefer,
		Log: pipeline.FilterLog{
			Filter: "greylist",
			Result: "defer",
			Detail: fmt.Sprintf("%s, re-greylisting, retry after %s", reason, retryAfter.Format(time.RFC3339)),
		},
	}, nil
}
