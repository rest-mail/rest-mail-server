package pipeline

import (
	"context"
	"testing"
)

// recordingObserver captures Observer calls for assertions.
type recordingObserver struct {
	steps     []StepResult
	terminals []terminalCall
}

type terminalCall struct {
	direction string
	action    Action
	terminal  *StepResult
}

func (o *recordingObserver) ObserveStep(s StepResult) { o.steps = append(o.steps, s) }
func (o *recordingObserver) ObserveTerminal(direction string, action Action, terminal *StepResult) {
	o.terminals = append(o.terminals, terminalCall{direction, action, terminal})
}

func continueFilter(name string) FilterFactory {
	return func([]byte) (Filter, error) {
		return &mockActionFilter{name: name, action: ActionContinue}, nil
	}
}

// The engine must call ObserveStep once per executed step and ObserveTerminal
// exactly once, with the pipeline direction and the final action.
func TestEngine_Observer_PerStepAndOnceTerminal(t *testing.T) {
	reg := NewRegistry()
	reg.Register("a", continueFilter("a"))
	reg.Register("b", continueFilter("b"))

	obs := &recordingObserver{}
	eng := NewEngine(reg, nil, obs)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters: []FilterConfig{
			{Name: "a", Enabled: true},
			{Name: "b", Enabled: true},
		},
	}

	res, err := eng.Execute(context.Background(), cfg, &EmailJSON{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.FinalAction != ActionContinue {
		t.Fatalf("final action = %q, want continue", res.FinalAction)
	}
	if len(obs.steps) != 2 {
		t.Fatalf("ObserveStep calls = %d, want 2", len(obs.steps))
	}
	if len(obs.terminals) != 1 {
		t.Fatalf("ObserveTerminal calls = %d, want 1", len(obs.terminals))
	}
	if got := obs.terminals[0]; got.direction != "inbound" || got.action != ActionContinue {
		t.Fatalf("terminal = %+v, want {inbound continue}", got)
	}
	// A continue outcome has no terminal reject step.
	if obs.terminals[0].terminal != nil {
		t.Fatalf("continue terminal step = %+v, want nil", obs.terminals[0].terminal)
	}
}

// A terminal action (reject) must be observed as a step AND stop the pipeline:
// downstream filters are neither executed nor observed, and ObserveTerminal is
// called once with the terminal action.
func TestEngine_Observer_RejectStopsAndReportsTerminal(t *testing.T) {
	reg := NewRegistry()
	reg.Register("ok", continueFilter("ok"))
	reg.Register("bad", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "bad", action: ActionReject, rejectMsg: "nope"}, nil
	})
	reg.Register("never", continueFilter("never"))

	obs := &recordingObserver{}
	eng := NewEngine(reg, nil, obs)

	cfg := &PipelineConfig{
		Direction: "outbound",
		Filters: []FilterConfig{
			{Name: "ok", Enabled: true},
			{Name: "bad", Enabled: true},
			{Name: "never", Enabled: true},
		},
	}

	res, err := eng.Execute(context.Background(), cfg, &EmailJSON{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.FinalAction != ActionReject {
		t.Fatalf("final action = %q, want reject", res.FinalAction)
	}
	if len(obs.steps) != 2 {
		t.Fatalf("ObserveStep calls = %d, want 2 (never must not run)", len(obs.steps))
	}
	if obs.steps[1].Action != ActionReject {
		t.Fatalf("terminal step action = %q, want reject", obs.steps[1].Action)
	}
	if len(obs.terminals) != 1 || obs.terminals[0].action != ActionReject || obs.terminals[0].direction != "outbound" {
		t.Fatalf("terminals = %+v, want one {outbound reject}", obs.terminals)
	}
	// The non-continue terminal must carry the deciding step so the observer can
	// derive a reason_code; it must be the rejecting "bad" step.
	term := obs.terminals[0].terminal
	if term == nil || term.Action != ActionReject || term.FilterName != "bad" {
		t.Fatalf("terminal step = %+v, want non-nil {bad reject}", term)
	}
}

// An engine constructed without an Observer must use the no-op default and run
// without panicking (backward compatibility for existing callers/tests).
func TestEngine_NoObserver_DefaultsToNop(t *testing.T) {
	reg := NewRegistry()
	reg.Register("a", continueFilter("a"))
	eng := NewEngine(reg, nil)

	cfg := &PipelineConfig{
		Direction: "inbound",
		Filters:   []FilterConfig{{Name: "a", Enabled: true}},
	}
	if _, err := eng.Execute(context.Background(), cfg, &EmailJSON{}); err != nil {
		t.Fatalf("Execute with nop observer: %v", err)
	}

	// A nil Observer argument must also fall back to the no-op.
	eng2 := NewEngine(reg, nil, nil)
	if _, err := eng2.Execute(context.Background(), cfg, &EmailJSON{}); err != nil {
		t.Fatalf("Execute with nil observer: %v", err)
	}
}
