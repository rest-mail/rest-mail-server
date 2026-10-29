package handlers

import (
	"database/sql/driver"
	"io"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// accessReads is how many reads mayReach makes to let an account act on its
// own mailbox, with a password or an app password: the mailbox, then the three
// grants.Load makes (the account's grants, its mailbox and the server
// ceiling).
const accessReads = 4

// defaultGrantRows answers the grant queries grants.Load makes, the way a
// database with the default server ceiling answers them for an account with
// no grants of its own: the account's grants are none, and the server ceiling
// holds every owner capability at "*". An account then holds what it owns on
// its own mailbox, which Load reads from the mailboxes table the fake answers
// itself. ok is false for any other query.
func defaultGrantRows(q string) (rows driver.Rows, ok bool) {
	return grantRowsWithin(q, models.OwnerCapabilities)
}

// grantRowsWithin is defaultGrantRows for a server ceiling holding only
// ceiling at "*".
func grantRowsWithin(q string, ceiling []string) (rows driver.Rows, ok bool) {
	if !strings.Contains(q, "FROM grants AS g") {
		return nil, false
	}
	if strings.Contains(q, "grant_activations") {
		return &grantRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}}, true
	}
	server := &grantRows{columns: []string{"scope", "capability"}}
	for _, capability := range ceiling {
		server.rows = append(server.rows, []driver.Value{grants.All, capability})
	}
	return server, true
}

// grantRows is a result of any number of rows.
type grantRows struct {
	columns []string
	rows    [][]driver.Value
}

func (r *grantRows) Columns() []string { return r.columns }
func (r *grantRows) Close() error      { return nil }
func (r *grantRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}
