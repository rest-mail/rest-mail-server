package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dop251/goja"
	"github.com/restmail/restmail/internal/pipeline"
)

type jsFilterConfig struct {
	Script    string `json:"script"`
	TimeoutMS int    `json:"timeout_ms"`
	MaxMemMB  int    `json:"max_mem_mb"`
}

type jsFilter struct {
	script  string
	timeout time.Duration
}

func init() {
	pipeline.DefaultRegistry.Register("javascript", NewJavaScript)
}

func NewJavaScript(config []byte) (pipeline.Filter, error) {
	cfg := jsFilterConfig{
		TimeoutMS: 500,
		MaxMemMB:  64,
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.Script == "" {
		return nil, fmt.Errorf("javascript filter requires a script")
	}

	return &jsFilter{
		script:  cfg.Script,
		timeout: time.Duration(cfg.TimeoutMS) * time.Millisecond,
	}, nil
}

func (f *jsFilter) Name() string             { return "javascript" }
func (f *jsFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *jsFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	vm := goja.New()

	// Set up timeout
	timer := time.AfterFunc(f.timeout, func() {
		vm.Interrupt("execution timeout")
	})
	defer timer.Stop()

	// Convert email to JS object
	emailJSON, err := json.Marshal(email)
	if err != nil {
		return nil, fmt.Errorf("marshal email: %w", err)
	}

	var emailObj interface{}
	json.Unmarshal(emailJSON, &emailObj)
	vm.Set("__email__", emailObj)

	// Run the script with the filter function wrapper
	script := fmt.Sprintf(`
		var email = __email__;
		%s
		var __result__ = filter(email);
		__result__;
	`, f.script)

	val, err := vm.RunString(script)
	if err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "javascript",
				Result: "error",
				Detail: fmt.Sprintf("script error: %v", err),
			},
		}, nil
	}

	// Parse the result
	resultObj := val.Export()
	resultJSON, err := json.Marshal(resultObj)
	if err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log: pipeline.FilterLog{
				Filter: "javascript",
				Result: "error",
				Detail: fmt.Sprintf("cannot marshal result: %v", err),
			},
		}, nil
	}

	var jsResult struct {
		Type    string          `json:"type"`
		Action  string          `json:"action"`
		Message json.RawMessage `json:"message"`
		Log     struct {
			Detail string `json:"detail"`
		} `json:"log"`
	}
	if err := json.Unmarshal(resultJSON, &jsResult); err != nil {
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
func ValidateScript(script string) error {
	vm := goja.New()
	_, err := vm.RunString(fmt.Sprintf(`
		%s
		if (typeof filter !== 'function') {
			throw new Error('script must define a filter(email) function');
		}
	`, script))
	if err != nil {
		return fmt.Errorf("syntax error: %w", err)
	}
	return nil
}
