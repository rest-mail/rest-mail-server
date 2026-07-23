package models

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// TestMessageTraceTableName pins the physical table (a new table, not an
// in-place rename of pipeline_logs).
func TestMessageTraceTableName(t *testing.T) {
	if got := (MessageTrace{}).TableName(); got != "message_traces" {
		t.Errorf("TableName = %q, want message_traces", got)
	}
}

// TestMessageTraceSchema parses the GORM schema (no database needed) and asserts
// the schema-commitment columns and indexes are present and shaped correctly —
// this is the slice that is hardest to change later, so the migration shape is
// locked by a test.
func TestMessageTraceSchema(t *testing.T) {
	s, err := schema.Parse(&MessageTrace{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse MessageTrace schema: %v", err)
	}

	// Every schema-commitment column must exist (by DB column name).
	wantCols := []string{
		"id", "message_id", "rfc_message_id", "direction", "transport",
		"mail_from", "rcpt_to", "client_ip", "pipeline_id", "final_action",
		"outcome", "reason_code", "spam_score", "duration_ms", "stages",
		"sampled", "created_at", "expires_at",
	}
	for _, col := range wantCols {
		if s.LookUpField(col) == nil {
			t.Errorf("column %q missing from MessageTrace schema", col)
		}
	}

	// message_id and expires_at must be nullable (set only on delivery / by the
	// PR4 pruner respectively); the pointer types encode that.
	if f := s.LookUpField("message_id"); f != nil && f.NotNull {
		t.Error("message_id must be nullable (nil for non-delivered outcomes)")
	}
	if f := s.LookUpField("expires_at"); f != nil && f.NotNull {
		t.Error("expires_at must be nullable (no horizon until PR4)")
	}

	// Index shape: standalone single-column indexes + the (outcome, created_at)
	// composite the PR5 analytics scans.
	indexes := s.ParseIndexes()
	byName := map[string][]string{}
	leads := map[string]bool{}
	for _, idx := range indexes {
		var out []string
		for _, opt := range idx.Fields {
			out = append(out, opt.Field.DBName)
		}
		byName[idx.Name] = out
		if len(out) > 0 {
			leads[out[0]] = true
		}
	}

	// The composite index — leading column outcome, then created_at (order matters
	// for the prefix scan).
	composite := byName["idx_message_traces_outcome_created"]
	if len(composite) != 2 || composite[0] != "outcome" || composite[1] != "created_at" {
		t.Errorf("composite index columns = %v, want [outcome created_at]", composite)
	}

	// Single-column indexes required for correlation and PR4 pruning. Each must be
	// covered by SOME index whose leading column is that column.
	needLeading := []string{"message_id", "rfc_message_id", "created_at", "reason_code", "expires_at"}
	for _, col := range needLeading {
		if !leads[col] {
			t.Errorf("no index leads with %q (needed for correlation / pruning)", col)
		}
	}
}
