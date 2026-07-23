package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/restmail/restmail/internal/api/respond"
)

// Pipeline-observability analytics (PR5). This endpoint turns the durable
// pipeline_rollups aggregate (written by the rollup worker, which snapshots the
// always-on, never-sampled Prometheus counters into time buckets) into a
// windowed message-lifecycle FUNNEL — received → auth verdicts → stage
// decisions → terminal outcomes, plus the top reject reasons — WITHOUT needing
// an external Prometheus/Grafana. That self-containment is the whole point: a
// deployer gets real windowed history straight from its own database.
//
// The window is summed in SQL against pipeline_rollups (one GROUP BY over the
// bounded label series); Go only reshapes the already-collapsed per-series
// totals into the funnel. Rollup rows are derived from the inline counters, so
// these numbers are exact regardless of per-message trace sampling/pruning.

// Prometheus counter families the funnel is built from. These mirror the
// rollup worker's tracked set (internal/rollup) — the durable bucket rows carry
// exactly these metric_name values. Duplicated as string literals here (rather
// than imported) so this read path does not depend on rollup internals.
const (
	metricMessagesReceived = "restmail_messages_received_total"
	metricStageDecisions   = "restmail_pipeline_stage_decisions_total"
	metricTerminal         = "restmail_pipeline_terminal_total"
	metricAuthVerdict      = "restmail_auth_verdict_total"
	metricRejectReason     = "restmail_pipeline_reject_reason_total"
)

// defaultAnalyticsWindow is the look-back used when neither ?window nor ?since
// is supplied. maxTopRejectReasons caps the reason breakdown (the enum is small
// and bounded, so this is a safety net, not a real limit).
const (
	defaultAnalyticsWindow = 24 * time.Hour
	maxTopRejectReasons    = 12
)

// TransportCount is a received-message count for one transport (tls|plaintext).
type TransportCount struct {
	Transport string  `json:"transport"`
	Count     float64 `json:"count"`
}

// AuthVerdictCount is an auth-result count for one (mechanism, result) pair.
type AuthVerdictCount struct {
	Mechanism string  `json:"mechanism"`
	Result    string  `json:"result"`
	Count     float64 `json:"count"`
}

// StageDecisionCount is a per-filter decision count for one (filter, action).
type StageDecisionCount struct {
	Filter string  `json:"filter"`
	Action string  `json:"action"`
	Count  float64 `json:"count"`
}

// TerminalCount is a terminal-outcome count for one (direction, outcome).
type TerminalCount struct {
	Direction string  `json:"direction"`
	Outcome   string  `json:"outcome"`
	Count     float64 `json:"count"`
}

// RejectReasonCount is a reject count for one bounded reason_code.
type RejectReasonCount struct {
	ReasonCode string  `json:"reason_code"`
	Count      float64 `json:"count"`
}

// PipelineFunnel is the message-lifecycle funnel over a window: the stages a
// message passes through, each broken down by its bounded label set.
type PipelineFunnel struct {
	Received         []TransportCount     `json:"received"`
	AuthVerdicts     []AuthVerdictCount   `json:"auth_verdicts"`
	StageDecisions   []StageDecisionCount `json:"stage_decisions"`
	TerminalOutcomes []TerminalCount      `json:"terminal_outcomes"`
	TopRejectReasons []RejectReasonCount  `json:"top_reject_reasons"`
}

// AnalyticsWindow describes the time span the funnel covers.
type AnalyticsWindow struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
	// Label echoes the ?window value that produced Since (empty when ?since was
	// given instead).
	Label string `json:"label,omitempty"`
}

// PipelineAnalyticsResponse is the analytics endpoint payload: the durable
// windowed funnel from the rollups, plus an optional live snapshot of the same
// shape read from the in-process registry (since THIS process started).
type PipelineAnalyticsResponse struct {
	Window AnalyticsWindow `json:"window"`
	Funnel PipelineFunnel  `json:"funnel"`
	// LiveTotals are the same counters as gathered from the in-process Prometheus
	// registry — cumulative since process start, NOT windowed. Present as a
	// live-ops convenience; absent if the registry could not be gathered.
	LiveTotals *PipelineFunnel `json:"live_totals,omitempty"`
}

// seriesSum is one collapsed counter series: the metric family, its decoded
// bounded label set, and the summed value over the window. Both the rollup
// (DB) path and the live (registry) path reduce to this before buildFunnel, so
// a single pure reshaping function serves both.
type seriesSum struct {
	metric string
	labels map[string]string
	total  float64
}

// GetPipelineAnalytics returns the windowed pipeline funnel from pipeline_rollups.
// GET /api/v1/admin/pipelines/analytics?window=24h  (or ?since=RFC3339)
func (h *StatsHandler) GetPipelineAnalytics(w http.ResponseWriter, r *http.Request) {
	win := parseAnalyticsWindow(r, time.Now())

	series, err := h.rollupSeries(win.Since)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to aggregate pipeline analytics")
		return
	}

	resp := PipelineAnalyticsResponse{
		Window: win,
		Funnel: buildFunnel(series),
	}
	// Best-effort live totals from the in-process registry; never fail the
	// request if the registry can't be gathered.
	if live, err := gatheredSeries(prometheus.DefaultGatherer); err == nil {
		f := buildFunnel(live)
		resp.LiveTotals = &f
	}

	respond.Data(w, http.StatusOK, resp)
}

// parseAnalyticsWindow resolves the look-back window from ?since (RFC3339) or
// ?window (Go duration), defaulting to defaultAnalyticsWindow. ?since wins when
// both are present.
func parseAnalyticsWindow(r *http.Request, now time.Time) AnalyticsWindow {
	until := now.UTC()
	if s := r.URL.Query().Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return AnalyticsWindow{Since: t.UTC(), Until: until}
		}
	}
	label := r.URL.Query().Get("window")
	d := defaultAnalyticsWindow
	if label != "" {
		if parsed, err := time.ParseDuration(label); err == nil && parsed > 0 {
			d = parsed
		} else {
			label = "" // invalid → fall back to default, don't echo a bogus label
		}
	}
	if label == "" {
		label = defaultAnalyticsWindow.String()
	}
	return AnalyticsWindow{Since: until.Add(-d), Until: until, Label: label}
}

// rollupRow is one grouped aggregate row: a series' total value over the window.
type rollupRow struct {
	MetricName string
	Labels     []byte // canonical jsonb label set
	Total      float64
}

// rollupAnalyticsSQL sums each counter series' bucket deltas over the window.
// The window summation — the expensive part, spanning every bucket in range —
// happens in SQL; grouping on (metric_name, labels) collapses to one row per
// bounded series (low hundreds total, volume-independent). No jsonb operators
// are used, so the query stays portable and the bounded label decode is a
// trivial Go step over the already-collapsed result.
const rollupAnalyticsSQL = `SELECT metric_name, labels, SUM(value) AS total
	FROM pipeline_rollups
	WHERE bucket_start >= ?
	GROUP BY metric_name, labels`

// rollupSeries runs the windowed aggregate and decodes each row's label set.
func (h *StatsHandler) rollupSeries(since time.Time) ([]seriesSum, error) {
	var rows []rollupRow
	if err := h.db.Raw(rollupAnalyticsSQL, since).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]seriesSum, 0, len(rows))
	for _, row := range rows {
		labels := map[string]string{}
		if len(row.Labels) > 0 {
			_ = json.Unmarshal(row.Labels, &labels)
		}
		out = append(out, seriesSum{metric: row.MetricName, labels: labels, total: row.Total})
	}
	return out, nil
}

// gatheredSeries reduces the tracked counter families from a Prometheus
// gatherer to the seriesSum shape (cumulative-since-start values). Mirrors the
// rollup worker's gather step, read-only, for the live-totals snapshot.
func gatheredSeries(g prometheus.Gatherer) ([]seriesSum, error) {
	families, err := g.Gather()
	if err != nil {
		return nil, err
	}
	var out []seriesSum
	for _, fam := range families {
		name := fam.GetName()
		switch name {
		case metricMessagesReceived, metricStageDecisions, metricTerminal, metricAuthVerdict, metricRejectReason:
		default:
			continue
		}
		for _, m := range fam.GetMetric() {
			c := m.GetCounter()
			if c == nil {
				continue
			}
			labels := make(map[string]string, len(m.GetLabel()))
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			out = append(out, seriesSum{metric: name, labels: labels, total: c.GetValue()})
		}
	}
	return out, nil
}

// buildFunnel reshapes collapsed per-series totals into the lifecycle funnel.
// Pure (no I/O) so it is unit-testable without a database, and deterministic:
// every slice is sorted so equal inputs render identically (stable API + tests).
func buildFunnel(series []seriesSum) PipelineFunnel {
	var f PipelineFunnel
	for _, s := range series {
		switch s.metric {
		case metricMessagesReceived:
			f.Received = append(f.Received, TransportCount{
				Transport: s.labels["transport"], Count: s.total,
			})
		case metricAuthVerdict:
			f.AuthVerdicts = append(f.AuthVerdicts, AuthVerdictCount{
				Mechanism: s.labels["mechanism"], Result: s.labels["result"], Count: s.total,
			})
		case metricStageDecisions:
			f.StageDecisions = append(f.StageDecisions, StageDecisionCount{
				Filter: s.labels["filter"], Action: s.labels["action"], Count: s.total,
			})
		case metricTerminal:
			f.TerminalOutcomes = append(f.TerminalOutcomes, TerminalCount{
				Direction: s.labels["direction"], Outcome: s.labels["outcome"], Count: s.total,
			})
		case metricRejectReason:
			f.TopRejectReasons = append(f.TopRejectReasons, RejectReasonCount{
				ReasonCode: s.labels["reason_code"], Count: s.total,
			})
		}
	}

	sort.Slice(f.Received, func(i, j int) bool { return f.Received[i].Transport < f.Received[j].Transport })
	sort.Slice(f.AuthVerdicts, func(i, j int) bool {
		if f.AuthVerdicts[i].Mechanism != f.AuthVerdicts[j].Mechanism {
			return f.AuthVerdicts[i].Mechanism < f.AuthVerdicts[j].Mechanism
		}
		return f.AuthVerdicts[i].Result < f.AuthVerdicts[j].Result
	})
	sort.Slice(f.StageDecisions, func(i, j int) bool {
		if f.StageDecisions[i].Filter != f.StageDecisions[j].Filter {
			return f.StageDecisions[i].Filter < f.StageDecisions[j].Filter
		}
		return f.StageDecisions[i].Action < f.StageDecisions[j].Action
	})
	sort.Slice(f.TerminalOutcomes, func(i, j int) bool {
		if f.TerminalOutcomes[i].Direction != f.TerminalOutcomes[j].Direction {
			return f.TerminalOutcomes[i].Direction < f.TerminalOutcomes[j].Direction
		}
		return f.TerminalOutcomes[i].Outcome < f.TerminalOutcomes[j].Outcome
	})
	// Reject reasons: highest count first (the "top reasons" view), ties broken
	// by code for determinism; then capped.
	sort.Slice(f.TopRejectReasons, func(i, j int) bool {
		if f.TopRejectReasons[i].Count != f.TopRejectReasons[j].Count {
			return f.TopRejectReasons[i].Count > f.TopRejectReasons[j].Count
		}
		return f.TopRejectReasons[i].ReasonCode < f.TopRejectReasons[j].ReasonCode
	})
	if len(f.TopRejectReasons) > maxTopRejectReasons {
		f.TopRejectReasons = f.TopRejectReasons[:maxTopRejectReasons]
	}
	return f
}
