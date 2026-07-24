package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

type rateLimitConfig struct {
	MaxPerHour   int `json:"max_per_hour"`
	MaxPerMinute int `json:"max_per_minute"`
}

// rateLimitStore holds the sliding-window counters shared across every message.
// The engine builds a fresh rateLimitFilter per message (engine.go), so the
// accumulating state must live OUTSIDE that per-message instance — otherwise
// every message starts with an empty bucket map and a count of 1 and the limit
// can never be reached. A single store is created once per registration (see
// NewRateLimit) and handed to each per-message filter, exactly like the shared
// *gorm.DB the stateful greylist/contact filters close over.
type rateLimitStore struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
}

type rateBucket struct {
	count   int
	resetAt time.Time
}

type rateLimitFilter struct {
	cfg   rateLimitConfig
	store *rateLimitStore
}

func init() {
	pipeline.DefaultRegistry.Register("rate_limit", NewRateLimit())
}

// NewRateLimit returns a filter factory backed by a single shared counter store.
// Every filter instance the factory produces — one per message — references the
// same store, so counts persist across messages and the limit actually trips.
// Each instance still parses its own config, so different pipelines may set
// different thresholds against the shared counters.
func NewRateLimit() pipeline.FilterFactory {
	store := &rateLimitStore{buckets: make(map[string]*rateBucket)}
	return func(config []byte) (pipeline.Filter, error) {
		cfg := rateLimitConfig{
			MaxPerHour:   100,
			MaxPerMinute: 20,
		}
		if len(config) > 0 {
			_ = json.Unmarshal(config, &cfg)
		}
		return &rateLimitFilter{cfg: cfg, store: store}, nil
	}
}

func (f *rateLimitFilter) Name() string              { return "rate_limit" }
func (f *rateLimitFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

// rateKey returns the identity the outbound limit is counted against. It prefers
// the authenticated submitting account (set as metadata on the outbound
// submission path) because that cannot be rotated by simply changing MAIL FROM;
// it falls back to the envelope sender, then the From header, so the filter
// still functions where an authenticated account is not available.
func (f *rateLimitFilter) rateKey(email *pipeline.EmailJSON) string {
	if acct := email.Metadata["auth_account"]; acct != "" {
		return "acct:" + acct
	}
	sender := email.Envelope.MailFrom
	if sender == "" && len(email.Headers.From) > 0 {
		sender = email.Headers.From[0].Address
	}
	return "sender:" + sender
}

func (f *rateLimitFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	id := f.rateKey(email)

	f.store.mu.Lock()
	defer f.store.mu.Unlock()

	now := time.Now()

	minKey := "min:" + id
	bucket, ok := f.store.buckets[minKey]
	if !ok || now.After(bucket.resetAt) {
		f.store.buckets[minKey] = &rateBucket{count: 1, resetAt: now.Add(time.Minute)}
	} else {
		bucket.count++
		if bucket.count > f.cfg.MaxPerMinute {
			return &pipeline.FilterResult{
				Type:      pipeline.FilterTypeAction,
				Action:    pipeline.ActionDefer,
				RejectMsg: "452 Too many messages, try again later",
				Log: pipeline.FilterLog{
					Filter: "rate_limit",
					Result: "defer",
					Detail: fmt.Sprintf("%s exceeded %d/min limit", id, f.cfg.MaxPerMinute),
				},
			}, nil
		}
	}

	hourKey := "hour:" + id
	hBucket, ok := f.store.buckets[hourKey]
	if !ok || now.After(hBucket.resetAt) {
		f.store.buckets[hourKey] = &rateBucket{count: 1, resetAt: now.Add(time.Hour)}
	} else {
		hBucket.count++
		if hBucket.count > f.cfg.MaxPerHour {
			return &pipeline.FilterResult{
				Type:      pipeline.FilterTypeAction,
				Action:    pipeline.ActionDefer,
				RejectMsg: "452 Hourly sending limit exceeded",
				Log: pipeline.FilterLog{
					Filter: "rate_limit",
					Result: "defer",
					Detail: fmt.Sprintf("%s exceeded %d/hour limit", id, f.cfg.MaxPerHour),
				},
			}, nil
		}
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "rate_limit",
			Result: "pass",
		},
	}, nil
}
