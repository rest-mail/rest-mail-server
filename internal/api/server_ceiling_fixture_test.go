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
		models.CapMailboxSession, models.CapAdminSession,
	} {
		rows.rows = append(rows.rows, []driver.Value{"*", capability})
	}
	return rows, true
}

// withSessionRows is what a fake grant lookup returns once its rows come from
// real roles: every role holding a mailbox capability at a scope holds
// mailbox:session there too, and every other role holds admin:session (#324).
// Rows are (scope, capability, ...); the added rows copy the rest of the row
// they follow, so expiries carry over.
func withSessionRows(rows [][]driver.Value) [][]driver.Value {
	mailbox := map[string]bool{}
	for _, c := range models.OwnerCapabilities {
		mailbox[c] = true
	}
	seen := map[string]bool{}
	out := append([][]driver.Value{}, rows...)
	for _, row := range rows {
		scope, _ := row[0].(string)
		capability, _ := row[1].(string)
		session := models.CapAdminSession
		switch {
		case capability == models.CapMailReceive, capability == models.CapMailboxSession, capability == models.CapAdminSession:
			continue
		case mailbox[capability]:
			session = models.CapMailboxSession
		}
		if seen[scope+" "+session] {
			continue
		}
		seen[scope+" "+session] = true
		added := append([]driver.Value{scope, session}, row[2:]...)
		out = append(out, added)
	}
	return out
}

// grantLookupRows is invitationRows for a fake grant lookup: the rows with the
// session capabilities their roles would carry (withSessionRows).
func grantLookupRows(columns []string, rows ...[]driver.Value) driver.Rows {
	return invitationRows(columns, withSessionRows(rows)...)
}
