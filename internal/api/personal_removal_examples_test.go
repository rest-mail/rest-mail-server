package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var personalRemovalPaths = map[string]string{"deleteContact": "/api/v1/accounts/{id}/contacts/{cid}", "deleteFolder": "/api/v1/accounts/{id}/folders/{folder}"}

type personalRemovalConnection struct {
	mailRemovalConnection
	current *personalRemovalTx
}

func (c personalRemovalConnection) Connect(context.Context) (driver.Conn, error) { return &c, nil }
func (c personalRemovalConnection) Driver() driver.Driver                        { return c }
func (c personalRemovalConnection) Open(string) (driver.Conn, error)             { return &c, nil }

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
func TestPersonalRemovalExamples(t *testing.T) {
	fixture := newPersonalRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range personalRemovalPaths {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := fixture.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				before := len(fixture.store.snapshot())
				req := httptest.NewRequest(http.MethodDelete, messageCollectionReadURI(path, pair), nil)
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				started := time.Now()
				s.handler.ServeHTTP(rr, req)
				finished := time.Now()
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
				}
				if rr.Code == 204 {
					if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
						t.Fatal("204 carried content")
					}
				} else {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				calls := fixture.store.snapshot()[before:]
				if op == "deleteContact" {
					wantRead := rr.Code == 204 || rr.Code == 404 || name == "lookup_unavailable" || name == "write_unavailable"
					wantWrite := rr.Code == 204 || name == "write_unavailable"
					read, writes := 0, 0
					for _, m := range calls {
						if strings.HasPrefix(m.query, "SELECT") && strings.Contains(m.query, `FROM "contacts"`) {
							read++
							if m.args[0].Value != int64(pair.Request.Path["cid"].(float64)) || m.args[1].Value != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("contact row/mailbox changed")
							}
						}
						if strings.HasPrefix(m.query, `DELETE FROM "contacts"`) {
							writes++
							if m.args[0].Value != int64(pair.Request.Path["cid"].(float64)) {
								t.Fatal("contact mutation changed ID")
							}
						}
					}
					if (read == 1) != wantRead || read > 1 || (writes == 1) != wantWrite || writes > 1 {
						t.Fatalf("contact read/write attempts %d/%d want %t/%t", read, writes, wantRead, wantWrite)
					}
				} else {
					wantWork := rr.Code == 204 || rr.Code == 404 || name == "sum_unavailable" || name == "delete_unavailable" || name == "quota_unavailable"
					if !wantWork {
						if len(calls) != 0 {
							t.Fatalf("refusal touched folder: %v", calls)
						}
						return
					}
					if len(calls) < 3 || calls[0].query != "BEGIN" {
						t.Fatalf("folder operation outside transaction: %v", calls)
					}
					end := "ROLLBACK"
					if rr.Code == 204 {
						end = "COMMIT"
					}
					if calls[len(calls)-1].query != end {
						t.Fatalf("folder transaction ended %s, want %s", calls[len(calls)-1].query, end)
					}
					wantOps := 3
					if name != "sum_unavailable" {
						wantOps++
					}
					if rr.Code == 204 && name != "empty_folder" || name == "quota_unavailable" {
						wantOps++
					}
					if len(calls) != wantOps {
						t.Fatalf("folder calls=%d want %d: %v", len(calls), wantOps, calls)
					}
					for _, m := range calls[1 : len(calls)-1] {
						if strings.Contains(m.query, `"messages"`) {
							if m.args[0].Value != int64(pair.Request.Path["id"].(float64)) || m.args[1].Value != pair.Request.Path["folder"] {
								t.Fatalf("folder mailbox/name changed: %v", m.args)
							}
						} else {
							// GORM adds the ordinary row update timestamp after the quota expression.
							if m.args[2].Value != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("quota changed mailbox")
							}
							stamp, ok := m.args[1].Value.(time.Time)
							if !ok || stamp.Before(started) || stamp.After(finished) {
								t.Fatal("quota timestamp outside request")
							}
						}
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := personalRemovalPaths[w.OperationID]; ok {
				t.Errorf("personal removal warning: %v", w)
			}
		}
	}
}
func TestPersonalRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for personal removal exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newPersonalRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range personalRemovalPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := fixture.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, http.MethodDelete, path, name, pair)
			})
		}
	}
}
