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

	rmconfig "github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/pipeline"
)

// JSSidecar is the JS filter sidecar the javascript filter runs its scripts on
// (JS_FILTER_URL), and the token it authenticates with (JS_FILTER_TOKEN, #160).
// Both are the operator's: a pipeline's filter config cannot send a script, or
// the token, anywhere else.
type JSSidecar struct {
	URL   string
	Token string
}

// Post sends body to one of the sidecar's endpoints ("/execute", "/validate")
// with the token, so the sidecar knows the request comes from the API.
func (s JSSidecar) Post(ctx context.Context, client *http.Client, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create sidecar request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	return client.Do(req)
}

type jsFilterConfig struct {
	Script    string `json:"script"`
	TimeoutMS int    `json:"timeout_ms"`
	// FailureAction is the pipeline action applied when the sidecar cannot be
	// reached, times out, errors, or returns an unusable result. It defaults to
	// "defer" (fail-closed): a wedged or compromised sidecar must not silently
	// disable JS filtering by letting every message through. An operator may set
	// "continue" to fail open, or "reject".
	FailureAction string `json:"failure_action"`
}

type jsFilter struct {
	script    string
	timeout   time.Duration
	sidecar   JSSidecar
	onFailure pipeline.Action
}

func init() {
	pipeline.DefaultRegistry.Register("javascript", NewJavaScript)
}

// jsFailureActions is the set of pipeline actions a failed JS filter may apply.
// A failure means "we could not get a trustworthy verdict", so the choice is
// between holding the message (defer, default), refusing it (reject), or letting
// it pass unfiltered (continue) — never a delivering/transform action.
var jsFailureActions = map[pipeline.Action]bool{
	pipeline.ActionDefer:    true,
	pipeline.ActionContinue: true,
	pipeline.ActionReject:   true,
}

// jsResultActions is the set of actions the sidecar is allowed to return. An
// action outside this set is untrusted (a bug or a compromised sidecar) and is
// rejected rather than cast blindly onto the pipeline.
var jsResultActions = map[pipeline.Action]bool{
	pipeline.ActionContinue:   true,
	pipeline.ActionReject:     true,
	pipeline.ActionQuarantine: true,
	pipeline.ActionDiscard:    true,
	pipeline.ActionDefer:      true,
}

// NewJavaScript creates a javascript filter that runs its script on the sidecar
// at the default address with no token, which the sidecar refuses, so it fails
// closed. It is the init()-registered factory; routes.go re-registers via
// NewJavaScriptWithSidecar to bind the deployment's JS_FILTER_URL and
// JS_FILTER_TOKEN.
func NewJavaScript(config []byte) (pipeline.Filter, error) {
	return newJavaScript(config, JSSidecar{URL: rmconfig.DefaultJSFilterURL})
}

// NewJavaScriptWithSidecar returns a filter factory whose javascript filters
// run their scripts on sidecar.
func NewJavaScriptWithSidecar(sidecar JSSidecar) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		return newJavaScript(config, sidecar)
	}
}

func newJavaScript(config []byte, sidecar JSSidecar) (pipeline.Filter, error) {
	cfg := jsFilterConfig{
		TimeoutMS: 500,
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.Script == "" {
		return nil, fmt.Errorf("javascript filter requires a script")
	}

	onFailure := pipeline.ActionDefer // fail-closed default
	if cfg.FailureAction != "" {
		a := pipeline.Action(cfg.FailureAction)
		if !jsFailureActions[a] {
			return nil, fmt.Errorf("javascript filter: invalid failure_action %q (want defer, continue, or reject)", cfg.FailureAction)
		}
		onFailure = a
	}

	return &jsFilter{
		script:    cfg.Script,
		timeout:   time.Duration(cfg.TimeoutMS) * time.Millisecond,
		sidecar:   sidecar,
		onFailure: onFailure,
	}, nil
}

// fail returns the configured failure result for a sidecar problem, tagging the
// log so an operator can see that the JS verdict was NOT obtained (and, with the
// default, that the message is being held rather than passed unfiltered).
func (f *jsFilter) fail(result, detail string) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: f.onFailure,
		Log: pipeline.FilterLog{
			Filter: "javascript",
			Result: result,
			Detail: fmt.Sprintf("%s; applying failure_action=%s", detail, f.onFailure),
		},
	}
}

func (f *jsFilter) Name() string              { return "javascript" }
func (f *jsFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *jsFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	message, err := json.Marshal(email)
	if err != nil {
		return nil, fmt.Errorf("marshal the email for the sidecar: %w", err)
	}
	bodyBytes, err := json.Marshal(SidecarExecute{
		Script:    f.script,
		Email:     message,
		TimeoutMS: int(f.timeout.Milliseconds()),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal sidecar request: %w", err)
	}

	client := &http.Client{
		Timeout: f.timeout + 5*time.Second, // extra headroom beyond script timeout
	}

	resp, err := f.sidecar.Post(ctx, client, "/execute", bodyBytes)
	if err != nil {
		return f.fail("error", fmt.Sprintf("sidecar request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return f.fail("error", fmt.Sprintf("read sidecar response: %v", err)), nil
	}

	// Handle timeout response
	if resp.StatusCode == http.StatusRequestTimeout {
		return f.fail("timeout", "script execution timed out"), nil
	}

	// Handle other error responses
	if resp.StatusCode != http.StatusOK {
		var errResp SidecarError
		_ = json.Unmarshal(respBody, &errResp)
		detail := errResp.Error
		if detail == "" {
			detail = fmt.Sprintf("sidecar returned status %d", resp.StatusCode)
		}
		return f.fail("error", fmt.Sprintf("script error: %s", detail)), nil
	}

	// Parse the successful response
	var sidecarResp SidecarResult
	if err := json.Unmarshal(respBody, &sidecarResp); err != nil {
		return f.fail("error", fmt.Sprintf("cannot parse sidecar response: %v", err)), nil
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
		return f.fail("error", "invalid result format from script"), nil
	}

	// Validate the returned action against the known set BEFORE casting it into
	// the pipeline. A sidecar (untrusted at runtime) returning an unknown action
	// must not be able to inject an arbitrary Action value; treat it as a failure.
	action := pipeline.Action(jsResult.Action)
	if !jsResultActions[action] {
		return f.fail("error", fmt.Sprintf("sidecar returned unknown action %q", jsResult.Action)), nil
	}

	result := &pipeline.FilterResult{
		Type:   pipeline.FilterType(jsResult.Type),
		Action: action,
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
// This performs a basic check that the script contains a filter function
// definition.
//
// NOTE (issue #201): this is only a substring check — it cannot catch a syntax
// error, a runtime throw, or a script that never returns. Real validation means
// parsing/dry-running the script in the same JS engine the sidecar uses, so the
// authoritative check belongs in the sidecar (a POST /validate already exists;
// see PipelineHandler.ValidateCustomFilter). Delegating this Go-side helper to
// that endpoint is deferred: it turns a pure function into a network call, so it
// needs a sidecar URL + timeout + failure policy (mirroring Execute) and touches
// every caller — a change better made alongside the sidecar hardening in #160.
func ValidateScript(script string) error {
	if !strings.Contains(script, "function filter") {
		return fmt.Errorf("script must define a filter(email) function")
	}
	return nil
}
