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

	"github.com/restmail/restmail/internal/pipeline"
)

const defaultSidecarURL = "http://js-filter:3100"

type jsFilterConfig struct {
	Script    string `json:"script"`
	TimeoutMS int    `json:"timeout_ms"`
	URL       string `json:"url"`
}

type jsFilter struct {
	script     string
	timeout    time.Duration
	sidecarURL string
}

func init() {
	pipeline.DefaultRegistry.Register("javascript", NewJavaScript)
}

func NewJavaScript(config []byte) (pipeline.Filter, error) {
	cfg := jsFilterConfig{
		TimeoutMS: 500,
		URL:       defaultSidecarURL,
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.Script == "" {
		return nil, fmt.Errorf("javascript filter requires a script")
	}
	if cfg.URL == "" {
		cfg.URL = defaultSidecarURL
	}

	return &jsFilter{
		script:     cfg.Script,
		timeout:    time.Duration(cfg.TimeoutMS) * time.Millisecond,
		sidecarURL: cfg.URL,
	}, nil
}

func (f *jsFilter) Name() string             { return "javascript" }
func (f *jsFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

// sidecarRequest is the JSON payload sent to the Node.js sidecar.
type sidecarRequest struct {
	Script    string             `json:"script"`
	Email     *pipeline.EmailJSON `json:"email"`
	TimeoutMS int                `json:"timeout_ms"`
}

// sidecarResponse is the JSON response from the Node.js sidecar.
type sidecarResponse struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

func (f *jsFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	reqBody := sidecarRequest{
		Script:    f.script,
		Email:     email,
		TimeoutMS: int(f.timeout.Milliseconds()),
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal sidecar request: %w", err)
	}

	// Build HTTP request with context for cancellation
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.sidecarURL+"/execute", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create sidecar request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: f.timeout + 5*time.Second, // extra headroom beyond script timeout
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "javascript",
				Result: "error",
				Detail: fmt.Sprintf("sidecar request failed: %v", err),
			},
		}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "javascript",
				Result: "error",
				Detail: fmt.Sprintf("read sidecar response: %v", err),
			},
		}, nil
	}

	// Handle timeout response
	if resp.StatusCode == http.StatusRequestTimeout {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "javascript",
				Result: "timeout",
				Detail: "script execution timed out",
			},
		}, nil
	}

	// Handle other error responses
	if resp.StatusCode != http.StatusOK {
		var errResp sidecarResponse
		json.Unmarshal(respBody, &errResp)
		detail := errResp.Error
		if detail == "" {
			detail = fmt.Sprintf("sidecar returned status %d", resp.StatusCode)
		}
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "javascript",
				Result: "error",
				Detail: fmt.Sprintf("script error: %s", detail),
			},
		}, nil
	}

	// Parse the successful response
	var sidecarResp sidecarResponse
	if err := json.Unmarshal(respBody, &sidecarResp); err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "javascript",
				Result: "error",
				Detail: fmt.Sprintf("cannot parse sidecar response: %v", err),
			},
		}, nil
	}

	// Parse the result from the sidecar into the expected format
	var jsResult struct {
		Type    string          `json:"type"`
		Action  string          `json:"action"`
		Message json.RawMessage `json:"message"`
		Log     struct {
			Detail string `json:"detail"`
		} `json:"log"`
	}
	if err := json.Unmarshal(sidecarResp.Result, &jsResult); err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "javascript",
				Result: "error",
				Detail: "invalid result format from script",
			},
		}, nil
	}

	result := &pipeline.FilterResult{
		Type:   pipeline.FilterType(jsResult.Type),
		Action: pipeline.Action(jsResult.Action),
		Log: pipeline.FilterLog{
			Filter: "javascript",
			Result: jsResult.Action,
			Detail: jsResult.Log.Detail,
		},
	}

	// If transform, parse the modified message
	if jsResult.Type == "transform" && len(jsResult.Message) > 0 {
		var modified pipeline.EmailJSON
		if err := json.Unmarshal(jsResult.Message, &modified); err == nil {
			result.Message = &modified
		}
	}

	return result, nil
}

// ValidateScript checks if a JavaScript filter script is syntactically valid.
// This performs a basic check that the script contains a filter function definition.
func ValidateScript(script string) error {
	if !strings.Contains(script, "function filter") {
		return fmt.Errorf("script must define a filter(email) function")
	}
	return nil
}
