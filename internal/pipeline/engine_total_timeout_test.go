package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// slowFilter takes d to answer continue, well inside the per-filter backstop. It
// does not look at its context, so only the engine can stop it. ran counts the
// calls that started.
type slowFilter struct {
	name string
	d    time.Duration
	ran  *atomic.Int32
}

func (f *slowFilter) Name() string     { return f.name }
func (f *slowFilter) Type() FilterType { return FilterTypeAction }
func (f *slowFilter) Execute(context.Context, *EmailJSON) (*FilterResult, error) {
	if f.ran != nil {
		f.ran.Add(1)
	}
	time.Sleep(f.d)
	return &FilterResult{Type: FilterTypeAction, Action: ActionContinue}, nil
}

// slowPipeline registers count slow filters of d each and a pipeline running them
// in order. ran counts the filters that started.
func slowPipeline(count int, d time.Duration) (*Registry, *PipelineConfig, *atomic.Int32) {
	reg := NewRegistry()
	ran := &atomic.Int32{}
	cfg := &PipelineConfig{Direction: "inbound", Active: true}
	for i := range count {
		name := fmt.Sprintf("slow%d", i+1)
		reg.Register(name, func([]byte) (Filter, error) { return &slowFilter{name: name, d: d, ran: ran}, nil })
		cfg.Filters = append(cfg.Filters, FilterConfig{Name: name, Type: FilterTypeAction, Enabled: true})
	}
	return reg, cfg, ran
}

// Four filters of 100ms each pass the per-filter backstop (300ms) one by one, but
// together run past the pipeline's deadline (250ms). The run stops at the deadline:
// the third filter is stopped in flight, the fourth never starts, and the message
// is deferred as for a filter that timed out (#2107).
func TestEngine_TotalTimeout_StopsRunAtDeadline(t *testing.T) {
	const (
		perFilter = 300 * time.Millisecond
		total     = 250 * time.Millisecond
		each      = 100 * time.Millisecond
	)
	reg, cfg, ran := slowPipeline(4, each)
	eng := NewEngine(reg, nil)
	eng.SetFilterTimeout(perFilter)
	eng.SetTotalTimeout(total)

	start := time.Now()
	res, err := eng.Execute(context.Background(), cfg, &EmailJSON{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionDefer {
		t.Fatalf("final action = %q, want defer (the deadline is a filter timeout, fail-closed)", res.FinalAction)
	}
	if elapsed >= 4*each {
		t.Fatalf("run took %s, want it stopped at the %s deadline, not after all four filters", elapsed, total)
	}
	if len(res.Steps) != 3 {
		t.Fatalf("steps = %d, want 3 (two finished, the third stopped at the deadline)", len(res.Steps))
	}
	if got := res.Steps[2].Error; !strings.Contains(got, "pipeline's deadline (250ms) passed") {
		t.Fatalf("third step error = %q, want it to name the pipeline's deadline", got)
	}
	if got := ran.Load(); got != 3 {
		t.Fatalf("%d filters started, want 3 (the fourth never starts)", got)
	}
}

// Under the fail-open choice an overrun is what a filter timeout is: the filter
// stopped and the ones not started are each skipped, and the message goes on.
// None of the skipped filters is started.
func TestEngine_TotalTimeout_FailOpenSkipsTheRest(t *testing.T) {
	reg, cfg, ran := slowPipeline(4, 100*time.Millisecond)
	eng := NewEngine(reg, nil)
	eng.SetFilterTimeout(300 * time.Millisecond)
	eng.SetTotalTimeout(250 * time.Millisecond)
	eng.SetFilterErrorAction(ActionContinue)

	res, err := eng.Execute(context.Background(), cfg, &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionContinue {
		t.Fatalf("final action = %q, want continue (fail-open)", res.FinalAction)
	}
	if len(res.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(res.Steps))
	}
	if res.Steps[2].Error == "" || res.Steps[2].Action != ActionContinue {
		t.Fatalf("third step = %+v, want a skipped filter error", res.Steps[2])
	}
	if got := res.Steps[3].Error; !strings.HasPrefix(got, "not run: the pipeline's deadline") {
		t.Fatalf("fourth step error = %q, want not run for the deadline", got)
	}
	if got := ran.Load(); got != 3 {
		t.Fatalf("%d filters started, want 3", got)
	}
}

// cancelAfterFirstStep cancels the run's context, as a caller that goes away
// does, once the first filter's step is recorded: after it ran, before the next.
type cancelAfterFirstStep struct {
	NopObserver
	cancel context.CancelFunc
}

func (o cancelAfterFirstStep) ObserveStep(StepResult) { o.cancel() }

// A context that ends between two filters keeps the next one from running: it is
// not started at all, and the message is deferred.
func TestEngine_ContextEndedBetweenFilters_NextNotStarted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := &atomic.Int32{}
	reg := NewRegistry()
	reg.Register("first", func([]byte) (Filter, error) {
		return &mockActionFilter{name: "first", action: ActionContinue}, nil
	})
	reg.Register("next", func([]byte) (Filter, error) { return &slowFilter{name: "next", ran: ran}, nil })
	eng := NewEngine(reg, nil, cancelAfterFirstStep{cancel: cancel})
	cfg := &PipelineConfig{
		Direction: "inbound",
		Active:    true,
		Filters: []FilterConfig{
			{Name: "first", Type: FilterTypeAction, Enabled: true},
			{Name: "next", Type: FilterTypeAction, Enabled: true},
		},
	}

	res, err := eng.Execute(ctx, cfg, &EmailJSON{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FinalAction != ActionDefer {
		t.Fatalf("final action = %q, want defer", res.FinalAction)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(res.Steps))
	}
	if got := res.Steps[1].Error; got != "not run: the caller cancelled it" {
		t.Fatalf("second step error = %q, want not run because the caller cancelled", got)
	}
	// A filter the engine started would have counted by now.
	time.Sleep(50 * time.Millisecond)
	if got := ran.Load(); got != 0 {
		t.Fatalf("the next filter started %d time(s) after the context ended, want 0", got)
	}
}
