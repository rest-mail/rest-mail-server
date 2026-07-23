// Package observer provides the concrete pipeline.Observer that translates
// pipeline execution events into Prometheus metrics.
//
// It is deliberately a separate package from internal/metrics so that
// internal/metrics stays a leaf (Prometheus definitions only) and
// internal/pipeline never imports metrics: this package is the one place that
// depends on both. Every method is lock-free (atomic counter increments only)
// and safe to call inline on the mail-processing hot path.
package observer

import (
	"github.com/restmail/restmail/internal/metrics"
	"github.com/restmail/restmail/internal/pipeline"
)

// metricsObserver implements pipeline.Observer by emitting bounded-cardinality
// Prometheus metrics.
type metricsObserver struct{}

// New returns a pipeline.Observer that records pipeline metrics.
func New() pipeline.Observer { return metricsObserver{} }

// ObserveStep records per-step metrics: the stage decision, the filter
// duration, and — when the step is an auth verification filter — the auth
// verdict. All label values are collapsed to bounded enums.
func (metricsObserver) ObserveStep(step pipeline.StepResult) {
	filter := boundedFilter(step.FilterName)
	metrics.PipelineStageDecisions.WithLabelValues(filter, stepAction(step)).Inc()
	metrics.PipelineFilterDuration.WithLabelValues(filter).Observe(step.Duration.Seconds())

	if mech, ok := authMechanism(step.Log.Filter); ok {
		metrics.AuthVerdict.WithLabelValues(mech, normalizeAuthResult(step.Log.Result)).Inc()
	}
}

// ObserveTerminal records the terminal outcome for one message.
func (metricsObserver) ObserveTerminal(direction string, action pipeline.Action) {
	metrics.PipelineTerminal.WithLabelValues(directionLabel(direction), terminalOutcome(direction, action)).Inc()
}

// builtinFilters is the bounded allowlist of built-in filter names. It mirrors
// the filters registered via init() in internal/pipeline/filters and the
// DB-backed filters registered in internal/api/routes.go. Any name NOT in this
// set (user-defined custom filters, unknown names) collapses to "custom" so the
// `filter` label can never grow unbounded. Keep in sync when a built-in filter
// is added.
var builtinFilters = map[string]struct{}{
	"spf_check":           {},
	"dkim_verify":         {},
	"dkim_sign":           {},
	"dmarc_check":         {},
	"arc_verify":          {},
	"arc_seal":            {},
	"clamav":              {},
	"rspamd":              {},
	"greylist":            {},
	"vacation":            {},
	"domain_allowlist":    {},
	"contact_whitelist":   {},
	"recipient_check":     {},
	"sender_verify":       {},
	"header_cleanup":      {},
	"header_validate":     {},
	"extract_attachments": {},
	"duplicate":           {},
	"rate_limit":          {},
	"size_check":          {},
	"sieve":               {},
	"javascript":          {},
	"webhook":             {},
}

// boundedFilter collapses any non-built-in filter name to the literal "custom",
// enforcing bounded cardinality for every `filter`-labeled metric.
func boundedFilter(name string) string {
	if _, ok := builtinFilters[name]; ok {
		return name
	}
	return "custom"
}

// stepAction maps a finalized step to its bounded action label:
// skipped, error, or the step's pipeline action (continue/reject/quarantine/
// discard/defer).
func stepAction(step pipeline.StepResult) string {
	switch {
	case step.Skipped:
		return "skipped"
	case step.Error != "":
		return "error"
	default:
		return string(step.Action)
	}
}

// authMechanism maps an auth *verification* filter's self-reported name (from
// FilterLog.Filter) to its mechanism. Signing/sealing filters (dkim_sign,
// arc_seal) are not verdicts and return ok=false.
func authMechanism(filterLogName string) (string, bool) {
	switch filterLogName {
	case "spf_check":
		return "spf", true
	case "dkim_verify":
		return "dkim", true
	case "dmarc_check":
		return "dmarc", true
	case "arc_verify":
		return "arc", true
	default:
		return "", false
	}
}

// authResults is the bounded set of recognized auth verdict values.
var authResults = map[string]struct{}{
	"pass":      {},
	"fail":      {},
	"none":      {},
	"neutral":   {},
	"softfail":  {},
	"temperror": {},
	"permerror": {},
}

// normalizeAuthResult bounds an auth result to the recognized set, mapping
// anything else (e.g. "signed", "skipped", empty) to "other".
func normalizeAuthResult(result string) string {
	if _, ok := authResults[result]; ok {
		return result
	}
	return "other"
}

// directionLabel bounds the pipeline direction to inbound/outbound, defaulting
// unknown/empty to inbound.
func directionLabel(direction string) string {
	if direction == "outbound" {
		return "outbound"
	}
	return "inbound"
}

// terminalOutcome maps a final pipeline action to its terminal outcome. For a
// Continue action the routing distinguishes an outbound submission (queued)
// from an inbound local delivery (delivered).
func terminalOutcome(direction string, action pipeline.Action) string {
	switch action {
	case pipeline.ActionReject:
		return "rejected"
	case pipeline.ActionQuarantine:
		return "quarantined"
	case pipeline.ActionDiscard:
		return "discarded"
	case pipeline.ActionDefer:
		return "deferred"
	case pipeline.ActionContinue:
		if direction == "outbound" {
			return "queued"
		}
		return "delivered"
	default:
		// Actions are a closed enum, so this is unreachable; fall back to a
		// bounded value rather than emit an unbounded label.
		return "delivered"
	}
}
