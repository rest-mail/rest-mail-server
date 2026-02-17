package pipeline

import (
	"encoding/json"
	"time"
)

// DefaultInboundPipeline returns a sensible default inbound pipeline
// configuration that gets applied when a new domain is created. It includes
// authentication checks, allowlists, greylisting, header validation,
// recipient verification, attachment extraction, and Sieve filtering.
func DefaultInboundPipeline(domainID uint) *PipelineConfig {
	now := time.Now()
	return &PipelineConfig{
		DomainID:  domainID,
		Direction: "inbound",
		Filters:   defaultInboundFilters(),
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// DefaultOutboundPipeline returns a sensible default outbound pipeline
// configuration that gets applied when a new domain is created. It includes
// sender verification, rate limiting, header cleanup, and DKIM signing.
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
		filters = defaultInboundFilters()
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
			Name:        "dmarc_check",
			Type:        FilterTypeAction,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{"fail_action": "quarantine"}`),
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
	}
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
			Config:  json.RawMessage(`{"per_sender_per_hour": 100}`),
		},
		{
			Name:    "header_cleanup",
			Type:    FilterTypeTransform,
			Enabled: true,
			Config:  json.RawMessage(`{}`),
		},
		{
			Name:        "dkim_sign",
			Type:        FilterTypeTransform,
			Enabled:     true,
			Unskippable: true,
			Config:      json.RawMessage(`{}`),
		},
	}
}
