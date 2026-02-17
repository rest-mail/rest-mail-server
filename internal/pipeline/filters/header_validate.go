package filters

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
)

type headerValidateConfig struct {
	RequireFrom      bool `json:"require_from"`
	RequireDate      bool `json:"require_date"`
	RequireMessageID bool `json:"require_message_id"`
	RejectInjection  bool `json:"reject_injection"`
}

type headerValidateFilter struct {
	cfg headerValidateConfig
}

func init() {
	pipeline.DefaultRegistry.Register("header_validate", NewHeaderValidate)
}

func NewHeaderValidate(config []byte) (pipeline.Filter, error) {
	cfg := headerValidateConfig{
		RequireFrom:     true,
		RequireDate:     true,
		RejectInjection: true,
	}
	if len(config) > 0 {
		json.Unmarshal(config, &cfg)
	}
	return &headerValidateFilter{cfg: cfg}, nil
}

func (f *headerValidateFilter) Name() string             { return "header_validate" }
func (f *headerValidateFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *headerValidateFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	var issues []string

	if f.cfg.RequireFrom && len(email.Headers.From) == 0 {
		issues = append(issues, "missing From header")
	}

	if f.cfg.RequireDate && email.Headers.Date == "" {
		issues = append(issues, "missing Date header")
	}

	if f.cfg.RequireMessageID && email.Headers.MessageID == "" {
		issues = append(issues, "missing Message-ID header")
	}

	// Check for header injection attempts
	if f.cfg.RejectInjection {
		for key, values := range email.Headers.Raw {
			for _, v := range values {
				if strings.Contains(v, "\r\n") || strings.Contains(v, "\n") {
					issues = append(issues, "header injection in "+key)
				}
			}
		}
	}

	if len(issues) > 0 {
		return &pipeline.FilterResult{
			Type:      pipeline.FilterTypeAction,
			Action:    pipeline.ActionReject,
			RejectMsg: "550 Invalid headers: " + strings.Join(issues, ", "),
			Log: pipeline.FilterLog{
				Filter: "header_validate",
				Result: "reject",
				Detail: strings.Join(issues, "; "),
			},
		}, nil
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "header_validate",
			Result: "pass",
			Detail: "all headers valid",
		},
	}, nil
}
