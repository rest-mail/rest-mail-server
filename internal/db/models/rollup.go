package models

import (
	"encoding/json"
	"time"
)

// PipelineRollup is a time-bucketed aggregate snapshot of one Prometheus counter
// series. The rollup worker (PR4) periodically snapshots the always-on, 100%-
// accurate pipeline counters (pipeline_stage_decisions_total, pipeline_terminal_
// total, auth_verdict_total, pipeline_reject_reason_total, messages_received_
// total) and writes each series' per-bucket DELTA here, giving the in-app
// dashboard durable windowed history without an external Prometheus/Grafana.
//
// Accuracy invariant: these rows are derived from the inline, never-sampled
// counters — NOT from the sampled message_traces — so per-message trace sampling
// and the trace retention pruner never affect rollup counts. Rollups are
// long-lived; the trace pruner deliberately never touches this table. Because
// each rollup is an independent snapshot of the counters, pruning traces can
// never lose aggregate history ("roll up before prune" is structurally
// satisfied — there is nothing to roll up FROM the traces).
type PipelineRollup struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// MetricName is the Prometheus counter family this bucket aggregates (e.g.
	// restmail_pipeline_terminal_total). It is the leading column of both the
	// funnel-query index (metric_name, bucket_start) and the
	// (metric_name, labels, bucket_start) uniqueness constraint the idempotent
	// upsert conflicts on.
	MetricName string `gorm:"size:64;not null;index:idx_pipeline_rollups_metric_bucket,priority:1;uniqueIndex:idx_pipeline_rollups_series_bucket,priority:1" json:"metric_name"`

	// Labels is the bounded label set of the series (stage/action/verdict/filter/
	// transport/outcome/reason_code, per the metric). Stored as canonical
	// sorted-key JSON so two observations of the same series collide on the unique
	// index (Postgres also normalises jsonb, but sorted-key marshaling keeps the
	// series-identity key deterministic in the worker too).
	Labels json.RawMessage `gorm:"type:jsonb;not null;uniqueIndex:idx_pipeline_rollups_series_bucket,priority:2" json:"labels"`

	// BucketStart is the aligned UTC start of this rollup's time bucket
	// (now truncated to the rollup interval).
	BucketStart time.Time `gorm:"not null;index:idx_pipeline_rollups_metric_bucket,priority:2;uniqueIndex:idx_pipeline_rollups_series_bucket,priority:3" json:"bucket_start"`

	// BucketSeconds is the bucket width in seconds (the rollup interval), so a
	// reader can interpret BucketStart without knowing the worker's configuration.
	BucketSeconds int `gorm:"not null" json:"bucket_seconds"`

	// Value is the summed counter delta attributed to this bucket. The upsert
	// accumulates (value = value + delta) across the ticks that fall in the
	// bucket; replaying an unchanged snapshot contributes a zero delta, so
	// re-running a bucket never double-counts.
	Value float64 `gorm:"not null" json:"value"`

	CreatedAt time.Time `json:"created_at"`
}

func (PipelineRollup) TableName() string { return "pipeline_rollups" }
