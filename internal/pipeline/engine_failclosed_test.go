package pipeline

import (
	"context"
	"errors"
	"testing"
)

// execErrorFilter returns an error from Execute to exercise the OSI-18
// fail-closed runtime-error path.
type execErrorFilter struct{ name string }

func (f *execErrorFilter) Name() string     { return f.name }
func (f *execErrorFilter) Type() FilterType { return FilterTypeAction }
func (f *execErrorFilter) Execute(context.Context, *EmailJSON) (*FilterResult, error) {
	return nil, errors.New("filter blew up")
}

func unknownFilterPipeline() *PipelineConfig {
	return &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "ghost", Type: FilterTypeAction, Enabled: true},
		},
		Active: true,
	}
}

// TestEngine_UnknownFilter_DefaultDefers proves an unknown/renamed filter
// fail-CLOSES (defers) by default instead of being silently skipped (OSI-18).
func TestEngine_UnknownFilter_DefaultDefers(t *testing.T) {
	eng := NewEngine(NewRegistry(), nil) // empty registry -> "ghost" is unknown

	res, err := eng.Execute(context.Background(), unknownFilterPipeline(), &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionDefer {
		t.Fatalf("final action = %q, want defer (fail-closed)", res.FinalAction)
	}
}

// TestEngine_ErroredFilter_DefaultDefers proves a filter that errors at runtime
// fail-CLOSES (defers) by default.
func TestEngine_ErroredFilter_DefaultDefers(t *testing.T) {
	reg := NewRegistry()
	reg.Register("boom", func([]byte) (Filter, error) { return &execErrorFilter{name: "boom"}, nil })
	reg.Register("after", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "after", action: ActionContinue}, nil
	})
	eng := NewEngine(reg, nil)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "boom", Type: FilterTypeAction, Enabled: true},
			{Name: "after", Type: FilterTypeAction, Enabled: true},
		},
		Active: true,
	}

	res, err := eng.Execute(context.Background(), cfg, &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionDefer {
		t.Fatalf("final action = %q, want defer", res.FinalAction)
	}
	// The pipeline must stop at the failed filter: the downstream filter never runs.
	if len(res.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 (pipeline stops on fail-closed)", len(res.Steps))
	}
}

// TestEngine_UnknownFilter_ContinueRestoresFailOpen proves the config override
// (ActionContinue) restores the legacy fail-open behavior: the unknown filter is
// skipped and the pipeline continues.
func TestEngine_UnknownFilter_ContinueRestoresFailOpen(t *testing.T) {
	eng := NewEngine(NewRegistry(), nil)
	eng.SetFilterErrorAction(ActionContinue)

	res, err := eng.Execute(context.Background(), unknownFilterPipeline(), &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionContinue {
		t.Fatalf("final action = %q, want continue (fail-open override)", res.FinalAction)
	}
}

// TestEngine_UnknownFilter_RejectOverride proves the reject override hard-fails.
func TestEngine_UnknownFilter_RejectOverride(t *testing.T) {
	eng := NewEngine(NewRegistry(), nil)
	eng.SetFilterErrorAction(ActionReject)

	res, err := eng.Execute(context.Background(), unknownFilterPipeline(), &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionReject {
		t.Fatalf("final action = %q, want reject", res.FinalAction)
	}
}

// TestEngine_SetFilterErrorAction_IgnoresInvalid proves an unsupported action is
// ignored, leaving the secure default in place.
func TestEngine_SetFilterErrorAction_IgnoresInvalid(t *testing.T) {
	eng := NewEngine(NewRegistry(), nil)
	eng.SetFilterErrorAction(ActionQuarantine) // not a valid fail action

	res, err := eng.Execute(context.Background(), unknownFilterPipeline(), &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionDefer {
		t.Fatalf("final action = %q, want defer (invalid override ignored)", res.FinalAction)
	}
}
