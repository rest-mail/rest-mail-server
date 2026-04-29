package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

type greylistConfig struct {
	DelayMinutes        int  `json:"delay_minutes"`
	WhitelistAfterPass  bool `json:"whitelist_after_pass"`
	SkipIfTrustedSender bool `json:"skip_if_trusted_sender"`
}

type greylistFilter struct {
	db  *gorm.DB
	cfg greylistConfig
}

func NewGreylist(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		cfg := greylistConfig{
			DelayMinutes:        5,
			WhitelistAfterPass:  true,
			SkipIfTrustedSender: true,
		}
		if len(config) > 0 {
			_ = json.Unmarshal(config, &cfg)
		}
		return &greylistFilter{db: db, cfg: cfg}, nil
	}
}

func (f *greylistFilter) Name() string             { return "greylist" }
func (f *greylistFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

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

	// Check for existing greylist entry
	var entry struct {
		ID         uint
		Sender     string
		Recipient  string
		SourceIP   string
		FirstSeen  time.Time
		RetryAfter time.Time
		Passed     bool
	}

	err := f.db.Table("greylist_entries").
		Where("sender = ? AND recipient = ? AND source_ip = ?", sender, rcpt, clientIP).
		First(&entry).Error

	now := time.Now()

	if err == gorm.ErrRecordNotFound {
		// First time seeing this triple — create entry and defer
		retryAfter := now.Add(time.Duration(f.cfg.DelayMinutes) * time.Minute)
		f.db.Table("greylist_entries").Create(map[string]interface{}{
			"sender":      sender,
			"recipient":   rcpt,
			"source_ip":   clientIP,
			"first_seen":  now,
			"retry_after": retryAfter,
			"passed":      false,
			"created_at":  now,
		})

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

	if err != nil {
		return continueResult("greylist", fmt.Sprintf("db error: %v", err)), nil
	}

	// Entry exists — check if enough time has passed
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

	// Enough time has passed — allow through and mark as passed
	f.db.Table("greylist_entries").
		Where("id = ?", entry.ID).
		Update("passed", true)

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
