package db

import (
	"strings"
	"testing"
)

// Issue #196: the destructive linked_accounts dedupe (a DELETE) ran on every API
// boot, in every environment. Destructive DML must NEVER be issued on the additive
// boot/default path; it runs only when an operator explicitly opts in (the migrate
// tool). These tests pin that contract without needing a live database, matching
// this package's SQL-property test convention.

// TestPendingDestructiveMigrations_NoneOnBoot: the boot/default path (no opt-in)
// must contribute zero destructive statements.
func TestPendingDestructiveMigrations_NoneOnBoot(t *testing.T) {
	if got := pendingDestructiveMigrations(false); len(got) != 0 {
		t.Fatalf("boot/additive path must issue no destructive DML, got %d statement(s): %v", len(got), got)
	}
}

// TestPendingDestructiveMigrations_OptInCollapsesDuplicates: the explicit opt-in
// path must include the destructive linked_accounts dedupe DELETE.
func TestPendingDestructiveMigrations_OptInCollapsesDuplicates(t *testing.T) {
	got := pendingDestructiveMigrations(true)
	if len(got) == 0 {
		t.Fatal("opt-in path must include the linked_accounts dedupe")
	}
	var sawDelete bool
	for _, s := range got {
		up := strings.ToUpper(strings.TrimSpace(s))
		if strings.HasPrefix(up, "DELETE") && strings.Contains(s, "linked_accounts") {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatalf("opt-in migrations must include the destructive linked_accounts DELETE, got %v", got)
	}
}

// TestDedupeLinkedAccountsSQL_IsDestructive documents that the gated statement is
// in fact a DELETE, so the gating above is guarding real destructive DML.
func TestDedupeLinkedAccountsSQL_IsDestructive(t *testing.T) {
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(dedupeLinkedAccountsSQL)), "DELETE") {
		t.Fatalf("dedupeLinkedAccountsSQL must be a DELETE, got: %s", dedupeLinkedAccountsSQL)
	}
}
