package observer

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/restmail/restmail/internal/metrics"
	"github.com/restmail/restmail/internal/pipeline"
)

// ── helper unit tests ────────────────────────────────────────────────

func TestBoundedFilter(t *testing.T) {
	if got := boundedFilter("spf_check"); got != "spf_check" {
		t.Errorf("built-in collapsed: got %q", got)
	}
	if got := boundedFilter("my_weird_custom_filter"); got != "custom" {
		t.Errorf("custom not collapsed: got %q, want custom", got)
	}
	if got := boundedFilter(""); got != "custom" {
		t.Errorf("empty name: got %q, want custom", got)
	}
}

func TestStepAction(t *testing.T) {
	cases := []struct {
		name string
		step pipeline.StepResult
		want string
	}{
		{"skipped", pipeline.StepResult{Skipped: true, Action: pipeline.ActionContinue}, "skipped"},
		{"error", pipeline.StepResult{Error: "boom"}, "error"},
		{"continue", pipeline.StepResult{Action: pipeline.ActionContinue}, "continue"},
		{"reject", pipeline.StepResult{Action: pipeline.ActionReject}, "reject"},
		{"skipped beats error", pipeline.StepResult{Skipped: true, Error: "boom"}, "skipped"},
	}
	for _, c := range cases {
		if got := stepAction(c.step); got != c.want {
			t.Errorf("%s: stepAction = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAuthMechanism(t *testing.T) {
	for name, wantMech := range map[string]string{
		"spf_check":   "spf",
		"dkim_verify": "dkim",
		"dmarc_check": "dmarc",
		"arc_verify":  "arc",
	} {
		mech, ok := authMechanism(name)
		if !ok || mech != wantMech {
			t.Errorf("authMechanism(%q) = %q,%v want %q,true", name, mech, ok, wantMech)
		}
	}
	// Signing/sealing and non-auth filters are not verdicts.
	for _, name := range []string{"dkim_sign", "arc_seal", "greylist", ""} {
		if _, ok := authMechanism(name); ok {
			t.Errorf("authMechanism(%q) unexpectedly reported an auth verdict", name)
		}
	}
}

func TestNormalizeAuthResult(t *testing.T) {
	for _, r := range []string{"pass", "fail", "none", "neutral", "softfail", "temperror", "permerror"} {
		if got := normalizeAuthResult(r); got != r {
			t.Errorf("normalizeAuthResult(%q) = %q, want unchanged", r, got)
		}
	}
	for _, r := range []string{"signed", "skipped", "weird", ""} {
		if got := normalizeAuthResult(r); got != "other" {
			t.Errorf("normalizeAuthResult(%q) = %q, want other", r, got)
		}
	}
}

func TestTerminalOutcome(t *testing.T) {
	cases := []struct {
		dir    string
		action pipeline.Action
		want   string
	}{
		{"inbound", pipeline.ActionContinue, "delivered"},
		{"outbound", pipeline.ActionContinue, "queued"},
		{"inbound", pipeline.ActionReject, "rejected"},
		{"inbound", pipeline.ActionQuarantine, "quarantined"},
		{"inbound", pipeline.ActionDiscard, "discarded"},
		{"inbound", pipeline.ActionDefer, "deferred"},
	}
	for _, c := range cases {
		if got := terminalOutcome(c.dir, c.action); got != c.want {
			t.Errorf("terminalOutcome(%q,%q) = %q, want %q", c.dir, c.action, got, c.want)
		}
	}
	if got := directionLabel("nonsense"); got != "inbound" {
		t.Errorf("directionLabel default = %q, want inbound", got)
	}
}

// ── engine-driven integration against the real (default-registry) metrics ──

// fakeFilter is a configurable pipeline.Filter for driving the engine.
type fakeFilter struct {
	name      string
	typ       pipeline.FilterType
	action    pipeline.Action
	logFilter string
	logResult string
}

func (f fakeFilter) Name() string { return f.name }
func (f fakeFilter) Type() pipeline.FilterType {
	if f.typ == "" {
		return pipeline.FilterTypeAction
	}
	return f.typ
}
func (f fakeFilter) Execute(context.Context, *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	return &pipeline.FilterResult{
		Type:   f.Type(),
		Action: f.action,
		Log:    pipeline.FilterLog{Filter: f.logFilter, Result: f.logResult},
	}, nil
}

func register(reg *pipeline.Registry, f fakeFilter) {
	reg.Register(f.name, func([]byte) (pipeline.Filter, error) { return f, nil })
}

func counter(t *testing.T, c *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	return testutil.ToFloat64(c.WithLabelValues(labels...))
}

func histCount(t *testing.T, h *prometheus.HistogramVec, labels ...string) uint64 {
	t.Helper()
	m := &dto.Metric{}
	if err := h.WithLabelValues(labels...).(prometheus.Metric).Write(m); err != nil {
		t.Fatalf("write histogram: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

// A full inbound pipeline run must increment stage_decisions per step (with the
// right action label), bump auth_verdict for auth filters, record filter
// duration, collapse custom filter names to "custom", and record a terminal
// "delivered" outcome.
func TestObserver_InboundRun(t *testing.T) {
	reg := pipeline.NewRegistry()
	register(reg, fakeFilter{name: "spf_check", action: pipeline.ActionContinue, logFilter: "spf_check", logResult: "pass"})
	register(reg, fakeFilter{name: "my_custom", action: pipeline.ActionContinue, logFilter: "my_custom", logResult: "whatever"})

	eng := pipeline.NewEngine(reg, nil, New())
	cfg := &pipeline.PipelineConfig{
		Direction: "inbound",
		Filters: []pipeline.FilterConfig{
			{Name: "spf_check", Enabled: true},
			{Name: "my_custom", Enabled: true},
		},
	}

	before := struct {
		spfDecision, customDecision, spfVerdict, terminalDelivered float64
		spfHist, customHist                                        uint64
	}{
		spfDecision:       counter(t, metrics.PipelineStageDecisions, "spf_check", "continue"),
		customDecision:    counter(t, metrics.PipelineStageDecisions, "custom", "continue"),
		spfVerdict:        counter(t, metrics.AuthVerdict, "spf", "pass"),
		terminalDelivered: counter(t, metrics.PipelineTerminal, "inbound", "delivered"),
		spfHist:           histCount(t, metrics.PipelineFilterDuration, "spf_check"),
		customHist:        histCount(t, metrics.PipelineFilterDuration, "custom"),
	}

	if _, err := eng.Execute(context.Background(), cfg, &pipeline.EmailJSON{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := counter(t, metrics.PipelineStageDecisions, "spf_check", "continue") - before.spfDecision; got != 1 {
		t.Errorf("stage_decisions{spf_check,continue} delta = %v, want 1", got)
	}
	if got := counter(t, metrics.PipelineStageDecisions, "custom", "continue") - before.customDecision; got != 1 {
		t.Errorf("stage_decisions{custom,continue} delta = %v, want 1 (custom collapse)", got)
	}
	if got := counter(t, metrics.AuthVerdict, "spf", "pass") - before.spfVerdict; got != 1 {
		t.Errorf("auth_verdict{spf,pass} delta = %v, want 1", got)
	}
	if got := counter(t, metrics.PipelineTerminal, "inbound", "delivered") - before.terminalDelivered; got != 1 {
		t.Errorf("terminal{inbound,delivered} delta = %v, want 1", got)
	}
	if got := histCount(t, metrics.PipelineFilterDuration, "spf_check") - before.spfHist; got != 1 {
		t.Errorf("filter_duration{spf_check} sample delta = %d, want 1", got)
	}
	if got := histCount(t, metrics.PipelineFilterDuration, "custom") - before.customHist; got != 1 {
		t.Errorf("filter_duration{custom} sample delta = %d, want 1", got)
	}
}

// A rejecting run must record a terminal "rejected" outcome and the reject
// stage decision.
func TestObserver_RejectingRun(t *testing.T) {
	reg := pipeline.NewRegistry()
	register(reg, fakeFilter{name: "greylist", action: pipeline.ActionReject, logFilter: "greylist", logResult: "reject"})

	eng := pipeline.NewEngine(reg, nil, New())
	cfg := &pipeline.PipelineConfig{
		Direction: "inbound",
		Filters:   []pipeline.FilterConfig{{Name: "greylist", Enabled: true}},
	}

	beforeDecision := counter(t, metrics.PipelineStageDecisions, "greylist", "reject")
	beforeTerminal := counter(t, metrics.PipelineTerminal, "inbound", "rejected")

	if _, err := eng.Execute(context.Background(), cfg, &pipeline.EmailJSON{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := counter(t, metrics.PipelineStageDecisions, "greylist", "reject") - beforeDecision; got != 1 {
		t.Errorf("stage_decisions{greylist,reject} delta = %v, want 1", got)
	}
	if got := counter(t, metrics.PipelineTerminal, "inbound", "rejected") - beforeTerminal; got != 1 {
		t.Errorf("terminal{inbound,rejected} delta = %v, want 1", got)
	}
}

// An outbound continue must record a terminal "queued" outcome.
func TestObserver_OutboundQueued(t *testing.T) {
	reg := pipeline.NewRegistry()
	register(reg, fakeFilter{name: "dkim_sign", action: pipeline.ActionContinue, logFilter: "dkim_sign", logResult: "signed"})

	eng := pipeline.NewEngine(reg, nil, New())
	cfg := &pipeline.PipelineConfig{
		Direction: "outbound",
		Filters:   []pipeline.FilterConfig{{Name: "dkim_sign", Enabled: true}},
	}

	beforeQueued := counter(t, metrics.PipelineTerminal, "outbound", "queued")
	// dkim_sign is not a verdict filter; auth_verdict must NOT move.
	beforeVerdict := counter(t, metrics.AuthVerdict, "dkim", "other")

	if _, err := eng.Execute(context.Background(), cfg, &pipeline.EmailJSON{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got := counter(t, metrics.PipelineTerminal, "outbound", "queued") - beforeQueued; got != 1 {
		t.Errorf("terminal{outbound,queued} delta = %v, want 1", got)
	}
	if got := counter(t, metrics.AuthVerdict, "dkim", "other") - beforeVerdict; got != 0 {
		t.Errorf("auth_verdict moved for signing filter: delta = %v, want 0", got)
	}
}
