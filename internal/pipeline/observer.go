package pipeline

// Observer receives observations emitted while a pipeline executes. It is the
// single capture seam the engine exposes for metrics/telemetry.
//
// IMPORTANT: implementations run INLINE on the mail-processing hot path. Every
// method MUST be non-blocking and lock-free (e.g. atomic Prometheus counter
// updates only) — never do I/O, acquire contended locks, or otherwise slow
// message processing.
//
// The pipeline package intentionally does NOT depend on any metrics package;
// the concrete Observer is injected via NewEngine so metrics stay a leaf and
// the engine remains trivially unit-testable with a fake Observer.
type Observer interface {
	// ObserveStep is called once per finalized filter step, in execution order
	// (including skipped and errored steps, and the terminal step).
	ObserveStep(step StepResult)
	// ObserveTerminal is called exactly once per Execute, after the final
	// action for the message has been decided. direction is the pipeline
	// direction ("inbound"/"outbound"); action is the final pipeline action.
	// terminal is the step that decided a non-continue outcome (so the observer
	// can derive a bounded reason_code via ReasonForStep); it is nil when the
	// message ran to a continue outcome (delivered/queued — no reject reason).
	ObserveTerminal(direction string, action Action, terminal *StepResult)
}

// NopObserver is the default Observer used when none is injected. All methods
// are no-ops, so an un-wired engine carries zero observation overhead.
type NopObserver struct{}

// ObserveStep does nothing.
func (NopObserver) ObserveStep(StepResult) {}

// ObserveTerminal does nothing.
func (NopObserver) ObserveTerminal(string, Action, *StepResult) {}
