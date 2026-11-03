package pipeline

import (
	"encoding/json"
	"time"
)

// DefaultInboundPipeline returns a sensible default inbound pipeline
// configuration that gets applied when a new domain is created. It includes
// authentication checks, allowlists, greylisting, header validation,
// recipient verification, attachment extraction, Sieve filtering and the
// account's vacation reply.
func DefaultInboundPipeline(domainID uint) *PipelineConfig {
	now := time.Now()
	return &PipelineConfig{
		DomainID:  domainID,
		Direction: "inbound",
		Filters:   WithDMARCReportsStep(defaultInboundFilters()),
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// DefaultOutboundPipeline returns a sensible default outbound pipeline
// configuration that gets applied when a new domain is created. It includes
// sender verification, rate limiting and header cleanup. DKIM signing is not a
// filter: the finished message is signed over its wire bytes after the pipeline
// (internal/outbound.SignDKIM).
func DefaultOutboundPipeline(domainID uint) *PipelineConfig {
	now := time.Now()
	return &PipelineConfig{
		DomainID:  domainID,
		Direction: "outbound",
		Filters:   defaultOutboundFilters(),
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// DefaultPipelineJSON returns the default filters array as JSON for storing
// in the database pipelines table. The direction parameter must be "inbound"
// or "outbound". If the direction is not recognised, an empty JSON array is
// returned.
func DefaultPipelineJSON(direction string) json.RawMessage {
	var filters []FilterConfig
	switch direction {
	case "inbound":
		filters = WithDMARCReportsStep(defaultInboundFilters())
	case "outbound":
		filters = defaultOutboundFilters()
	default:
		return json.RawMessage(`[]`)
	}

	data, err := json.Marshal(filters)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return data
}

func defaultInboundFilters() []FilterConfig {
	return []FilterConfig{
		{
			Name:        "size_check",
			Type:        FilterTypeAction,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{"max_size_mb": 25}`),
		},
		{
			Name:        "spf_check",
			Type:        FilterTypeAction,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{"fail_action": "tag"}`),
		},
		{
			Name:        "dkim_verify",
			Type:        FilterTypeTransform,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{"fail_action": "tag"}`),
		},
		{
			Name:        "arc_verify",
			Type:        FilterTypeTransform,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{}`),
		},
		{
			Name:        "dmarc_check",
			Type:        FilterTypeAction,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{}`),
		},
		{
			Name:    "domain_allowlist",
			Type:    FilterTypeAction,
			Enabled: true,
			Config:  json.RawMessage(`{}`),
		},
		{
			Name:    "contact_whitelist",
			Type:    FilterTypeAction,
			Enabled: true,
			Config:  json.RawMessage(`{}`),
		},
		{
			Name:    "greylist",
			Type:    FilterTypeAction,
			Enabled: true,
			Config:  json.RawMessage(`{"delay_minutes": 5, "ttl_days": 36}`),
		},
		{
			Name:    "header_validate",
			Type:    FilterTypeAction,
			Enabled: true,
			Config:  json.RawMessage(`{}`),
		},
		{
			Name:        "recipient_check",
			Type:        FilterTypeAction,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{}`),
		},
		{
			Name:    "extract_attachments",
			Type:    FilterTypeTransform,
			Enabled: true,
			Config:  json.RawMessage(`{"storage_dir": "/data/attachments"}`),
		},
		{
			Name:    "sieve",
			Type:    FilterTypeTransform,
			Enabled: true,
			Config:  json.RawMessage(`{}`),
		},
		// The account's vacation reply, after Sieve, so a message the script rejects
		// or discards is not answered. Without it a reply set in the apps is never
		// sent (#772).
		{
			Name:    "vacation",
			Type:    FilterTypeAction,
			Enabled: true,
			Config:  json.RawMessage(`{"days": 7}`),
		},
	}
}

// WithVacationStep returns an inbound pipeline's steps with the account's
// vacation reply on, after Sieve, so a reply set in the apps is always sent
// whatever the domain's stored pipeline says (#772), and a message the script
// rejects or discards is not answered. It is one step, so one reply: a stored
// step's own settings are kept.
func WithVacationStep(steps []FilterConfig) []FilterConfig {
	vacation := FilterConfig{Name: "vacation", Type: FilterTypeAction, Enabled: true, Config: json.RawMessage(`{"days": 7}`)}
	out := make([]FilterConfig, 0, len(steps)+1)
	at := -1
	for _, step := range steps {
		if step.Name == "vacation" {
			if len(step.Config) > 0 {
				vacation.Config = step.Config
			}
			continue
		}
		out = append(out, step)
		if step.Name == "sieve" {
			at = len(out)
		}
	}
	if at < 0 {
		return append(out, vacation)
	}
	return append(out[:at:at], append([]FilterConfig{vacation}, out[at:]...)...)
}

func WithDMARCReportsStep(steps []FilterConfig) []FilterConfig {
	required, content := []FilterConfig{}, []FilterConfig{}
	for _, step := range steps {
		if step.Name == "dmarc_reports_received" {
			continue
		}
		if requiredChecks[step.Name] {
			required = append(required, step)
		} else {
			content = append(content, step)
		}
	}
	required = append(required, FilterConfig{Name: "dmarc_reports_received", Type: FilterTypeAction, Enabled: true, Unskippable: true, Config: json.RawMessage(`{}`)})
	return append(required, content...)
}

func defaultOutboundFilters() []FilterConfig {
	return []FilterConfig{
		{
			Name:        "sender_verify",
			Type:        FilterTypeAction,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{}`),
		},
		{
			Name:    "rate_limit",
			Type:    FilterTypeAction,
			Enabled: true,
			Config:  json.RawMessage(`{"max_per_hour": 100, "max_per_minute": 20}`),
		},
		{
			Name:    "header_cleanup",
			Type:    FilterTypeTransform,
			Enabled: true,
			Config:  json.RawMessage(`{}`),
		},
	}
}
