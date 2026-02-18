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
	url    string
	client *http.Client
}

// clamavResponse represents the JSON response from a ClamAV REST scan endpoint.
type clamavResponse struct {
	Status      string `json:"status"`      // "OK" or "FOUND"
	Description string `json:"description"` // virus name if found
}

// NewClamAV creates a new ClamAV adapter filter from JSON configuration.
func NewClamAV(config []byte) (pipeline.Filter, error) {
	cfg := clamavConfig{
		URL:            "http://clamav:3310",
		TimeoutMS:      30000,
		FallbackAction: "continue",
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, fmt.Errorf("clamav: invalid config: %w", err)
		}
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("clamav: url is required")
	}
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = 30000
	}

	adapter := &clamavAdapter{
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
	// Serialize the email to raw RFC 2822 format for scanning.
	rawMsg, err := rmime.Serialize(email)
	if err != nil {
		return nil, fmt.Errorf("clamav: serialize email: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url+"/scan", bytes.NewReader(rawMsg))
	if err != nil {
		return nil, fmt.Errorf("clamav: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clamav: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("clamav: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clamav: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var clamResp clamavResponse
	if err := json.Unmarshal(body, &clamResp); err != nil {
		return nil, fmt.Errorf("clamav: parse response: %w", err)
	}

	headers := map[string]string{
		"X-Virus-Scanned": "ClamAV",
	}

	// Determine if the message is infected.
	infected := strings.EqualFold(clamResp.Status, "FOUND")

	if infected {
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
	}

	headers["X-Virus-Status"] = "Clean"

	return &pipeline.AdapterResult{
		Clean:   true,
		Score:   0,
		Action:  pipeline.ActionContinue,
		Details: "no virus detected",
		Headers: headers,
	}, nil
}
