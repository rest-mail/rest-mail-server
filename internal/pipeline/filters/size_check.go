package filters

import (
	"context"
	"encoding/json"
	"fmt"

	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
)

type sizeCheckConfig struct {
	MaxSizeBytes int64 `json:"max_size_bytes"`
}

type sizeCheckFilter struct {
	maxSize int64
}

func init() {
	pipeline.DefaultRegistry.Register("size_check", NewSizeCheck)
}

func NewSizeCheck(config []byte) (pipeline.Filter, error) {
	cfg := sizeCheckConfig{MaxSizeBytes: 25 * 1024 * 1024} // 25MB default
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}
	return &sizeCheckFilter{maxSize: cfg.MaxSizeBytes}, nil
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
