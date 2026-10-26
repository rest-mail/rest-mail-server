package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type personalRemovalConnection struct {
	mailRemovalConnection
	current *personalRemovalTx
}

func (c personalRemovalConnection) Connect(context.Context) (driver.Conn, error) { return &c, nil }

func (c personalRemovalConnection) Driver() driver.Driver { return c }

func (c personalRemovalConnection) Open(string) (driver.Conn, error) { return &c, nil }

type personalRemovalTx struct {
	c        *personalRemovalConnection
	relevant bool
}

func (c *personalRemovalConnection) Begin() (driver.Tx, error) {
	tx := &personalRemovalTx{c: c}
	c.current = tx
	return tx, nil
}

func (tx *personalRemovalTx) Commit() error {
	if tx.relevant {
		tx.c.record("COMMIT", nil)
	}
	tx.c.current = nil
	return nil
}

func (tx *personalRemovalTx) Rollback() error {
	if tx.relevant {
		tx.c.record("ROLLBACK", nil)
	}
	tx.c.current = nil
	return nil
}

func (c *personalRemovalConnection) markTransaction() {
	if c.current != nil && !c.current.relevant {
		c.record("BEGIN", nil)
		c.current.relevant = true
	}
}

func (c *personalRemovalConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, `FROM "contacts"`) {
		if len(args) != 3 || !strings.Contains(q, "id =") || !strings.Contains(q, "mailbox_id =") || args[2].Value != int64(1) {
			return nil, errors.New("contact lookup lost row/mailbox or bound")
		}
		c.record(q, args)
		r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id"}}
		if args[0].Value == int64(3) {
			return nil, errors.New("private contact lookup detail")
		}
		if args[0].Value != int64(2147483647) && args[0].Value != int64(42) {
			r.rows = [][]driver.Value{{args[0].Value, args[1].Value}}
		}
		return r, nil
	}
	if strings.Contains(q, `FROM "messages"`) && strings.Contains(q, "SUM(size_bytes)") {
		if len(args) != 2 || !strings.Contains(q, "mailbox_id =") || !strings.Contains(q, "folder =") {
			return nil, errors.New("folder sum lost mailbox/name")
		}
		c.markTransaction()
		c.record(q, args)
		if args[1].Value == "SumFailure" {
			return nil, errors.New("private folder sum detail")
		}
		size := int64(20)
		if args[1].Value == "Empty" || args[1].Value == "Missing" {
			size = 0
		}
		return &authReadSnapshotRows{columns: []string{"total"}, rows: [][]driver.Value{{size}}}, nil
	}
	return c.mailRemovalConnection.QueryContext(ctx, q, args)
}

func (c *personalRemovalConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.HasPrefix(q, `DELETE FROM "contacts"`):
		if len(args) != 1 || !strings.Contains(q, `"contacts"."id"`) {
			return nil, errors.New("contact deletion lost primary key")
		}
		c.markTransaction()
		c.record(q, args)
		if args[0].Value == int64(43) {
			return nil, errors.New("private contact deletion detail")
		}
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(q, `DELETE FROM "messages"`):
		if len(args) != 2 || !strings.Contains(q, "mailbox_id =") || !strings.Contains(q, "folder =") {
			return nil, errors.New("folder deletion lost mailbox/name")
		}
		c.markTransaction()
		c.record(q, args)
		if args[1].Value == "DeleteFailure" {
			return nil, errors.New("private folder deletion detail")
		}
		if args[1].Value == "Missing" {
			return driver.RowsAffected(0), nil
		}
		return driver.RowsAffected(2), nil
	case strings.HasPrefix(q, `UPDATE "mailboxes" SET "quota_used_bytes"`):
		if len(args) != 3 || args[0].Value != int64(20) || !strings.Contains(q, "GREATEST(quota_used_bytes -") || !strings.Contains(q, "id =") {
			return nil, fmt.Errorf("quota lost floor or exact mailbox: %s %v", q, args)
		}
		c.markTransaction()
		c.record(q, args)
		if args[2].Value == int64(4) {
			return nil, errors.New("private quota update detail")
		}
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected ancillary write in personal removal fixture")
}

func newPersonalRemovalScenarios(t *testing.T) mailRemovalScenarios {
	fixture := newMailRemovalScenarios(t, func(base mailResourceReadConnection, store *bulkQueueExampleStore) driver.Connector {
		return personalRemovalConnection{mailRemovalConnection{base, store}, nil}
	})
	for _, name := range []string{"invalid_cid", "invalid_cid_format", "literal_folder", "empty_folder", "sum_unavailable", "delete_unavailable", "quota_unavailable", "standard_inbox", "standard_inbox_case", "standard_sent", "standard_drafts", "standard_trash", "standard_spam", "standard_archive"} {
		fixture.scenarios[name] = fixture.scenarios["found"]
	}
	return fixture
}

func TestPersonalRemovalFailuresAreNotMissingOrSuccessful(t *testing.T) {
	fixture := newPersonalRemovalScenarios(t)
	for _, tc := range []struct{ op, path string }{
		{"deleteContact", "/api/v1/accounts/1/contacts/3"},
		{"deleteFolder", "/api/v1/accounts/1/folders/SumFailure"},
		{"deleteFolder", "/api/v1/accounts/1/folders/DeleteFailure"},
		{"deleteFolder", "/api/v1/accounts/4/folders/Work"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			s := fixture.scenarios["found"]
			before := len(fixture.store.snapshot())
			req := httptest.NewRequest(http.MethodDelete, tc.path, nil)
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("got %d %s, want sanitized 500", rr.Code, rr.Body.String())
			}
			calls := fixture.store.snapshot()[before:]
			if tc.op == "deleteFolder" {
				if len(calls) < 3 || calls[0].query != "BEGIN" || calls[len(calls)-1].query != "ROLLBACK" {
					t.Fatalf("failure not rolled back: %v", calls)
				}
				if strings.Contains(tc.path, "SumFailure") && len(calls) != 3 {
					t.Fatalf("failed sum attempted another operation: %v", calls)
				}
			} else if len(calls) != 1 {
				t.Fatal("failed contact lookup attempted deletion")
			}
		})
	}
}
