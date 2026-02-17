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

type rateLimitFilter struct {
	cfg     rateLimitConfig
	mu      sync.Mutex
	buckets map[string]*rateBucket
}

type rateBucket struct {
	count    int
	resetAt  time.Time
}

func init() {
	pipeline.DefaultRegistry.Register("rate_limit", NewRateLimit)
}

func NewRateLimit(config []byte) (pipeline.Filter, error) {
	cfg := rateLimitConfig{
		MaxPerHour:   100,
		MaxPerMinute: 20,
	}
	if len(config) > 0 {
		json.Unmarshal(config, &cfg)
	}
	return &rateLimitFilter{
		cfg:     cfg,
		buckets: make(map[string]*rateBucket),
	}, nil
}

func (f *rateLimitFilter) Name() string             { return "rate_limit" }
func (f *rateLimitFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *rateLimitFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	sender := email.Envelope.MailFrom
	if sender == "" && len(email.Headers.From) > 0 {
		sender = email.Headers.From[0].Address
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	key := "min:" + sender
	bucket, ok := f.buckets[key]
	if !ok || now.After(bucket.resetAt) {
		f.buckets[key] = &rateBucket{count: 1, resetAt: now.Add(time.Minute)}
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
					Detail: fmt.Sprintf("sender %s exceeded %d/min limit", sender, f.cfg.MaxPerMinute),
				},
			}, nil
		}
	}

	hourKey := "hour:" + sender
	hBucket, ok := f.buckets[hourKey]
	if !ok || now.After(hBucket.resetAt) {
		f.buckets[hourKey] = &rateBucket{count: 1, resetAt: now.Add(time.Hour)}
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
					Detail: fmt.Sprintf("sender %s exceeded %d/hour limit", sender, f.cfg.MaxPerHour),
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
