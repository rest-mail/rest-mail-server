package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
)

func init() {
	pipeline.DefaultRegistry.Register("rspamd", NewRspamd)
}

// rspamdConfig holds the JSON configuration for the rspamd filter.
type rspamdConfig struct {
	URL            string `json:"url"`
	TimeoutMS      int    `json:"timeout_ms"`
	FallbackAction string `json:"fallback_action"`
}

// rspamdAdapter communicates with an rspamd instance over HTTP.
type rspamdAdapter struct {
	url    string
	client *http.Client
}

// rspamdResponse represents the relevant fields from rspamd's /checkv2 JSON response.
type rspamdResponse struct {
	Action        string                   `json:"action"`
	Score         float64                  `json:"score"`
	RequiredScore float64                  `json:"required_score"`
	Symbols       map[string]rspamdSymbol  `json:"symbols"`
	MessageID     string                   `json:"message-id"`
}

// rspamdSymbol represents a single symbol returned by rspamd.
type rspamdSymbol struct {
	Name        string  `json:"name"`
	Score       float64 `json:"score"`
	Description string  `json:"description"`
}

// NewRspamd creates a new rspamd adapter filter from JSON configuration.
func NewRspamd(config []byte) (pipeline.Filter, error) {
	cfg := rspamdConfig{
		URL:            "http://rspamd:11333",
		TimeoutMS:      5000,
		FallbackAction: "continue",
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("rspamd: invalid config: %w", err)
		}
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("rspamd: url is required")
	}
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = 5000
	}

	adapter := &rspamdAdapter{
		url: strings.TrimRight(cfg.URL, "/"),
		client: &http.Client{
			Timeout: time.Duration(cfg.TimeoutMS) * time.Millisecond,
		},
	}

	return &adapterFilter{
		adapter:        adapter,
		fallbackAction: parseAction(cfg.FallbackAction, pipeline.ActionContinue),
	}, nil
}

func (a *rspamdAdapter) Name() string { return "rspamd" }

// Healthy checks if rspamd is reachable by hitting the /ping endpoint.
func (a *rspamdAdapter) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url+"/ping", nil)
	if err != nil {
		return false
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Scan sends the email to rspamd's /checkv2 endpoint and parses the result.
func (a *rspamdAdapter) Scan(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.AdapterResult, error) {
	// Serialize the email to raw RFC 2822 format for rspamd.
	rawMsg, err := rmime.Serialize(email)
	if err != nil {
		return nil, fmt.Errorf("rspamd: serialize email: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url+"/checkv2", bytes.NewReader(rawMsg))
	if err != nil {
		return nil, fmt.Errorf("rspamd: create request: %w", err)
	}

	// Set rspamd-specific headers for better analysis.
	if email.Envelope.ClientIP != "" {
		req.Header.Set("IP", email.Envelope.ClientIP)
	}
	if email.Envelope.Helo != "" {
		req.Header.Set("Helo", email.Envelope.Helo)
	}
	if email.Envelope.MailFrom != "" {
		req.Header.Set("From", email.Envelope.MailFrom)
	}
	for _, rcpt := range email.Envelope.RcptTo {
		req.Header.Add("Rcpt", rcpt)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rspamd: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rspamd: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rspamd: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var rspamdResp rspamdResponse
	if err := json.Unmarshal(body, &rspamdResp); err != nil {
		return nil, fmt.Errorf("rspamd: parse response: %w", err)
	}

	// Map rspamd action to pipeline action.
	action, clean := mapRspamdAction(rspamdResp.Action)

	// Build result headers.
	headers := make(map[string]string)
	if clean {
		headers["X-Spam-Status"] = fmt.Sprintf("No, score=%.1f required=%.1f", rspamdResp.Score, rspamdResp.RequiredScore)
	} else {
		headers["X-Spam-Status"] = fmt.Sprintf("Yes, score=%.1f required=%.1f", rspamdResp.Score, rspamdResp.RequiredScore)
	}
	headers["X-Spam-Score"] = fmt.Sprintf("%.1f", rspamdResp.Score)

	// Build a summary of triggered symbols for X-Spamd-Result.
	symbolNames := make([]string, 0, len(rspamdResp.Symbols))
	for name, sym := range rspamdResp.Symbols {
		symbolNames = append(symbolNames, fmt.Sprintf("%s(%.1f)", name, sym.Score))
	}
	headers["X-Spamd-Result"] = fmt.Sprintf("action=%s; score=%.1f/%.1f; %s",
		rspamdResp.Action, rspamdResp.Score, rspamdResp.RequiredScore,
		strings.Join(symbolNames, " "))

	detail := fmt.Sprintf("rspamd action=%s score=%.1f/%.1f symbols=%d",
		rspamdResp.Action, rspamdResp.Score, rspamdResp.RequiredScore, len(rspamdResp.Symbols))

	result := &pipeline.AdapterResult{
		Clean:   clean,
		Score:   rspamdResp.Score,
		Action:  action,
		Details: detail,
		Headers: headers,
	}

	if action == pipeline.ActionReject {
		result.RejectMsg = fmt.Sprintf("550 Message rejected: spam score %.1f exceeds threshold %.1f",
			rspamdResp.Score, rspamdResp.RequiredScore)
	}

	return result, nil
}

// mapRspamdAction converts an rspamd action string to a pipeline Action and
// a boolean indicating whether the message is considered clean.
func mapRspamdAction(rspamdAction string) (pipeline.Action, bool) {
	switch rspamdAction {
	case "reject":
		return pipeline.ActionReject, false
	case "greylist":
		return pipeline.ActionDefer, false
	case "add header", "rewrite subject":
		// Message is spam but not rejected; add headers and continue delivery.
		return pipeline.ActionContinue, false
	case "soft reject":
		return pipeline.ActionDefer, false
	case "no action":
		return pipeline.ActionContinue, true
	default:
		// Unknown action: continue to be safe.
		return pipeline.ActionContinue, true
	}
}
