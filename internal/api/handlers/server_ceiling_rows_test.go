package handlers

import (
	"database/sql/driver"
	"io"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
)

// Keep the server ceiling separate from each fixture's actor permissions.
func defaultServerCeilingRows(query string, args []driver.NamedValue) (driver.Rows, bool) {
	if !strings.Contains(query, "FROM grants AS g") || len(args) == 0 || args[0].Value != models.GrantHolderServer {
		return nil, false
	}
	return &serverCeilingRows{capabilities: models.OwnerCapabilities}, true
}

type serverCeilingRows struct {
	capabilities []string
	index        int
}

func (r *serverCeilingRows) Columns() []string { return []string{"scope", "capability"} }
func (r *serverCeilingRows) Close() error      { return nil }
func (r *serverCeilingRows) Next(out []driver.Value) error {
	if r.index == len(r.capabilities) {
		return io.EOF
	}
	out[0], out[1] = "*", r.capabilities[r.index]
	r.index++
	return nil
}
