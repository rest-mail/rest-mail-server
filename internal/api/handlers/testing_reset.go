package handlers

import (
	"fmt"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"gorm.io/gorm"
)

const resetAdmins = `(SELECT unnest(account_ids) FROM test_reset_baseline)`

var resetRetainedTables = map[string]string{
	"test_reset_baseline": "Initial administrator IDs pin the control plane and sequence floors; later fixture administrators are test data.",
	"mailboxes":           "Only initial administrators survive so the reset caller keeps access.",
	"domains":             "Only domains containing retained administrators survive so their addresses remain valid.",
	"credentials":         "Only retained administrators' credentials survive so their sign-in and MFA remain usable.",
	"grants":              "Only retained administrators' own grants and the installation server ceiling survive.",
	"roles":               "System roles and roles used by retained grants define the installation's authorization vocabulary.",
	"role_capabilities":   "Only definitions belonging to retained roles survive.",
	"capabilities":        "The installed capability catalog is schema/bootstrap data, not fixture data.",
	"owner_keys":          "Installation recovery trust must not be erased by fixture cleanup.",
	"audit_log":           "The append-only, hash-chained audit log cannot be deleted without destroying integrity.",
}

func (h *TestHandler) OnReset(reset func()) {
	if !h.productionLocked() {
		h.resetState = append(h.resetState, reset)
	}
}

func (h *TestHandler) ResetRuntime(broker *SSEBroker, sessions *SessionCache, auth *AuthHandler, recovery *RecoveryEmailHandler) {
	h.OnReset(func() {
		broker.reset()
		sessions.mu.Lock()
		sessions.gen++
		clear(sessions.entries)
		sessions.mu.Unlock()
		for _, budget := range []*middleware.FailureBudget{auth.recoverFailures, recovery.requests} {
			if budget != nil {
				budget.Reset()
			}
		}
	})
}

func cleanResetData(tx *gorm.DB) error {
	if err := tx.Exec(`CREATE TABLE IF NOT EXISTS test_reset_baseline (
		singleton boolean PRIMARY KEY CHECK (singleton), account_ids bigint[] NOT NULL)`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO test_reset_baseline (singleton, account_ids)
		SELECT true, ARRAY` + testDataAdmins + ` ON CONFLICT DO NOTHING`).Error; err != nil {
		return err
	}
	tables, err := tx.Migrator().GetTables()
	if err != nil {
		return err
	}
	for _, table := range tables {
		if _, retained := resetRetainedTables[table]; retained {
			continue
		}
		if err := tx.Exec("DELETE FROM " + resetIdentifier(table)).Error; err != nil {
			return fmt.Errorf("clear reset table %s: %w", table, err)
		}
	}
	statements := []string{
		"DELETE FROM credentials WHERE account_id NOT IN " + resetAdmins,
		"DELETE FROM grants WHERE NOT ((holder_kind = 'account' AND holder_id IN " + resetAdmins +
			" AND (scope NOT LIKE 'mailbox:%' OR scope = (SELECT 'mailbox:' || address FROM mailboxes WHERE id = holder_id))) OR holder_kind = 'server')",
		"DELETE FROM mailboxes WHERE id NOT IN " + resetAdmins,
		"DELETE FROM domains WHERE id NOT IN (SELECT domain_id FROM mailboxes)",
		"DELETE FROM roles WHERE NOT system_role AND id NOT IN (SELECT role_id FROM grants)",
		"DELETE FROM role_capabilities WHERE role_id NOT IN (SELECT id FROM roles)",
	}
	for _, statement := range statements {
		var table string
		if _, err := fmt.Sscanf(statement, "DELETE FROM %s", &table); err != nil {
			return fmt.Errorf("read the table of retained reset statement %q: %w", statement, err)
		}
		if !containsResetTable(tables, table) {
			continue
		}
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("clear retained reset table %s: %w", table, err)
		}
	}
	// The retained accounts lost their messages and their usage row with them,
	// and an import into an account with no usage row is refused: each starts
	// again at nothing, as a new account does.
	if containsResetTable(tables, "quota_usage") {
		if err := tx.Exec(`INSERT INTO quota_usage (mailbox_id, updated_at)
			SELECT id, now() FROM mailboxes ON CONFLICT DO NOTHING`).Error; err != nil {
			return fmt.Errorf("restart the retained accounts' usage: %w", err)
		}
	}
	for _, table := range tables {
		var sequence *string
		if err := tx.Raw(`SELECT pg_get_serial_sequence(?, 'id') WHERE EXISTS (
			SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'id')`,
			resetIdentifier(table), table).Scan(&sequence).Error; err != nil {
			return err
		}
		if sequence == nil {
			continue
		}
		var next int64
		if err := tx.Raw("SELECT COALESCE(MAX(id), 0) + 1 FROM " + resetIdentifier(table)).Scan(&next).Error; err != nil {
			return err
		}
		if err := tx.Exec(fmt.Sprintf("ALTER SEQUENCE %s RESTART WITH %d", *sequence, next)).Error; err != nil {
			return err
		}
	}
	return nil
}

func resetIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func containsResetTable(tables []string, name string) bool {
	for _, table := range tables {
		if table == name {
			return true
		}
	}
	return false
}
