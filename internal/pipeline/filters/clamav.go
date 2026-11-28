package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	rmconfig "github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func init() {
	pipeline.DefaultRegistry.Register("clamav", NewClamAV)
}

// clamavConfig holds the JSON configuration for the ClamAV filter.
type clamavConfig struct {
	URL            string `json:"url"`
	TimeoutMS      int    `json:"timeout_ms"`
	FallbackAction string `json:"fallback_action"`
}

// clamavAdapter communicates with a ClamAV REST service over HTTP.
type clamavAdapter struct {
	url string
	// hmacSecret, when non-empty, requires every verdict to carry a valid
	// ScannerSignatureHeader HMAC (OSI-15); a missing/forged signature fails the
	// scan and the pipeline fails closed. Empty disables verdict verification.
	hmacSecret string
	client     *http.Client
	// maxRequestBytes is the largest message sent; larger is refused.
	maxRequestBytes int64
}

// clamavResponse represents the JSON response from a ClamAV REST scan endpoint.
type clamavResponse struct {
	Status      string `json:"status"`      // "OK" or "FOUND"
	Description string `json:"description"` // virus name if found
}

// NewClamAV creates a new ClamAV adapter filter from JSON configuration with no
// verdict-HMAC secret. It is the init()-registered factory; routes.go re-registers
// via NewClamAVWithSecret to bind the deployment's SCANNER_HMAC_SECRET (OSI-15).
func NewClamAV(config []byte) (pipeline.Filter, error) {
	return newClamAV(config, "", rmconfig.DefaultClamAVMaxRequestBytes)
}

// NewClamAVWithSecret returns a filter factory whose ClamAV adapters HMAC-verify
// every verdict against secret (SCANNER_HMAC_SECRET). An empty secret leaves
// verdict-signature verification off; the fail-closed fallback still applies.
func NewClamAVWithSecret(secret string) pipeline.FilterFactory {
	return NewClamAVWithLimits(secret, rmconfig.DefaultClamAVMaxRequestBytes)
}

// NewClamAVWithLimits is NewClamAVWithSecret with the largest message the
// filter sends; a larger one is not sent and takes the fallback action, as an
// unreachable scanner does.
func NewClamAVWithLimits(secret string, maxRequestBytes int64) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return newClamAV(config, secret, maxRequestBytes)
	}
}

func newClamAV(config []byte, hmacSecret string, maxRequestBytes int64) (pipeline.Filter, error) {
	cfg := clamavConfig{
		URL:       "http://clamav:3310",
		TimeoutMS: 30000,
		// SECURE DEFAULT (OSI-15): defer, not continue. An unreachable or errored
		// virus scanner temp-fails the message rather than delivering it unscanned.
		// An operator may choose fail-open with "fallback_action":"continue".
		FallbackAction: "defer",
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("clamav: invalid config: %w", err)
		}
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("clamav: url is required")
	}

	adapter := &clamavAdapter{
		url:             strings.TrimRight(cfg.URL, "/"),
		hmacSecret:      hmacSecret,
		maxRequestBytes: maxRequestBytes,
		client: &http.Client{
			Timeout: filterHTTPTimeout(cfg.TimeoutMS, 30*time.Second, 0),
		},
	}

	return &adapterFilter{
		adapter:        adapter,
		fallbackAction: parseAction(cfg.FallbackAction, pipeline.ActionDefer),
	}, nil
}

func (a *clamavAdapter) Name() string { return "clamav" }

// Healthy checks if the ClamAV REST service is reachable.
func (a *clamavAdapter) Healthy(ctx context.Context) bool {
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

// Scan sends the email to the ClamAV REST scan endpoint and parses the result.
func (a *clamavAdapter) Scan(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.AdapterResult, error) {
	// The email as RFC 5322, streamed: it is not held whole while it is scanned.
	rawMsg, err := newMIMERequest(ctx, email, a.maxRequestBytes)
	if err != nil {
		return nil, fmt.Errorf("clamav: serialize email: %w", err)
	}

	requestBody := rawMsg.body()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url+"/scan", requestBody)
	if err != nil {
		_ = requestBody.Close()
		return nil, fmt.Errorf("clamav: create request: %w", err)
	}
	req.ContentLength = rawMsg.length
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clamav: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readFilterResponse(resp, clamavResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("clamav: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clamav: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Authenticate the verdict before trusting it (OSI-15). A missing/forged
	// signature (when a scanner secret is configured) returns an error so
	// adapterFilter fails closed rather than treating the response as clean.
	if err := verifyScannerSignature(a.hmacSecret, resp.Header, body); err != nil {
		return nil, fmt.Errorf("clamav: %w", err)
	}

	var clamResp clamavResponse
	if err := json.Unmarshal(body, &clamResp); err != nil {
		return nil, fmt.Errorf("clamav: parse response: %w", err)
	}

	headers := map[string]string{
		"X-Virus-Scanned": "ClamAV",
	}

	switch {
	case strings.EqualFold(clamResp.Status, "FOUND"):
		virusName := clamResp.Description
		if virusName == "" {
			virusName = "unknown"
		}
		headers["X-Virus-Status"] = fmt.Sprintf("Infected (%s)", virusName)

		return &pipeline.AdapterResult{
			Clean:     false,
			Score:     1,
			Action:    pipeline.ActionReject,
			Details:   fmt.Sprintf("virus detected: %s", virusName),
			Headers:   headers,
			RejectMsg: fmt.Sprintf("Message contains virus: %s", virusName),
		}, nil

	case strings.EqualFold(clamResp.Status, "OK"):
		headers["X-Virus-Status"] = "Clean"

		return &pipeline.AdapterResult{
			Clean:   true,
			Score:   0,
			Action:  pipeline.ActionContinue,
			Details: "no virus detected",
			Headers: headers,
		}, nil

	default:
		// Unrecognized or missing status: fail CLOSED (OSI-15). A response that is
		// neither OK nor FOUND is an ambiguous/partial verdict and must not be
		// treated as clean — surface an error so adapterFilter defers the message.
		return nil, fmt.Errorf("clamav: unrecognized scan status %q (fail-closed)", clamResp.Status)
	}
}
