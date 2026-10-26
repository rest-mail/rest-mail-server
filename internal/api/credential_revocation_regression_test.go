package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type credentialRevocationConnection struct {
	accountCredentialListConnection
	store *bulkQueueExampleStore
}

func (c credentialRevocationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c credentialRevocationConnection) Driver() driver.Driver { return c }

func (c credentialRevocationConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c credentialRevocationConnection) Begin() (driver.Tx, error) { return bulkQueueExampleTx{}, nil }

func (c credentialRevocationConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}

func revocationAuditFields(q string, args []driver.NamedValue) (map[string]driver.Value, error) {
	start := strings.Index(q, "(")
	end := strings.Index(q, ") VALUES")
	if start < 0 || end < start {
		return nil, errors.New("unexpected audit insert layout")
	}
	columns := strings.Split(q[start+1:end], ",")
	if len(columns) != len(args) {
		return nil, errors.New("audit columns/values differ")
	}
	out := map[string]driver.Value{}
	for i, col := range columns {
		out[strings.Trim(col, " \"")] = args[i].Value
	}
	return out, nil
}

func (c credentialRevocationConnection) auditInsert(q string, args []driver.NamedValue) error {
	c.record(q, args)
	fields, err := revocationAuditFields(q, args)
	if err != nil {
		return err
	}
	var target map[string]string
	if err := json.Unmarshal([]byte(fmt.Sprint(fields["target"])), &target); err != nil {
		return err
	}
	if target["credential_id"] == "5" {
		return errors.New("private audit storage detail")
	}
	return nil
}

// Fixed SQL rows and affected counts prove production selectors and audit
// wiring, not PostgreSQL persistence, token invalidation or audit durability.
func (c credentialRevocationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, "SELECT seq, hash FROM audit_log") {
		return &authReadSnapshotRows{columns: []string{"seq", "hash"}}, nil
	}
	if strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
		if err := c.auditInsert(q, args); err != nil {
			return nil, err
		}
		return &authReadSnapshotRows{columns: []string{"seq"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	rows, err := c.accountCredentialListConnection.QueryContext(ctx, q, args)
	if err == nil && strings.Contains(q, `FROM "mailboxes"`) && len(args) > 0 && fmt.Sprint(args[0].Value) == "20" {
		r := rows.(*authReadSnapshotRows)
		for i, col := range r.columns {
			if col == "address" {
				for _, row := range r.rows {
					row[i] = "other@outside.test"
				}
			}
		}
	}
	return rows, err
}

func (c credentialRevocationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SAVEPOINT ") || strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT ") || strings.HasPrefix(q, "SELECT pg_advisory_xact_lock(") {
		return driver.RowsAffected(0), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
		if err := c.auditInsert(q, args); err != nil {
			return nil, err
		}
		return driver.RowsAffected(1), nil
	}
	if !strings.HasPrefix(q, `UPDATE "credentials" SET "revoked_at"=$1 WHERE id = $2 AND account_id = $3 AND kind = $4 AND revoked_at IS NULL`) || len(args) != 4 || strings.Contains(q, "expires_at") {
		return nil, errors.New("revocation lost exact ID/account/kind/unrevoked restriction")
	}
	c.record(q, args)
	if args[1].Value == int64(3) {
		return nil, errors.New("private credential update detail")
	}
	for _, id := range []int64{2, 7, 8, 2147483647} {
		if args[1].Value == id {
			return driver.RowsAffected(0), nil
		}
	}
	return driver.RowsAffected(1), nil
}
