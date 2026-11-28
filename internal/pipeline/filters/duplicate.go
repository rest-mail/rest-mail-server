package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	rmconfig "github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
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
	client     *http.Client

	maxRequestBytes int64
}

func init() {
	pipeline.DefaultRegistry.Register("duplicate", NewDuplicate)
}

// NewDuplicate creates a duplicate filter with the default request cap.
func NewDuplicate(config []byte) (pipeline.Filter, error) {
	return newDuplicate(config, rmconfig.DefaultSMTPMaxMessageSize*rmconfig.WebhookRequestExpansionFactor)
}

// NewDuplicateWithLimit returns a filter factory whose copies refuse a request
// over maxRequestBytes (WEBHOOK_MAX_REQUEST_BYTES) without sending it.
func NewDuplicateWithLimit(maxRequestBytes int64) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return newDuplicate(config, maxRequestBytes)
	}
}

// newDuplicate builds a duplicate filter from its JSON configuration.
// The filter forks a copy of the email to a webhook URL.
func newDuplicate(config []byte, maxRequestBytes int64) (pipeline.Filter, error) {
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
	timeout := filterHTTPTimeout(cfg.TimeoutMS, 5*time.Second, 0)

	return &duplicateFilter{
		webhookURL: cfg.WebhookURL,
		method:     cfg.Method,
		headers:    cfg.Headers,
		// The webhook URL is operator-supplied (pipelines:write), so the
		// client refuses loopback/link-local targets to blunt SSRF (see
		// ssrf.go). Its timeout also bounds the detached send.
		client: newGuardedHTTPClient(timeout),

		maxRequestBytes: maxRequestBytes,
	}, nil
}

func (f *duplicateFilter) Name() string              { return "duplicate" }
func (f *duplicateFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *duplicateFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// A part handed in by reference is copied as content, as it always was (#2107).
	email, err := withReferencedContent(ctx, email, f.maxRequestBytes)
	if err != nil {
		detail := fmt.Sprintf("duplicate not sent: %v", err)
		if errors.Is(err, errOverRequestLimit) {
			detail = fmt.Sprintf("duplicate not sent: request body is over the limit of %d bytes", f.maxRequestBytes)
		}
		return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{Filter: "duplicate", Result: "error", Detail: detail}}, nil
	}
	// Fire-and-forget webhook delivery in a goroutine
	payload, err := json.Marshal(email)
	if err != nil {
		slog.ErrorContext(ctx, "duplicate: failed to marshal email for webhook", "error", err)
	} else if int64(len(payload)) > f.maxRequestBytes {
		// A copy over the cap is not sent; the email still continues.
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "duplicate",
				Result: "error",
				Detail: fmt.Sprintf("duplicate not sent: request body of %d bytes is over the limit of %d bytes", len(payload), f.maxRequestBytes),
			},
		}, nil
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
	req, err := http.NewRequestWithContext(context.Background(), f.method, f.webhookURL, bytes.NewReader(payload))
	if err != nil {
		slog.Error("duplicate: failed to create webhook request", "url", f.webhookURL, "error", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range f.headers {
		req.Header.Set(k, v)
	}

	resp, err := f.client.Do(req)
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
