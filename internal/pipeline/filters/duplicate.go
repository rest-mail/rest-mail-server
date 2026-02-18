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
	WebhookURL     string            `json:"webhook_url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	TimeoutMS      int               `json:"timeout_ms"`
	QueueRecipient string            `json:"queue_recipient"`
}

type duplicateFilter struct {
	webhookURL     string
	method         string
	headers        map[string]string
	timeout        time.Duration
	queueRecipient string
}

func init() {
	pipeline.DefaultRegistry.Register("duplicate", NewDuplicate)
}

// NewDuplicate creates a duplicate filter from its JSON configuration.
// The filter forks a copy of the email to a webhook URL and/or marks it
// for delivery to an additional queue recipient.
func NewDuplicate(config []byte) (pipeline.Filter, error) {
	var cfg duplicateConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("duplicate: invalid config: %w", err)
		}
	}

	if cfg.WebhookURL == "" && cfg.QueueRecipient == "" {
		return nil, fmt.Errorf("duplicate: at least one of webhook_url or queue_recipient is required")
	}

	if cfg.Method == "" {
		cfg.Method = "POST"
	}
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = 5000
	}

	return &duplicateFilter{
		webhookURL:     cfg.WebhookURL,
		method:         cfg.Method,
		headers:        cfg.Headers,
		timeout:        time.Duration(cfg.TimeoutMS) * time.Millisecond,
		queueRecipient: cfg.QueueRecipient,
	}, nil
}

func (f *duplicateFilter) Name() string             { return "duplicate" }
func (f *duplicateFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *duplicateFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	var destinations []string

	// Fire-and-forget webhook delivery in a goroutine
	if f.webhookURL != "" {
		destinations = append(destinations, "webhook:"+f.webhookURL)

		payload, err := json.Marshal(email)
		if err != nil {
			slog.Error("duplicate: failed to marshal email for webhook", "error", err)
		} else {
			go f.sendWebhook(payload)
		}
	}

	// Store queue recipient metadata for later processing
	if f.queueRecipient != "" {
		destinations = append(destinations, "queue:"+f.queueRecipient)

		if email.Metadata == nil {
			email.Metadata = make(map[string]string)
		}
		email.Metadata["duplicate_queue_recipient"] = f.queueRecipient

		slog.Info("duplicate: queued copy for additional recipient",
			"recipient", f.queueRecipient,
			"from", email.Envelope.MailFrom,
		)
	}

	detail := fmt.Sprintf("duplicated to: %v", destinations)

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
	client := &http.Client{
		Timeout: f.timeout,
	}

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
