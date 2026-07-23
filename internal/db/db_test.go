package db

import (
	"regexp"
	"strings"
	"testing"
)

// The raw_size backfill runs as raw SQL inside AutoMigrate (this package has
// no live-database test harness), so these tests pin the properties of the
// statement itself — the ones that make the backfill correct and safe to run
// on every startup.

// TestBackfillRawSizeSQL_UsesOctetLength guards the octet-vs-character
// distinction: RFC822.SIZE and POP3 LIST are octet counts, and Go's len()
// (what writers record) counts bytes. Postgres length() on text counts
// CHARACTERS, which diverges from the stored byte count for any non-ASCII
// message — the backfill must use octet_length.
func TestBackfillRawSizeSQL_UsesOctetLength(t *testing.T) {
	if !strings.Contains(backfillRawSizeSQL, "octet_length(raw_message)") {
		t.Fatalf("backfill must compute octet_length(raw_message):\n%s", backfillRawSizeSQL)
	}
	// A bare length( — not preceded by octet_ — would count characters.
	if regexp.MustCompile(`[^_]length\(`).MatchString(strings.ReplaceAll(backfillRawSizeSQL, "octet_length(", "OCTETLEN(")) {
		t.Fatalf("backfill must not use character-counting length():\n%s", backfillRawSizeSQL)
	}
}

// TestBackfillRawSizeSQL_FallsBackToSizeBytes: rows that never had a stored
// raw (locally-composed drafts, digests, folder markers) must inherit
// size_bytes so the sizes gateways report for them do not change.
func TestBackfillRawSizeSQL_FallsBackToSizeBytes(t *testing.T) {
	if !strings.Contains(backfillRawSizeSQL, "ELSE size_bytes") {
		t.Fatalf("backfill must fall back to size_bytes for rows without raw_message:\n%s", backfillRawSizeSQL)
	}
}

// TestBackfillRawSizeSQL_Idempotent: AutoMigrate runs on every startup, so
// the backfill must only touch rows the writers have not populated
// (raw_size = 0) — never overwrite a value recorded at write time.
func TestBackfillRawSizeSQL_Idempotent(t *testing.T) {
	if !strings.Contains(backfillRawSizeSQL, "WHERE raw_size = 0") {
		t.Fatalf("backfill must be guarded on raw_size = 0 to stay idempotent:\n%s", backfillRawSizeSQL)
	}
}
