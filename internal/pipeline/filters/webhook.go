package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

type webhookFilter struct {
	cfg pipeline.WebhookCfg
}

func init() {
	pipeline.DefaultRegistry.Register("webhook", NewWebhook)
}

func NewWebhook(config []byte) (pipeline.Filter, error) {
	var cfg pipeline.WebhookCfg
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("webhook URL is required")
	}
	if cfg.Method == "" {
		cfg.Method = "POST"
	}
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = 5000
	}
	return &webhookFilter{cfg: cfg}, nil
}

func (f *webhookFilter) Name() string             { return "webhook" }
func (f *webhookFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *webhookFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// Build the payload
	payload, err := json.Marshal(email)
	if err != nil {
		return nil, fmt.Errorf("marshal email: %w", err)
	}

	// Send the webhook
	client := &http.Client{
		Timeout: time.Duration(f.cfg.TimeoutMS) * time.Millisecond,
	}

	req, err := http.NewRequestWithContext(ctx, f.cfg.Method, f.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range f.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue, // Don't block email delivery on webhook failure
			Log: pipeline.FilterLog{
				Filter: "webhook",
				Result: "error",
				Detail: fmt.Sprintf("webhook failed: %v", err),
			},
		}, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Parse response
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Try to parse response for action directives
		var webhookResp struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(body, &webhookResp) == nil && webhookResp.Action != "" {
			action := pipeline.Action(webhookResp.Action)
			return &pipeline.FilterResult{
				Type:   pipeline.FilterTypeAction,
				Action: action,
				Log: pipeline.FilterLog{
					Filter: "webhook",
					Result: string(action),
					Detail: fmt.Sprintf("webhook responded: %s", webhookResp.Action),
				},
			}, nil
		}

		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "webhook",
				Result: "pass",
				Detail: fmt.Sprintf("webhook returned %d", resp.StatusCode),
			},
		}, nil
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "webhook",
			Result: "error",
			Detail: fmt.Sprintf("webhook returned %d: %s", resp.StatusCode, string(body)),
		},
	}, nil
}
