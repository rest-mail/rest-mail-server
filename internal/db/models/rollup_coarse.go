package models

import (
	"encoding/json"
	"time"
)

// PipelineRollupCoarse is the DOWNSAMPLED (coarse-resolution) form of a
// PipelineRollup series. The rollup worker keeps fine-grained rollups
// (pipeline_rollups, one row per RollupInterval bucket) only for a recent
// detailed-retention window; once a coarse period (default: a UTC day) has aged
// fully past that window the downsampler SUMs its fine buckets into a single
// coarse row here and deletes the superseded fine rows. That bounds storage
// without discarding the aggregate signal: ~288 five-minute rows/series/day
// collapse to one daily row/series (≈288× reduction) while the counts are
// preserved exactly.
//
// It is a SEPARATE table from pipeline_rollups (never an altered/overloaded
// pipeline_rollups) so the two resolutions cannot collide on the fine table's
// (metric_name, labels, bucket_start) unique index — a coarse row aligned to a
// day boundary would otherwise clash with the fine row that starts on the same
// boundary. Readers union the two tables (fine for the recent window, coarse for
// older periods) to reconstruct full windowed history.
//
// Accuracy invariant carries over from PipelineRollup: these values derive from
// the always-on, never-sampled Prometheus counters (via the fine rollups), so
// downsampling — like trace sampling/pruning — never affects aggregate counts.
type PipelineRollupCoarse struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// MetricName is the Prometheus counter family this bucket aggregates. Leading
	// column of both the funnel-query index and the
	// (metric_name, labels, bucket_start) uniqueness constraint the idempotent
	// downsample upsert conflicts on.
	MetricName string `gorm:"size:64;not null;index:idx_pipeline_rollups_coarse_metric_bucket,priority:1;uniqueIndex:idx_pipeline_rollups_coarse_series_bucket,priority:1" json:"metric_name"`

	// Labels is the bounded label set of the series, stored as canonical
	// sorted-key JSON — identical shape to PipelineRollup.Labels so the same
	// canonicalisation makes a fine series and its coarse aggregate share a
	// label identity.
	Labels json.RawMessage `gorm:"type:jsonb;not null;uniqueIndex:idx_pipeline_rollups_coarse_series_bucket,priority:2" json:"labels"`

	// BucketStart is the aligned UTC start of this coarse bucket (a fine
	// bucket_start truncated to the coarse resolution — e.g. the start of the UTC
	// day).
	BucketStart time.Time `gorm:"not null;index:idx_pipeline_rollups_coarse_metric_bucket,priority:2;uniqueIndex:idx_pipeline_rollups_coarse_series_bucket,priority:3" json:"bucket_start"`

	// BucketSeconds is the coarse bucket width in seconds (the coarse resolution),
	// stored per-row so a reader can interpret BucketStart without knowing the
	// worker's current configuration and so a later resolution change does not
	// misrepresent already-condensed rows.
	BucketSeconds int `gorm:"not null" json:"bucket_seconds"`

	// Value is the SUM of the fine buckets condensed into this coarse period. The
	// downsample upsert accumulates (value = value + excluded.value) so the same
	// coarse period can be built up transactionally without double-counting.
	Value float64 `gorm:"not null" json:"value"`

	CreatedAt time.Time `json:"created_at"`
}

func (PipelineRollupCoarse) TableName() string { return "pipeline_rollups_coarse" }
