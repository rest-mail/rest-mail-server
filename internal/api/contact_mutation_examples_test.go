package api

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/restmail/restmail"
)

var contactMutationPaths = map[string]string{"createContact": "/api/v1/accounts/{id}/contacts", "updateContact": "/api/v1/accounts/{id}/contacts/{cid}", "blockSender": "/api/v1/accounts/{id}/contacts/block"}

type contactMutationConnection struct {
	mailRemovalConnection
	phase   int
	current map[string]driver.Value
}

func (c contactMutationConnection) Connect(context.Context) (driver.Conn, error) { return &c, nil }
func (c contactMutationConnection) Driver() driver.Driver                        { return c }
func (c contactMutationConnection) Open(string) (driver.Conn, error)             { return &c, nil }
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
		id, mailbox, email := int64(1), int64(1), "before@example.com"
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
func TestContactMutationExamples(t *testing.T) {
	f := newContactMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range contactMutationPaths {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario:%s", name)
				}
				method := http.MethodPost
				if op == "updateContact" {
					method = http.MethodPatch
				}
				body := []byte(pair.Request.Body)
				if name == "malformed_json" {
					body = []byte("{")
				}
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				before := len(f.store.snapshot())
				req := httptest.NewRequest(method, messageCollectionReadURI(path, pair), bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s want%d", rr.Code, rr.Body, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				calls := f.store.snapshot()[before:]
				writes := 0
				var input map[string]interface{}
				_ = json.Unmarshal(pair.Request.Body, &input)
				for _, m := range calls {
					if strings.HasPrefix(m.query, "SELECT") {
						if op == "blockSender" && strings.Contains(m.query, "email =") {
							if m.args[0].Value != int64(pair.Request.Path["id"].(float64)) || m.args[1].Value != input["email"] {
								t.Fatal("block lookup changed mailbox/email")
							}
						} else {
							cid := int64(1)
							if op == "updateContact" {
								cid = int64(pair.Request.Path["cid"].(float64))
							}
							if m.args[0].Value != cid || m.args[1].Value != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("contact lookup changed row/mailbox")
							}
						}
						continue
					}
					writes++
					var fields map[string]driver.Value
					var err error
					if strings.HasPrefix(m.query, "INSERT") {
						fields, err = exampleInsertFields(m.query, m.args)
						if fields["mailbox_id"] != int64(pair.Request.Path["id"].(float64)) || fields["email"] != input["email"] || fields["source"] != "manual" {
							t.Fatal("contact creation changed mailbox/email/source")
						}
					} else {
						fields, err = vacationMutationFields(m.query, m.args)
						cid := int64(1)
						if op == "updateContact" {
							cid = int64(pair.Request.Path["cid"].(float64))
						}
						if fields["id"] != cid {
							t.Fatal("contact write changed loaded row")
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					if rr.Code < 300 {
						var expected struct{ Data map[string]interface{} }
						if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
							t.Fatal(err)
						}
						for _, key := range []string{"email", "name", "trust_level", "source"} {
							if value, ok := fields[key]; ok && value != expected.Data[key] {
								t.Fatalf("stored %s=%v differs from response", key, value)
							}
						}
					}
				}
				wantWrite := rr.Code < 300 || name == "duplicate" || name == "write_unavailable" || name == "create_unavailable" || name == "reload_unavailable" || name == "reload_missing"
				if op == "updateContact" && (name == "unchanged" || name == "null_fields") {
					wantWrite = false
				}
				if (writes == 1) != wantWrite || writes > 1 {
					t.Fatalf("writes=%d want%t", writes, wantWrite)
				}
				if name == "lookup_unavailable" && len(calls) != 1 {
					t.Fatal("failed lookup proceeded")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := contactMutationPaths[w.OperationID]; ok {
				t.Errorf("contact mutation warning:%v", w)
			}
		}
	}
}
func TestContactMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for contact mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newContactMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range contactMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario:%s", name)
				}
				method := http.MethodPost
				if op == "updateContact" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, method, path, name, pair, 513)
			})
		}
	}
}
