package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type contactMutationConnection struct {
	mailRemovalConnection
	phase   int
	current map[string]driver.Value
}

func (c contactMutationConnection) Connect(context.Context) (driver.Conn, error) { return &c, nil }

func (c contactMutationConnection) Driver() driver.Driver { return c }

func (c contactMutationConnection) Open(string) (driver.Conn, error) { return &c, nil }

func (c *contactMutationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "contacts"`) {
		c.record(q, args)
		fields, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		if fields["email"] == "duplicate@example.com" {
			return nil, &pgconn.PgError{Code: "23505", Message: "private duplicate contact detail"}
		}
		if fields["email"] == "create-failure@example.com" {
			return nil, errors.New("private contact insert detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "contacts"`) {
		if (len(args) != 3 && len(args) != 4) || !strings.Contains(q, "mailbox_id =") || args[len(args)-1].Value != int64(1) {
			return nil, fmt.Errorf("contact lookup lost row/email/mailbox or bound: %s %v", q, args)
		}
		if len(args) == 4 && args[2].Value != args[0].Value {
			return nil, errors.New("reload changed loaded primary key")
		}
		c.record(q, args)
		c.phase++
		id, email := int64(1), "before@example.com"
		var mailbox int64
		if strings.Contains(q, "email =") {
			mailbox = args[0].Value.(int64)
			email = args[1].Value.(string)
		} else {
			id = args[0].Value.(int64)
			mailbox = args[1].Value.(int64)
		}
		r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "email", "name", "trust_level", "source", "created_at", "updated_at"}}
		if c.phase == 1 {
			if id == 3 || email == "lookup-failure@example.com" {
				return nil, errors.New("private contact lookup detail")
			}
			if id == 2147483647 || email == "new@example.com" || email == "create-failure@example.com" {
				return r, nil
			}
			c.current = map[string]driver.Value{"id": id, "mailbox_id": mailbox, "email": email, "name": "Before", "trust_level": "auto", "source": "sent", "created_at": sieveInstallationTime, "updated_at": sieveInstallationTime}
		} else {
			if id == 5 || c.current["email"] == "reload-failure@example.com" {
				return nil, errors.New("private contact reload detail")
			}
			if id == 6 || c.current["email"] == "reload-missing@example.com" {
				return r, nil
			}
		}
		if c.current == nil {
			return nil, errors.New("contact reload had no selected row")
		}
		r.rows = [][]driver.Value{{c.current["id"], c.current["mailbox_id"], c.current["email"], c.current["name"], c.current["trust_level"], c.current["source"], c.current["created_at"], c.current["updated_at"]}}
		return r, nil
	}
	c.phase = 0
	c.current = nil
	return c.mailRemovalConnection.QueryContext(ctx, q, args)
}

func (c *contactMutationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "contacts" SET`) {
		return nil, errors.New("unexpected ancillary contact write")
	}
	fields, err := vacationMutationFields(q, args)
	if err != nil {
		return nil, err
	}
	c.record(q, args)
	if c.current == nil || fields["id"] != c.current["id"] {
		return nil, errors.New("contact update lost loaded primary key")
	}
	if fields["name"] == "update unavailable" || c.current["email"] == "update-failure@example.com" {
		return nil, errors.New("private contact update detail")
	}
	for k, v := range fields {
		if k != "id" && k != "name" && k != "trust_level" && k != "source" && k != "updated_at" {
			return nil, fmt.Errorf("unexpected contact field:%s", k)
		}
		c.current[k] = v
	}
	return driver.RowsAffected(1), nil
}

func newContactMutationScenarios(t *testing.T) mailRemovalScenarios {
	f := newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector {
		return contactMutationConnection{mailRemovalConnection: base}
	}, 512)
	for _, n := range []string{"example", "email_invalid", "email_missing", "email_null", "email_display", "trust_level_invalid", "trust_default", "trust_null", "trust_auto", "trust_blocked", "name_empty", "duplicate", "lookup_unavailable", "write_unavailable", "reload_unavailable", "reload_missing", "invalid_cid", "invalid_cid_format", "unchanged", "null_fields", "clear_name", "trust_invalid", "trust_only", "email_missing", "existing"} {
		f.scenarios[n] = f.scenarios["found"]
	}
	return f
}

func TestContactMutationFailuresAreNotConflictsMissingOrSuccess(t *testing.T) {
	f := newContactMutationScenarios(t)
	for _, tc := range []struct {
		method, uri, body string
		status            int
	}{{"POST", "/api/v1/accounts/1/contacts", `{"email":"create-failure@example.com"}`, 500}, {"PATCH", "/api/v1/accounts/1/contacts/3", `{"name":"Changed"}`, 500}, {"PATCH", "/api/v1/accounts/1/contacts/5", `{"name":"Changed"}`, 500}, {"PATCH", "/api/v1/accounts/1/contacts/6", `{"name":"Changed"}`, 404}, {"POST", "/api/v1/accounts/1/contacts/block", `{"email":"lookup-failure@example.com"}`, 500}, {"POST", "/api/v1/accounts/1/contacts/block", `{"email":"update-failure@example.com"}`, 500}, {"POST", "/api/v1/accounts/1/contacts/block", `{"email":"reload-failure@example.com"}`, 500}, {"POST", "/api/v1/accounts/1/contacts/block", `{"email":"reload-missing@example.com"}`, 500}} {
		t.Run(tc.uri+tc.body, func(t *testing.T) {
			s := f.scenarios["found"]
			req := httptest.NewRequest(tc.method, tc.uri, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+s.token)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("got %d %s want %d", rr.Code, rr.Body, tc.status)
			}
		})
	}
}
