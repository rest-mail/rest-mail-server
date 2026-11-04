package db

import (
	"fmt"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// agentLedgerAppendOnlySQL makes agent_ledger append-only the way
// auditLogAppendOnlySQL does audit_log, and lets one entry undo another once.
var agentLedgerAppendOnlySQL = []string{
	`CREATE OR REPLACE FUNCTION agent_ledger_append_only() RETURNS trigger
	LANGUAGE plpgsql AS $$
	BEGIN
		RAISE EXCEPTION 'agent_ledger is append-only';
	END
	$$`,
	`CREATE OR REPLACE TRIGGER agent_ledger_no_update_or_delete
	BEFORE UPDATE OR DELETE ON agent_ledger
	FOR EACH ROW EXECUTE FUNCTION agent_ledger_append_only()`,
	`CREATE OR REPLACE TRIGGER agent_ledger_no_truncate
	BEFORE TRUNCATE ON agent_ledger
	FOR EACH STATEMENT EXECUTE FUNCTION agent_ledger_append_only()`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_ledger_undoes
	ON agent_ledger (mailbox_id, undoes) WHERE undoes IS NOT NULL`,
}

// agentLedgerMigrateLockKey serialises MigrateAgentLedger across replicas
// starting at once: "agentmig".
const agentLedgerMigrateLockKey int64 = 0x6167656e746d6967

// MigrateAgentLedger creates the assistant's ledger and trust tables and
// installs the triggers that make the ledger append-only, in one transaction.
// Idempotent; AutoMigrate runs it on every start. A trust row goes with its
// account; a ledger entry outlives it, as an audit entry does.
func MigrateAgentLedger(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", agentLedgerMigrateLockKey).Error; err != nil {
			return fmt.Errorf("agent ledger migration failed: %w", err)
		}
		if err := tx.AutoMigrate(&models.AgentLedgerEntry{}, &models.AgentTrust{}); err != nil {
			return fmt.Errorf("agent ledger migration failed: %w", err)
		}
		if !tx.Migrator().HasConstraint(&models.AgentTrust{}, "Mailbox") {
			if err := tx.Migrator().CreateConstraint(&models.AgentTrust{}, "Mailbox"); err != nil {
				return fmt.Errorf("agent trust constraint: %w", err)
			}
		}
		for _, stmt := range agentLedgerAppendOnlySQL {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("agent ledger append-only guard failed: %w", err)
			}
		}
		return nil
	})
}
