package filters

import (
	"context"
	"fmt"

	"github.com/restmail/restmail/internal/pipeline"
)

// adapterFilter wraps an ExternalAdapter as a pipeline Filter.
type adapterFilter struct {
	adapter        pipeline.ExternalAdapter
	fallbackAction pipeline.Action // what to do if service unavailable
}

func (f *adapterFilter) Name() string               { return f.adapter.Name() }
func (f *adapterFilter) Type() pipeline.FilterType   { return pipeline.FilterTypeAction }

func (f *adapterFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	name := f.adapter.Name()

	// If the adapter is not healthy, use the fallback action.
	if !f.adapter.Healthy(ctx) {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: f.fallbackAction,
			Log: pipeline.FilterLog{
				Filter: name,
				Result: "unavailable",
				Detail: fmt.Sprintf("%s service is not reachable, using fallback action: %s", name, f.fallbackAction),
			},
		}, nil
	}

	// Call the external adapter to scan the email.
	result, err := f.adapter.Scan(ctx, email)
	if err != nil {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: f.fallbackAction,
			Log: pipeline.FilterLog{
				Filter: name,
				Result: "error",
				Detail: fmt.Sprintf("%s scan failed: %v, using fallback action: %s", name, err, f.fallbackAction),
			},
		}, nil
	}

	// Apply returned headers to the email.
	if len(result.Headers) > 0 {
		if email.Headers.Extra == nil {
			email.Headers.Extra = make(map[string]string)
		}
		for k, v := range result.Headers {
			email.Headers.Extra[k] = v
		}
		if email.Headers.Raw == nil {
			email.Headers.Raw = make(map[string][]string)
		}
		for k, v := range result.Headers {
			email.Headers.Raw[k] = append(email.Headers.Raw[k], v)
		}
	}

	return &pipeline.FilterResult{
		Type:      pipeline.FilterTypeAction,
		Action:    result.Action,
		RejectMsg: result.RejectMsg,
		Log: pipeline.FilterLog{
			Filter: name,
			Result: string(result.Action),
			Detail: result.Details,
		},
	}, nil
}

// parseAction converts a string fallback action name to a pipeline.Action.
// Returns the given default if the string is empty or unrecognised.
func parseAction(s string, def pipeline.Action) pipeline.Action {
	switch pipeline.Action(s) {
	case pipeline.ActionContinue, pipeline.ActionReject,
		pipeline.ActionQuarantine, pipeline.ActionDiscard, pipeline.ActionDefer:
		return pipeline.Action(s)
	default:
		return def
	}
}
