package filters

import (
	"context"

	"github.com/restmail/restmail/internal/pipeline"
)

// headerCleanupFilter removes internal headers and adds standard outbound headers.
type headerCleanupFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("header_cleanup", NewHeaderCleanup)
}

func NewHeaderCleanup(_ []byte) (pipeline.Filter, error) {
	return &headerCleanupFilter{}, nil
}

func (f *headerCleanupFilter) Name() string             { return "header_cleanup" }
func (f *headerCleanupFilter) Type() pipeline.FilterType { return pipeline.FilterTypeTransform }

func (f *headerCleanupFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	modified := *email

	// Remove internal/sensitive headers
	internalHeaders := []string{
		"X-RestMail-Internal",
		"X-Pipeline-ID",
		"X-Queue-ID",
		"Bcc",
	}

	if modified.Headers.Raw != nil {
		for _, h := range internalHeaders {
			delete(modified.Headers.Raw, h)
		}
	}

	// Clear BCC from structured headers (should be envelope-only)
	modified.Headers.Bcc = nil

	return &pipeline.FilterResult{
		Type:    pipeline.FilterTypeTransform,
		Action:  pipeline.ActionContinue,
		Message: &modified,
		Log: pipeline.FilterLog{
			Filter: "header_cleanup",
			Result: "transformed",
			Detail: "removed internal headers, cleared BCC",
		},
	}, nil
}
