package pipeline

import "context"

// Filter is the interface that all pipeline filters must implement.
// Both action filters and transform filters implement this interface.
// The difference is in the FilterResult they return:
//   - Action filters: Result.Type = "action", Result.Message is nil
//   - Transform filters: Result.Type = "transform", Result.Message is non-nil
type Filter interface {
	// Name returns the unique identifier for this filter.
	Name() string

	// Type returns whether this is an action or transform filter.
	Type() FilterType

	// Execute runs the filter against the given email.
	// The context carries request-scoped values (deadline, cancellation, metadata).
	Execute(ctx context.Context, email *EmailJSON) (*FilterResult, error)
}

// FilterFactory creates a filter instance from its JSON configuration.
type FilterFactory func(config []byte) (Filter, error)
