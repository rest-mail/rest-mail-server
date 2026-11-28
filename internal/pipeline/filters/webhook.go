package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	rmconfig "github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

type webhookFilter struct {
	cfg    pipeline.WebhookCfg
	client *http.Client
	// maxRequestBytes is the largest JSON request sent; larger is refused.
	maxRequestBytes int64
}

func init() {
	pipeline.DefaultRegistry.Register("webhook", NewWebhook)
}

func NewWebhook(config []byte) (pipeline.Filter, error) {
	return newWebhook(config, rmconfig.DefaultSMTPMaxMessageSize*rmconfig.WebhookRequestExpansionFactor)
}

// NewWebhookWithLimit returns a filter factory whose webhooks refuse a request
// over maxRequestBytes (WEBHOOK_MAX_REQUEST_BYTES) without sending it.
func NewWebhookWithLimit(maxRequestBytes int64) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return newWebhook(config, maxRequestBytes)
	}
}

func newWebhook(config []byte, maxRequestBytes int64) (pipeline.Filter, error) {
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
	return &webhookFilter{
		cfg:    cfg,
		client: newGuardedHTTPClient(filterHTTPTimeout(cfg.TimeoutMS, 5*time.Second, 0)),

		maxRequestBytes: maxRequestBytes,
	}, nil
}

func (f *webhookFilter) Name() string              { return "webhook" }
func (f *webhookFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

// failed is the outcome of a webhook that could not be called.
func (f *webhookFilter) failed(err error) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue, // Don't block email delivery on webhook failure
		Log: pipeline.FilterLog{
			Filter: "webhook",
			Result: "error",
			Detail: fmt.Sprintf("webhook failed: %v", err),
		},
	}
}

func (f *webhookFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// Build the payload: a part handed in by reference is sent as content, as it always was.
	email, err := withReferencedContent(ctx, email, f.maxRequestBytes)
	if errors.Is(err, errOverRequestLimit) {
		return f.failed(fmt.Errorf("request body is over the limit of %d bytes", f.maxRequestBytes)), nil
	}
	if err != nil {
		return f.failed(err), nil
	}
	payload, err := json.Marshal(email)
	if err != nil {
		return nil, fmt.Errorf("marshal email: %w", err)
	}

	// A request over the cap is refused before any of it is sent, as the
	// failure a webhook that cannot be reached already is.
	if int64(len(payload)) > f.maxRequestBytes {
		return f.failed(fmt.Errorf("request body of %d bytes is over the limit of %d bytes", len(payload), f.maxRequestBytes)), nil
	}

	// Send the webhook. The URL is operator-supplied (pipelines:write), so the
	// client refuses loopback/link-local targets to blunt SSRF (see ssrf.go).
	req, err := http.NewRequestWithContext(ctx, f.cfg.Method, f.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range f.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return f.failed(err), nil
	}
	defer resp.Body.Close()

	body, err := readFilterResponse(resp, webhookResponseBytes)
	if err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "webhook",
				Result: "error",
				Detail: fmt.Sprintf("read webhook response: %v", err),
			},
		}, nil
	}

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
