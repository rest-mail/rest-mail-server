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
	url string
	// hmacSecret, when non-empty, requires every verdict to carry a valid
	// ScannerSignatureHeader HMAC (OSI-15); a missing/forged signature fails the
	// scan and the pipeline fails closed. Empty disables verdict verification.
	hmacSecret string
	client     *http.Client
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

// NewRspamd creates a new rspamd adapter filter from JSON configuration with no
// verdict-HMAC secret. It is the init()-registered factory; routes.go re-registers
// via NewRspamdWithSecret to bind the deployment's SCANNER_HMAC_SECRET (OSI-15).
func NewRspamd(config []byte) (pipeline.Filter, error) {
	return newRspamd(config, "")
}

// NewRspamdWithSecret returns a filter factory whose rspamd adapters
// HMAC-verify every verdict against secret (SCANNER_HMAC_SECRET). An empty
// secret leaves verdict-signature verification off; the fail-closed fallback
// still applies regardless.
func NewRspamdWithSecret(secret string) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return newRspamd(config, secret)
	}
}

func newRspamd(config []byte, hmacSecret string) (pipeline.Filter, error) {
	cfg := rspamdConfig{
		URL:       "http://rspamd:11333",
		TimeoutMS: 5000,
		// SECURE DEFAULT (OSI-15): defer, not continue. If the scanner is
		// unreachable or errors, the message is temp-failed (sender retries)
		// rather than silently delivered unscanned. An operator can restore the
		// legacy fail-open behavior with "fallback_action":"continue".
		FallbackAction: "defer",
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
		url:        strings.TrimRight(cfg.URL, "/"),
		hmacSecret: hmacSecret,
		client: &http.Client{
			Timeout: time.Duration(cfg.TimeoutMS) * time.Millisecond,
		},
	}

	return &adapterFilter{
		adapter:        adapter,
		fallbackAction: parseAction(cfg.FallbackAction, pipeline.ActionDefer),
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

	// Authenticate the verdict before trusting it (OSI-15). When a scanner secret
	// is configured, a missing/forged signature returns an error here, so
	// adapterFilter applies its fail-closed fallback instead of the verdict.
	if err := verifyScannerSignature(a.hmacSecret, resp.Header, body); err != nil {
		return nil, fmt.Errorf("rspamd: %w", err)
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
		// Unknown or missing action: fail CLOSED (OSI-15). An unrecognized or
		// empty verdict is treated as a temp-failure (defer) — never as clean —
		// so a malformed/partial scanner response cannot pass mail unscanned.
		return pipeline.ActionDefer, false
	}
}
