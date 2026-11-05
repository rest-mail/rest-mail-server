package api

import (
	"database/sql/driver"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// Fixed recipient fixtures hold receive permission on the account and server.
// PostgreSQL-backed receive tests exercise actual scoped and bounded grants.
func defaultReceiveAccountRows(query string) (*authReadSnapshotRows, bool) {
	if strings.Contains(query, "FROM grants AS g") {
		return &authReadSnapshotRows{columns: []string{"scope", "capability"}, rows: [][]driver.Value{{"*", models.CapMailReceive}}}, true
	}
	if strings.Contains(query, `SELECT "address","owner_id" FROM "mailboxes"`) {
		return &authReadSnapshotRows{columns: []string{"address", "owner_id"}}, true
	}
	// The fixture accounts are on a live domain: only those take mail (#1845).
	if strings.Contains(query, `SELECT count(*) FROM "domains" WHERE id = $1 AND active`) {
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(1)}}}, true
	}
	return nil, false
}
