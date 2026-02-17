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
}

// NewEngine creates a pipeline execution engine.
func NewEngine(registry *Registry, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		registry: registry,
		logger:   logger,
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
			result.Steps = append(result.Steps, step)
			e.logger.Debug("filter skipped", "filter", fc.Name, "reason", step.SkipReason)
			continue
		}

		// Create the filter instance
		filter, err := e.registry.Create(fc.Name, fc.Config)
		if err != nil {
			step.Error = fmt.Sprintf("create filter: %v", err)
			step.Duration = time.Since(stepStart)
			result.Steps = append(result.Steps, step)
			e.logger.Error("failed to create filter", "filter", fc.Name, "error", err)
			continue // Skip filters that fail to instantiate
		}

		// Execute the filter
		filterResult, err := filter.Execute(ctx, result.FinalEmail)
		if err != nil {
			step.Error = fmt.Sprintf("execute: %v", err)
			step.Duration = time.Since(stepStart)
			result.Steps = append(result.Steps, step)
			e.logger.Error("filter execution failed", "filter", fc.Name, "error", err)
			continue
		}

		step.Action = filterResult.Action
		step.Log = filterResult.Log
		step.Duration = time.Since(stepStart)
		result.Steps = append(result.Steps, step)

		e.logger.Debug("filter executed",
			"filter", fc.Name,
			"action", filterResult.Action,
			"duration", step.Duration,
		)

		// Process skip_filters
		for _, skipName := range filterResult.SkipFilters {
			skipSet[skipName] = true
		}

		// Handle action results
		switch filterResult.Action {
		case ActionReject:
			result.FinalAction = ActionReject
			result.RejectMsg = filterResult.RejectMsg
			result.Duration = time.Since(start)
			return result, nil

		case ActionQuarantine:
			result.FinalAction = ActionQuarantine
			result.Duration = time.Since(start)
			return result, nil

		case ActionDiscard:
			result.FinalAction = ActionDiscard
			result.Duration = time.Since(start)
			return result, nil

		case ActionDefer:
			result.FinalAction = ActionDefer
			result.Duration = time.Since(start)
			return result, nil

		case ActionContinue:
			// If transform filter, replace the email
			if filterResult.Type == FilterTypeTransform && filterResult.Message != nil {
				result.FinalEmail = filterResult.Message
			}
		}
	}

	result.FinalAction = ActionContinue
	result.Duration = time.Since(start)
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
