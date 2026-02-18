package pipeline

import "context"

// ExternalAdapter defines the interface for external scanning services
// like rspamd, ClamAV, etc. Each adapter communicates with an external
// service via HTTP or socket and returns scan results.
type ExternalAdapter interface {
	// Name returns the adapter identifier (e.g., "rspamd", "clamav").
	Name() string
	// Scan sends the email content to the external service and returns results.
	Scan(ctx context.Context, email *EmailJSON) (*AdapterResult, error)
	// Healthy checks if the external service is reachable.
	Healthy(ctx context.Context) bool
}

// AdapterResult holds the outcome of an external scan.
type AdapterResult struct {
	Clean     bool              // true if message passed scanning
	Score     float64           // spam score (rspamd) or 0/1 for virus
	Action    Action            // recommended action
	Details   string            // human-readable description
	Headers   map[string]string // headers to add to the email
	RejectMsg string            // rejection message if action is reject
}
