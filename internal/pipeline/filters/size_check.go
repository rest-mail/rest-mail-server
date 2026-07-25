package filters

import (
	"context"
	"encoding/json"
	"fmt"

	rmconfig "github.com/restmail/restmail/internal/config"
	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
)

type sizeCheckConfig struct {
	// MaxSizeBytes wins when both keys are present. MaxSizeMB is honored
	// because the default inbound pipeline template (and therefore every
	// seeded pipeline row) configures this filter as {"max_size_mb": 25};
	// before it was recognised here, admin edits to that value were silently
	// ignored and the compiled-in 25 MB default applied regardless.
	MaxSizeBytes int64 `json:"max_size_bytes"`
	MaxSizeMB    int64 `json:"max_size_mb"`
}

type sizeCheckFilter struct {
	maxSize int64
}

func init() {
	pipeline.DefaultRegistry.Register("size_check", NewSizeCheck)
}

func NewSizeCheck(config []byte) (pipeline.Filter, error) {
	var cfg sizeCheckConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	maxSize := cfg.MaxSizeBytes
	if maxSize <= 0 && cfg.MaxSizeMB > 0 {
		maxSize = cfg.MaxSizeMB * 1024 * 1024
	}
	if maxSize <= 0 {
		// Align the unconfigured default with the SMTP ingress limit. The old
		// 25 MB default was unreachable: the ingress rejects anything over
		// DefaultSMTPMaxMessageSize before the pipeline runs, so a message could
		// never reach this filter at a size above it (issue #201).
		maxSize = rmconfig.DefaultSMTPMaxMessageSize
	}
	return &sizeCheckFilter{maxSize: maxSize}, nil
}

func (f *sizeCheckFilter) Name() string               { return "size_check" }
func (f *sizeCheckFilter) Type() pipeline.FilterType   { return pipeline.FilterTypeAction }

func (f *sizeCheckFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	size := rmime.EstimateSize(email)

	if size > f.maxSize {
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: fmt.Sprintf("552 Message too large: %d bytes exceeds limit of %d", size, f.maxSize),
			Log: pipeline.FilterLog{
				Filter: "size_check",
				Result: "reject",
				Detail: fmt.Sprintf("size=%d max=%d", size, f.maxSize),
			},
		}, nil
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "size_check",
			Result: "pass",
			Detail: fmt.Sprintf("size=%d max=%d", size, f.maxSize),
		},
	}, nil
}
