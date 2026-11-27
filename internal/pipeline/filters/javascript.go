package filters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	rmconfig "github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// JSSidecar is the JS filter sidecar the javascript filter runs its scripts on
// (JS_FILTER_URL), and the token it authenticates with (JS_FILTER_TOKEN, #160).
// Both are the operator's: a pipeline's filter config cannot send a script, or
// the token, anywhere else.
type JSSidecar struct {
	URL   string
	Token string
	// MaxBodyBytes is the largest request body the sidecar accepts
	// (JS_FILTER_MAX_BODY_BYTES on both sides). Zero means the default,
	// rmconfig.DefaultJSFilterMaxBodyBytes. The javascript filter refuses a larger
	// request itself, so the message is not uploaded just to be answered with 413.
	MaxBodyBytes int64
}

func (s JSSidecar) maxBodyBytes() int64 {
	if s.MaxBodyBytes > 0 {
		return s.MaxBodyBytes
	}
	return rmconfig.DefaultJSFilterMaxBodyBytes
}

// Post sends body to one of the sidecar's endpoints ("/execute", "/validate")
// with the token, so the sidecar knows the request comes from the API.
func (s JSSidecar) Post(ctx context.Context, client *http.Client, path string, body []byte) (*http.Response, error) {
	return s.post(ctx, client, path, sidecarRequestBody{body})
}

// post sends a body whose parts are not joined into one slice.
func (s JSSidecar) post(ctx context.Context, client *http.Client, path string, body sidecarRequestBody) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.URL, "/")+path, body.reader())
	if err != nil {
		return nil, fmt.Errorf("create sidecar request: %w", err)
	}
	req.ContentLength = body.size()
	if req.ContentLength == 0 {
		req.Body = http.NoBody
	}
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(body.reader()), nil }
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	return client.Do(req)
}

// sidecarRequestBody is a request body kept as the pieces it is made of, so a
// message the size of the sidecar's limit is not copied into one more buffer to be
// sent.
type sidecarRequestBody [][]byte

func (b sidecarRequestBody) size() int64 {
	var n int64
	for _, part := range b {
		n += int64(len(part))
	}
	return n
}

func (b sidecarRequestBody) reader() io.Reader {
	readers := make([]io.Reader, len(b))
	for i, part := range b {
		readers[i] = bytes.NewReader(part)
	}
	return io.MultiReader(readers...)
}

// bytes joins the pieces; only tests and small bodies need it.
func (b sidecarRequestBody) bytes() []byte {
	return bytes.Join(b, nil)
}

// executeRequest is POST /execute's body, SidecarExecute{Script, Email, TimeoutMS}
// as encoding/json writes it, with the message marshalled once and sent as it is
// rather than marshalled again inside the envelope.
func executeRequest(script string, message []byte, timeoutMS int) (sidecarRequestBody, error) {
	scriptJSON, err := json.Marshal(script)
	if err != nil {
		return nil, fmt.Errorf("marshal sidecar request: %w", err)
	}
	head := append(make([]byte, 0, len(scriptJSON)+32), `{"script":`...)
	head = append(head, scriptJSON...)
	head = append(head, `,"email":`...)
	tail := []byte("}")
	if timeoutMS != 0 { // omitempty, as SidecarExecute.TimeoutMS
		tail = append(strconv.AppendInt([]byte(`,"timeout_ms":`), int64(timeoutMS), 10), '}')
	}
	return sidecarRequestBody{head, message, tail}, nil
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

const sidecarHTTPHeadroom = 5 * time.Second

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
		timeout:   filterHTTPTimeout(cfg.TimeoutMS, 500*time.Millisecond, sidecarHTTPHeadroom),
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

// request is the body of POST /execute for email: the message marshalled once.
func (f *jsFilter) request(email *pipeline.EmailJSON) (sidecarRequestBody, error) {
	message, err := json.Marshal(email)
	if err != nil {
		return nil, fmt.Errorf("marshal the email for the sidecar: %w", err)
	}
	return executeRequest(f.script, message, int(f.timeout.Milliseconds()))
}

func (f *jsFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	// A part handed in by reference reaches the script as content, as it always did.
	limit := f.sidecar.maxBodyBytes()
	email, err := withReferencedContent(ctx, email, limit)
	if errors.Is(err, errOverRequestLimit) {
		return f.fail("error", fmt.Sprintf("script error: request body too large (limit %d bytes)", limit)), nil
	}
	if err != nil {
		return f.fail("error", fmt.Sprintf("read a referenced attachment: %v", err)), nil
	}
	body, err := f.request(email)
	if err != nil {
		return nil, err
	}
	// The sidecar answers 413 to a body over its limit once it has been uploaded;
	// the same refusal is made here, with the sidecar's own words, before any of it
	// is sent.
	if body.size() > limit {
		return f.fail("error", fmt.Sprintf("script error: request body too large (limit %d bytes)", limit)), nil
	}

	client := &http.Client{
		Timeout: f.timeout + sidecarHTTPHeadroom,
	}

	resp, err := f.sidecar.post(ctx, client, "/execute", body)
	if err != nil {
		return f.fail("error", fmt.Sprintf("sidecar request failed: %v", err)), nil
	}
	defer resp.Body.Close()

	respBody, err := ReadSidecarResponse(resp)
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
