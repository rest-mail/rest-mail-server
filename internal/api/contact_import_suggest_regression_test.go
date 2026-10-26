package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type contactImportSuggestConnection struct {
	mailRemovalConnection
	tx   *contactImportTx
	seen map[string]bool
}

type contactImportTx struct {
	c        *contactImportSuggestConnection
	relevant bool
}

func (c contactImportSuggestConnection) Connect(context.Context) (driver.Conn, error) { return &c, nil }

func (c contactImportSuggestConnection) Driver() driver.Driver { return c }

func (c contactImportSuggestConnection) Open(string) (driver.Conn, error) { return &c, nil }

func (c *contactImportSuggestConnection) Begin() (driver.Tx, error) {
	tx := &contactImportTx{c: c}
	c.tx = tx
	return tx, nil
}

func (tx *contactImportTx) Commit() error {
	if tx.relevant {
		tx.c.record("COMMIT", nil)
	}
	tx.c.tx = nil
	return nil
}

func (tx *contactImportTx) Rollback() error {
	if tx.relevant {
		tx.c.record("ROLLBACK", nil)
	}
	tx.c.tx = nil
	return nil
}

func (c *contactImportSuggestConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "contacts"`) {
		if c.tx != nil && !c.tx.relevant {
			c.record("BEGIN", nil)
			c.tx.relevant = true
		}
		c.record(q, args)
		fields, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		email := fields["email"].(string)
		if email == "fail@example.com" {
			return nil, errors.New("private contact import detail")
		}
		if email == "duplicate@example.com" || c.seen[email] {
			if strings.Contains(q, `ON CONFLICT ("mailbox_id","email") DO NOTHING`) {
				return &authReadSnapshotRows{columns: []string{"id"}}, nil
			}
			return nil, &pgconn.PgError{Code: "23505", Message: "private duplicate detail"}
		}
		if c.seen == nil {
			c.seen = map[string]bool{}
		}
		c.seen[email] = true
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "contacts"`) {
		if len(args) != 5 || !strings.Contains(q, "mailbox_id =") || !strings.Contains(q, `email LIKE ?`) && !strings.Contains(q, `email LIKE $`) || !strings.Contains(q, `ESCAPE '\'`) || !strings.Contains(q, "ORDER BY name ASC, email ASC") || args[1].Value != args[2].Value || args[3].Value != "blocked" || args[4].Value != int64(10) {
			return nil, errors.New("suggestion lost mailbox/literal matching/blocked ceiling/order/limit")
		}
		c.record(q, args)
		if args[1].Value == "%db-failure%" {
			return nil, errors.New("private suggestion dependency detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "email", "name", "trust_level", "source", "created_at", "updated_at"}}
		if args[1].Value != "%missing%" {
			r.rows = [][]driver.Value{{int64(1), args[0].Value, "alpha@example.com", "Alpha", "auto", "sent", sieveInstallationTime, sieveInstallationTime}, {int64(2), args[0].Value, "beta@example.com", "Beta", "trusted", "manual", sieveInstallationTime, sieveInstallationTime}}
		}
		for _, literal := range []struct {
			query, email, name string
			id                 int64
		}{{`%\%%`, "percent@example.com", "50% offer", 3}, {`%\_%`, "underscore@example.com", "part_one", 4}, {`%\\%`, "backslash@example.com", `path\name`, 5}, {"%Élodie%", "unicode@example.com", "Élodie", 6}} {
			if args[1].Value == literal.query {
				r.rows = [][]driver.Value{{literal.id, args[0].Value, literal.email, literal.name, "auto", "sent", sieveInstallationTime, sieveInstallationTime}}
			}
		}
		return r, nil
	}
	c.seen = nil
	return c.mailRemovalConnection.QueryContext(ctx, q, args)
}

func (c *contactImportSuggestConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("unexpected ancillary import/suggest write")
}

func newContactImportSuggestScenarios(t *testing.T) mailRemovalScenarios {
	f := newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector {
		return contactImportSuggestConnection{mailRemovalConnection: base}
	}, 512)
	for _, n := range []string{"body_array", "contacts_empty", "contacts_missing", "contacts_null", "email_invalid", "email_missing", "imported", "mixed_batch", "duplicates", "all_duplicate", "repeat_batch", "batch_failure", "empty_query", "missing", "literal_percent", "literal_underscore", "literal_backslash", "unicode", "query_unavailable"} {
		f.scenarios[n] = f.scenarios["found"]
	}
	return f
}

func TestContactImportSuggestFailuresAreNotSuccess(t *testing.T) {
	f := newContactImportSuggestScenarios(t)
	for _, tc := range []struct{ method, uri, body string }{{"POST", "/api/v1/accounts/1/contacts/import", `{"contacts":[{"email":"new@example.com"},{"email":"fail@example.com"}]}`}, {"GET", "/api/v1/accounts/1/contacts/suggest?q=db-failure", ""}} {
		t.Run(tc.uri, func(t *testing.T) {
			s := f.scenarios["found"]
			req := httptest.NewRequest(tc.method, tc.uri, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+s.token)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != 500 {
				t.Fatalf("got %d %s want500", rr.Code, rr.Body)
			}
		})
	}
}
