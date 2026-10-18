package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

type duplicateConfig struct {
	WebhookURL string            `json:"webhook_url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	TimeoutMS  int               `json:"timeout_ms"`
}

type duplicateFilter struct {
	webhookURL string
	method     string
	headers    map[string]string
	timeout    time.Duration
}

func init() {
	pipeline.DefaultRegistry.Register("duplicate", NewDuplicate)
}

// NewDuplicate creates a duplicate filter from its JSON configuration.
// The filter forks a copy of the email to a webhook URL.
func NewDuplicate(config []byte) (pipeline.Filter, error) {
	var cfg duplicateConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("duplicate: invalid config: %w", err)
		}
	}

	if cfg.WebhookURL == "" {
		return nil, fmt.Errorf("duplicate: webhook_url is required")
	}

	if cfg.Method == "" {
		cfg.Method = "POST"
	}
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = 5000
	}

	return &duplicateFilter{
		webhookURL: cfg.WebhookURL,
		method:     cfg.Method,
		headers:    cfg.Headers,
		timeout:    time.Duration(cfg.TimeoutMS) * time.Millisecond,
	}, nil
}

func (f *duplicateFilter) Name() string             { return "duplicate" }
func (f *duplicateFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *duplicateFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// Fire-and-forget webhook delivery in a goroutine
	payload, err := json.Marshal(email)
	if err != nil {
		slog.Error("duplicate: failed to marshal email for webhook", "error", err)
	} else {
		go f.sendWebhook(payload)
	}

	detail := fmt.Sprintf("duplicated to: %v", []string{"webhook:" + f.webhookURL})

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "duplicate",
			Result: "pass",
			Detail: detail,
		},
	}, nil
}

// sendWebhook posts the email JSON payload to the configured webhook URL.
// It runs in a goroutine and logs errors instead of returning them.
func (f *duplicateFilter) sendWebhook(payload []byte) {
	// The webhook URL is operator-supplied (pipelines:write), so the client
	// refuses loopback/link-local targets to blunt SSRF (see ssrf.go).
	client := newGuardedHTTPClient(f.timeout)

	req, err := http.NewRequestWithContext(context.Background(), f.method, f.webhookURL, bytes.NewReader(payload))
	if err != nil {
		slog.Error("duplicate: failed to create webhook request", "url", f.webhookURL, "error", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range f.headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("duplicate: webhook request failed", "url", f.webhookURL, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("duplicate: webhook delivered successfully", "url", f.webhookURL, "status", resp.StatusCode)
	} else {
		slog.Warn("duplicate: webhook returned non-2xx status", "url", f.webhookURL, "status", resp.StatusCode)
	}
}
