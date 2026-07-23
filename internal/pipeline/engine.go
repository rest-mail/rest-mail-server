package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Engine executes a pipeline of filters against an email.
type Engine struct {
	registry *Registry
	logger   *slog.Logger
	observer Observer
}

// NewEngine creates a pipeline execution engine.
//
// An optional Observer may be supplied to receive per-step and terminal
// observations (used for metrics). When omitted, a no-op Observer is used so
// existing callers and tests carry zero observation overhead. Only the first
// supplied Observer is used; nil entries fall back to the no-op.
func NewEngine(registry *Registry, logger *slog.Logger, observer ...Observer) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	var obs Observer = NopObserver{}
	if len(observer) > 0 && observer[0] != nil {
		obs = observer[0]
	}
	return &Engine{
		registry: registry,
		logger:   logger,
		observer: obs,
	}
}

// ExecutionResult holds the outcome of running a full pipeline.
type ExecutionResult struct {
	FinalAction Action      `json:"final_action"`
	FinalEmail  *EmailJSON  `json:"final_email"`
	Steps       []StepResult `json:"steps"`
	RejectMsg   string      `json:"reject_message,omitempty"`
	Duration    time.Duration `json:"duration_ms"`
}

// StepResult records what happened at each pipeline step.
type StepResult struct {
	FilterName string        `json:"filter_name"`
	FilterType FilterType    `json:"filter_type"`
	Action     Action        `json:"action"`
	Skipped    bool          `json:"skipped,omitempty"`
	SkipReason string        `json:"skip_reason,omitempty"`
	Log        FilterLog     `json:"log"`
	Duration   time.Duration `json:"duration_ms"`
	Error      string        `json:"error,omitempty"`
}

// Execute runs the given pipeline configuration against an email.
// It returns the final result after all filters have been applied.
func (e *Engine) Execute(ctx context.Context, pipeline *PipelineConfig, email *EmailJSON) (*ExecutionResult, error) {
	start := time.Now()
	result := &ExecutionResult{
		FinalAction: ActionContinue,
		FinalEmail:  email,
	}

	// Build the list of active filters
	skipSet := make(map[string]bool)

	// recordStep finalizes a step: it appends it to the result and hands it to
	// the observer. Observation happens for every step (including skipped and
	// errored ones), in execution order, at step finalize.
	recordStep := func(s StepResult) {
		result.Steps = append(result.Steps, s)
		e.observer.ObserveStep(s)
	}

Loop:
	for _, fc := range pipeline.Filters {
		if !fc.Enabled {
			continue
		}

		stepStart := time.Now()
		step := StepResult{
			FilterName: fc.Name,
			FilterType: fc.Type,
		}

		// Check if this filter should be skipped
		if skipSet[fc.Name] && !fc.Unskippable {
			step.Skipped = true
			step.SkipReason = "skipped by upstream filter"
			step.Action = ActionContinue
			step.Duration = time.Since(stepStart)
			recordStep(step)
			e.logger.Debug("filter skipped", "filter", fc.Name, "reason", step.SkipReason)
			continue
		}

		// Create the filter instance
		filter, err := e.registry.Create(fc.Name, fc.Config)
		if err != nil {
			step.Error = fmt.Sprintf("create filter: %v", err)
			step.Duration = time.Since(stepStart)
			recordStep(step)
			e.logger.Error("failed to create filter", "filter", fc.Name, "error", err)
			continue // Skip filters that fail to instantiate
		}

		// Execute the filter
		filterResult, err := filter.Execute(ctx, result.FinalEmail)
		if err != nil {
			step.Error = fmt.Sprintf("execute: %v", err)
			step.Duration = time.Since(stepStart)
			recordStep(step)
			e.logger.Error("filter execution failed", "filter", fc.Name, "error", err)
			continue
		}

		step.Action = filterResult.Action
		step.Log = filterResult.Log
		step.Duration = time.Since(stepStart)
		recordStep(step)

		e.logger.Debug("filter executed",
			"filter", fc.Name,
			"action", filterResult.Action,
			"duration", step.Duration,
		)

		// Process skip_filters
		for _, skipName := range filterResult.SkipFilters {
			skipSet[skipName] = true
		}

		// Handle action results. A terminal action breaks out of the loop so
		// that the single terminal observation below runs exactly once.
		switch filterResult.Action {
		case ActionReject:
			result.FinalAction = ActionReject
			result.RejectMsg = filterResult.RejectMsg
			break Loop

		case ActionQuarantine:
			result.FinalAction = ActionQuarantine
			break Loop

		case ActionDiscard:
			result.FinalAction = ActionDiscard
			break Loop

		case ActionDefer:
			result.FinalAction = ActionDefer
			break Loop

		case ActionContinue:
			// If transform filter, replace the email
			if filterResult.Type == FilterTypeTransform && filterResult.Message != nil {
				result.FinalEmail = filterResult.Message
			}
		}
	}

	result.Duration = time.Since(start)
	// One terminal observation per message, once the final action is known. A
	// non-continue outcome breaks the loop immediately after recording its step,
	// so the last recorded step IS the terminal step — hand it to the observer so
	// it can derive the reason_code. A continue outcome has no terminal step.
	var terminal *StepResult
	if result.FinalAction != ActionContinue && len(result.Steps) > 0 {
		terminal = &result.Steps[len(result.Steps)-1]
	}
	e.observer.ObserveTerminal(pipeline.Direction, result.FinalAction, terminal)
	return result, nil
}

// TestFilter runs a single filter against an email (for testing/debugging).
func (e *Engine) TestFilter(ctx context.Context, filterName string, config []byte, email *EmailJSON) (*FilterResult, error) {
	filter, err := e.registry.Create(filterName, config)
	if err != nil {
		return nil, fmt.Errorf("create filter %s: %w", filterName, err)
	}
	return filter.Execute(ctx, email)
}
