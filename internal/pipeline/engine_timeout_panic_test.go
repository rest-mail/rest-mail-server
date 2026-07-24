package pipeline

import (
	"context"
	"testing"
	"time"
)

// panicFilter deterministically panics from Execute. Without recovery this is a
// poison message: the panic propagates out of the engine (crashing the worker),
// and the retrying sender re-triggers it forever.
type panicFilter struct{ name string }

func (f *panicFilter) Name() string     { return f.name }
func (f *panicFilter) Type() FilterType { return FilterTypeAction }
func (f *panicFilter) Execute(context.Context, *EmailJSON) (*FilterResult, error) {
	panic("boom in filter")
}

// blockingFilter blocks forever and deliberately IGNORES its context, modelling
// a hung/deadlocked filter. Only an out-of-band per-filter timeout can bound it;
// context.WithTimeout alone cannot, because the filter never observes the
// deadline.
type blockingFilter struct{ name string }

func (f *blockingFilter) Name() string     { return f.name }
func (f *blockingFilter) Type() FilterType { return FilterTypeAction }
func (f *blockingFilter) Execute(context.Context, *EmailJSON) (*FilterResult, error) {
	select {} // block forever, never looking at ctx.Done()
}

// TestEngine_PanickingFilter_Recovered proves a filter that panics is contained:
// the panic is recovered, turned into a filter error, and routed through the
// fail-closed policy (defer) instead of crashing the worker. The downstream
// filter must not run — the pipeline stops at the failed step.
func TestEngine_PanickingFilter_Recovered(t *testing.T) {
	reg := NewRegistry()
	reg.Register("panic", func([]byte) (Filter, error) { return &panicFilter{name: "panic"}, nil })
	reg.Register("after", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "after", action: ActionContinue}, nil
	})
	eng := NewEngine(reg, nil)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "panic", Type: FilterTypeAction, Enabled: true},
			{Name: "after", Type: FilterTypeAction, Enabled: true},
		},
		Active: true,
	}

	res, err := eng.Execute(context.Background(), cfg, &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionDefer {
		t.Fatalf("final action = %q, want defer (panic contained, fail-closed)", res.FinalAction)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 (pipeline stops at panicking filter)", len(res.Steps))
	}
	if res.Steps[0].Error == "" {
		t.Fatalf("step error empty, want a recorded panic error")
	}
}

// TestEngine_PanickingFilter_ContinueOverride proves a recovered panic is routed
// through the configured policy: under the legacy fail-open override the
// panicking filter is skipped and the pipeline continues to the next filter.
func TestEngine_PanickingFilter_ContinueOverride(t *testing.T) {
	reg := NewRegistry()
	reg.Register("panic", func([]byte) (Filter, error) { return &panicFilter{name: "panic"}, nil })
	reg.Register("after", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "after", action: ActionContinue}, nil
	})
	eng := NewEngine(reg, nil)
	eng.SetFilterErrorAction(ActionContinue)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "panic", Type: FilterTypeAction, Enabled: true},
			{Name: "after", Type: FilterTypeAction, Enabled: true},
		},
		Active: true,
	}

	res, err := eng.Execute(context.Background(), cfg, &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionContinue {
		t.Fatalf("final action = %q, want continue (fail-open override)", res.FinalAction)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("steps = %d, want 2 (downstream filter runs after skipped panic)", len(res.Steps))
	}
}

// TestEngine_BlockingFilter_TimesOut proves a filter that hangs forever is bounded
// by the per-filter timeout and routed through the fail-closed policy (defer),
// instead of wedging the whole pipeline. The engine call is run in a goroutine
// with a generous test-side deadline: on unfixed code (no per-filter timeout)
// Execute never returns and the test-side deadline fires the failure.
func TestEngine_BlockingFilter_TimesOut(t *testing.T) {
	reg := NewRegistry()
	reg.Register("block", func([]byte) (Filter, error) { return &blockingFilter{name: "block"}, nil })
	eng := NewEngine(reg, nil)
	eng.SetFilterTimeout(150 * time.Millisecond)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "block", Type: FilterTypeAction, Enabled: true},
		},
		Active: true,
	}

	type outcome struct {
		res *ExecutionResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, e := eng.Execute(context.Background(), cfg, &EmailJSON{})
		done <- outcome{r, e}
	}()

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("unexpected error: %v", o.err)
		}
		if o.res.FinalAction != ActionDefer {
			t.Fatalf("final action = %q, want defer (timeout, fail-closed)", o.res.FinalAction)
		}
		if len(o.res.Steps) != 1 || o.res.Steps[0].Error == "" {
			t.Fatalf("want 1 step with a recorded timeout error, got %d steps", len(o.res.Steps))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not return: a blocking filter hung the pipeline (no per-filter timeout)")
	}
}
