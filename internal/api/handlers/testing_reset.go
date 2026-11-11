package handlers

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

const resetAdmins = `(SELECT unnest(account_ids) FROM test_reset_baseline)`

var resetRetainedTables = map[string]string{
	"test_reset_baseline": "Initial administrator IDs and their domains' provisioned DKIM keys pin the control plane, published DNS and sequence floors; later fixture changes are test data.",
	"mailboxes":           "Only initial administrators survive so the reset caller keeps access.",
	"domains":             "Only domains containing retained administrators survive so their addresses remain valid.",
	"credentials":         "Only retained administrators' sign-in credentials survive (resetCredentialKindsKept) so their sign-in and MFA remain usable.",
	"grants":              "Only retained administrators' own grants and the installation server ceiling survive.",
	"roles":               "System roles and roles used by retained grants define the installation's authorization vocabulary.",
	"role_capabilities":   "Only definitions belonging to retained roles survive; the server role's set goes back to every capability.",
	"capabilities":        "The installed capability catalog is schema/bootstrap data, not fixture data.",
	"owner_keys":          "Installation recovery trust must not be erased by fixture cleanup.",
	"audit_log":           "The append-only, hash-chained audit log cannot be deleted without destroying integrity.",
}

// resetDomainSettings puts each domain the reset keeps back to a new domain's
// settings, then restores its initially provisioned DKIM material for published
// DNS. The seed finds a kept domain by name and does not touch its settings, so
// later key, rotation interval or sending policy changes must be reset (#1385).
var resetDomainSettings = map[string]string{
	"dkim_selector":               "''",
	"dkim_private_key":            "''",
	"dkim_created_at":             "NULL",
	"dkim_rotation_interval_days": "DEFAULT",
	"sending_policy":              "DEFAULT",
	"dmarc_policy":                "DEFAULT",
	"default_quota_bytes":         "DEFAULT",
	"outbound_stopped_at":         "NULL",
	"outbound_stop_reason":        "DEFAULT",
	"outbound_stopped_by":         "DEFAULT",
}

// resetDomainKept are the domain columns the reset leaves as they are, and why.
// Every other column must be in resetDomainSettings.
var resetDomainKept = map[string]string{
	"id":          "The retained administrators' addresses and sessions name the domain by id.",
	"name":        "The retained administrators' addresses are on it.",
	"active":      "The seed sets it on its own domains; a retained administrator's other domain keeps serving.",
	"server_type": "The seed sets it on its own domains; a retained administrator's other domain keeps its kind.",
	"created_at":  "Bookkeeping, not a setting.",
	"updated_at":  "Bookkeeping, not a setting.",
}

// resetMailboxSettings puts each account the reset keeps (the initial
// administrators) back to a new account's settings, as resetDomainSettings does
// for their domains. The row survives so its sessions keep working, and without
// this a quiet hours window, contact setting or quota set on it before the reset
// would still be there after it (#1385).
var resetMailboxSettings = map[string]string{
	"contact_reply_time_enabled": "DEFAULT",
	"quiet_hours_enabled":        "DEFAULT",
	"quiet_hours_start":          "DEFAULT",
	"quiet_hours_end":            "DEFAULT",
	"quiet_hours_time_zone":      "DEFAULT",
	"quota_bytes":                "DEFAULT",
	"quota_used_bytes":           "DEFAULT",
}

// resetMailboxKept are the account columns the reset leaves as they are, and
// why. Every other column must be in resetMailboxSettings.
var resetMailboxKept = map[string]string{
	"id":                       "The retained administrators' sessions and grants name the account by id.",
	"domain_id":                "The account's address is on that domain, which the reset keeps.",
	"local_part":               "Part of the address the retained administrator signs in with.",
	"address":                  "The retained administrator signs in with it.",
	"provisioned_postmaster":   "Whether this account was provisioned with its domain is provenance, not a resettable setting.",
	"display_name":             "Part of who the retained administrator is, given when the installation was made.",
	"active":                   "The reset caller signs in with this account, so it stays as it is.",
	"last_login_at":            "Bookkeeping, not a setting.",
	"password_change_required": "Part of the retained credentials: the reset keeps them as they are.",
	"two_factor_required":      "Part of the retained credentials: the reset keeps them as they are.",
	"credential_epoch":         "Stamped into the retained sessions, which must keep working across the reset.",
	"bootstrap_expires_at":     "Part of the retained credentials: the reset keeps them as they are.",
	"owner_id":                 "Initial administrators are not owned accounts; the reset keeps who owns what among them.",
	"last_password_change":     "Part of the retained credentials: the reset keeps them as they are.",
	"created_at":               "Bookkeeping, not a setting.",
	"updated_at":               "Bookkeeping, not a setting.",
}

// resetCredentialKindsKept are the credential kinds a retained administrator
// keeps across a reset, and why. Every other kind is what a test made: an app
// password, an API key or a client secret made before the reset would otherwise
// still be listed, and still work, after it (#1385).
var resetCredentialKindsKept = map[string]string{
	models.CredentialKindPassword:     "The retained administrators sign in with it.",
	models.CredentialKindTOTP:         "A retained administrator's second factor, which its sign-in and fresh proofs need.",
	models.CredentialKindRecoveryCode: "Stands in for that second factor, and goes with it.",
}

// resetCredentialsCleared is the condition on the credentials a reset removes.
func resetCredentialsCleared() string {
	kinds := make([]string, 0, len(resetCredentialKindsKept))
	for kind := range resetCredentialKindsKept {
		kinds = append(kinds, "'"+kind+"'")
	}
	sort.Strings(kinds)
	return "account_id NOT IN " + resetAdmins + " OR kind NOT IN (" + strings.Join(kinds, ", ") + ")"
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
	if err := tx.Exec(`ALTER TABLE test_reset_baseline ADD COLUMN IF NOT EXISTS domain_dkim jsonb`).Error; err != nil {
		return err
	}
	if err := tx.Exec(`UPDATE test_reset_baseline SET domain_dkim = (
		SELECT COALESCE(jsonb_agg(jsonb_build_object(
			'id', id, 'dkim_selector', dkim_selector,
			'dkim_private_key', dkim_private_key, 'dkim_created_at', dkim_created_at)), '[]'::jsonb)
		FROM domains WHERE id IN (SELECT domain_id FROM mailboxes WHERE id IN ` + resetAdmins + `)
	) WHERE domain_dkim IS NULL`).Error; err != nil {
		return fmt.Errorf("pin the retained domains' provisioned DKIM keys: %w", err)
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
		"DELETE FROM credentials WHERE " + resetCredentialsCleared(),
		// A retained administrator keeps its sign-in and its confirmed second
		// factor, but not an enrollment it never confirmed, nor recovery codes
		// that belong to no confirmed authenticator: a pending enrollment started
		// before the reset would come back with the administrator (#1385).
		"DELETE FROM credentials WHERE kind = 'totp' AND confirmed_at IS NULL",
		"DELETE FROM credentials WHERE kind = 'recovery_code' AND account_id NOT IN " +
			"(SELECT account_id FROM credentials WHERE kind = 'totp' AND confirmed_at IS NOT NULL AND revoked_at IS NULL)",
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
	if containsResetTable(tables, "domains") {
		assignments := make([]string, 0, len(resetDomainSettings))
		for column, value := range resetDomainSettings {
			assignments = append(assignments, resetIdentifier(column)+" = "+value)
		}
		sort.Strings(assignments)
		if err := tx.Exec("UPDATE domains SET " + strings.Join(assignments, ", ")).Error; err != nil {
			return fmt.Errorf("restart the retained domains' settings: %w", err)
		}
		if err := tx.Exec(`UPDATE domains SET dkim_selector = baseline.dkim_selector,
			dkim_private_key = baseline.dkim_private_key, dkim_created_at = baseline.dkim_created_at
			FROM jsonb_to_recordset((SELECT domain_dkim FROM test_reset_baseline))
			AS baseline(id bigint, dkim_selector text, dkim_private_key text, dkim_created_at timestamptz)
			WHERE domains.id = baseline.id`).Error; err != nil {
			return fmt.Errorf("restore the retained domains' provisioned DKIM keys: %w", err)
		}
	}
	if containsResetTable(tables, "mailboxes") {
		assignments := make([]string, 0, len(resetMailboxSettings))
		for column, value := range resetMailboxSettings {
			assignments = append(assignments, resetIdentifier(column)+" = "+value)
		}
		sort.Strings(assignments)
		if err := tx.Exec("UPDATE mailboxes SET " + strings.Join(assignments, ", ")).Error; err != nil {
			return fmt.Errorf("restart the retained accounts' settings: %w", err)
		}
	}
	// The server role is a system role, so it and its capabilities survive, and
	// a ceiling narrowed before the reset would bound every account after it.
	// It goes back to every capability, as a new installation starts (#1385).
	if containsResetTable(tables, "role_capabilities") && containsResetTable(tables, "capabilities") {
		if err := tx.Exec(`INSERT INTO role_capabilities (role_id, capability_id)
			SELECT roles.id, capabilities.id FROM roles CROSS JOIN capabilities
			WHERE roles.name = ? AND capabilities.name <> ? ON CONFLICT DO NOTHING`,
			models.RoleServer, grants.Wildcard).Error; err != nil {
			return fmt.Errorf("restart the server capability ceiling: %w", err)
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
