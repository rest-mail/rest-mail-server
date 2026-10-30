package api

import (
	"database/sql/driver"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
)

// HTTP fixtures model the default server object separately from actors.
// A server lookup must never accidentally return the actor's scoped grants.
func defaultServerCeilingRows(query string, args []driver.NamedValue) (*authReadSnapshotRows, bool) {
	if !strings.Contains(query, "FROM grants AS g") || len(args) == 0 || args[0].Value != models.GrantHolderServer {
		return nil, false
	}
	rows := &authReadSnapshotRows{columns: []string{"scope", "capability"}}
	for _, capability := range []string{
		"domains:read", "domains:write", "domains:delete",
		"mailboxes:read", "mailboxes:write", "mailboxes:delete",
		"users:read", "users:write", "users:delete",
		"pipelines:read", "pipelines:write", "pipelines:delete",
		"messages:send_bulk", "messages:read", "queue:read", "queue:manage",
		"bans:read", "bans:write", "bans:delete", "observability:read", "audit:read", "agents:create",
		models.CapMailRead, models.CapMailWrite, models.CapMailSend, models.CapMailReceive, models.CapMailSendAs, models.CapMailSendOnBehalf, models.CapMailboxManage,
	} {
		rows.rows = append(rows.rows, []driver.Value{"*", capability})
	}
	return rows, true
}
