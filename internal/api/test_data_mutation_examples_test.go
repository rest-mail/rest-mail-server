package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"golang.org/x/crypto/bcrypt"
)

var testDataMutationPaths = map[string]string{"resetTestData": "/api/v1/admin/test/reset", "seedTestData": "/api/v1/admin/test/seed"}

type testDataMutationContextKey struct{}
type testDataMutationConnection struct {
	domainMutationConnection
	mode    string
	nextID  int64
	tx      *testDataMutationTx
	aborted bool
	cleared bool
	cancel  context.CancelFunc
}
type testDataMutationTx struct {
	c        *testDataMutationConnection
	relevant bool
}

func (c *testDataMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *testDataMutationConnection) Driver() driver.Driver                        { return c }
func (c *testDataMutationConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *testDataMutationConnection) Begin() (driver.Tx, error) {
	tx := &testDataMutationTx{c: c}
	c.tx = tx
	return tx, nil
}
func (tx *testDataMutationTx) Commit() error {
	defer func() { tx.c.tx = nil; tx.c.aborted = false }()
	if !tx.relevant {
		return nil
	}
	if tx.c.aborted || tx.c.mode == "commit_failure" {
		tx.c.record("COMMIT_FAILED", nil)
		return errors.New("private transaction commit detail")
	}
	tx.c.record("COMMIT", nil)
	return nil
}
func (tx *testDataMutationTx) Rollback() error {
	if tx.relevant {
		tx.c.record("ROLLBACK", nil)
	}
	tx.c.tx = nil
	tx.c.aborted = false
	return nil
}
func (c *testDataMutationConnection) relevant(ctx context.Context, q string, args []driver.NamedValue) {
	if c.tx != nil && !c.tx.relevant {
		c.tx.relevant = true
		c.record("BEGIN", nil)
	}
	c.record(q, args)
	c.record("DATA_CONTEXT", []driver.NamedValue{{Value: ctx.Value(testDataMutationContextKey{}) == true}})
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}
func (c *testDataMutationConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT") {
		c.record("ROLLBACK_TO", nil)
		c.aborted = false
		return driver.RowsAffected(0), nil
	}
	if strings.HasPrefix(q, "RELEASE SAVEPOINT") {
		c.relevant(ctx, "RELEASE", nil)
		return driver.RowsAffected(0), nil
	}
	if strings.HasPrefix(q, "SAVEPOINT") {
		c.relevant(ctx, "SAVEPOINT", nil)
		return driver.RowsAffected(0), nil
	}
	if strings.HasPrefix(q, "DELETE FROM ") {
		c.relevant(ctx, q, args)
		if c.aborted {
			return nil, &pgconn.PgError{Code: "25P02", Message: "fixture transaction aborted"}
		}
		if q == "DELETE FROM attachments" && (c.mode == "missing_table" || c.mode == "clear_failure") {
			c.aborted = true
			if c.mode == "missing_table" {
				return nil, &pgconn.PgError{Code: "42P01", Message: "optional fixture table absent"}
			}
			return nil, errors.New("private clear failure detail")
		}
		if strings.HasPrefix(q, "DELETE FROM domains ") {
			c.cleared = true
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "quota_usage"`) {
		_, err := c.insert(ctx, q, args)
		return driver.RowsAffected(1), err
	}
	return nil, errors.New("unexpected ancillary write")
}

var testDataInsertTable = regexp.MustCompile(`^INSERT INTO "([a-z_]+)"`)

func (c *testDataMutationConnection) insert(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	match := testDataInsertTable.FindStringSubmatch(q)
	if len(match) != 2 {
		return nil, errors.New("invalid fixture insertion")
	}
	table := match[1]
	fields, err := exampleInsertFields(q, args)
	if err != nil {
		return nil, err
	}
	if table == "credentials" {
		hash, ok := fields["secret_hash"].(string)
		if !ok {
			return nil, errors.New("password credential lost hash")
		}
		if _, err := bcrypt.Cost([]byte(strings.TrimPrefix(hash, "{BLF-CRYPT}"))); err != nil || fields["kind"] != "password" {
			return nil, errors.New("fixture password was not hashed")
		}
		// Only the validated shape is retained; never store credential hash/token arguments.
		c.relevant(ctx, "PASSWORD_CREATED", []driver.NamedValue{{Value: fields["account_id"]}})
		if c.mode == "credential_failure" {
			return nil, errors.New("private credential failure detail")
		}
	} else {
		c.relevant(ctx, q, args)
	}
	if c.mode == "insert_"+table {
		return nil, errors.New("private fixture insertion detail")
	}
	c.nextID++
	column := "id"
	value := c.nextID
	if table == "quota_usage" {
		column = "mailbox_id"
		var ok bool
		value, ok = fields["mailbox_id"].(int64)
		if !ok || value == 0 {
			return nil, errors.New("quota insertion has no existing mailbox ID")
		}
	}
	return &authReadSnapshotRows{columns: []string{column}, rows: [][]driver.Value{{value}}}, nil
}
func (c *testDataMutationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, "INSERT INTO ") && (strings.Contains(q, `"domains"`) || strings.Contains(q, `"mailboxes"`) || strings.Contains(q, `"quota_usage"`) || strings.Contains(q, `"aliases"`) || strings.Contains(q, `"credentials"`)) {
		return c.insert(ctx, q, args)
	}
	if strings.HasPrefix(q, `UPDATE "credentials"`) {
		c.relevant(ctx, "PASSWORD_REVOKED", nil)
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	}
	for _, table := range []string{"domains", "mailboxes", "quota_usage", "aliases"} {
		if strings.Contains(q, `FROM "`+table+`"`) && len(args) > 0 && strings.Contains(q, "WHERE") && (strings.Contains(q, "name =") || strings.Contains(q, "address =") || strings.Contains(q, "mailbox_id =")) {
			c.relevant(ctx, q, args)
			if c.mode == "lookup_"+table {
				return nil, errors.New("private fixture lookup detail")
			}
			r := &authReadSnapshotRows{columns: []string{"id"}}
			if table == "quota_usage" {
				r.columns = []string{"mailbox_id"}
			}
			fresh := c.cleared || c.mode == "seed_created" || strings.HasPrefix(c.mode, "insert_") || c.mode == "credential_failure"
			if !fresh {
				c.nextID++
				id := c.nextID
				if table == "quota_usage" {
					var ok bool
					id, ok = args[0].Value.(int64)
					if !ok || id == 0 {
						return nil, errors.New("quota lookup has no existing mailbox ID")
					}
				}
				r.rows = [][]driver.Value{{id}}
			}
			return r, nil
		}
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func newTestDataMutationScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_admin":        {Capability: grants.Wildcard, Scope: "*"},
		"key_domain_read":  {Capability: middleware.CapDomainsRead, Scope: "*"},
		"key_domain":       {Capability: grants.Wildcard, Scope: "domain:example.com"},
		"key_mailbox":      {Capability: grants.Wildcard, Scope: "mailbox:owner@example.com"},
		"key_wrong_domain": {Capability: grants.Wildcard, Scope: "domain:other.test"},
	} {
		value, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		scope, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{p}})
		if err != nil {
			t.Fatal(err)
		}
		keys[public] = accountListKey{accountReadKey{hash: hash, scope: scope}, models.CredentialKindAPIKey}
		tokens[name] = value
	}
	previous := heldGrants
	defer func() { heldGrants = previous }()
	makeRouter := func(entries []grants.Entry, fail, production bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private snapshot grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &testDataMutationConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		environment := "test"
		if production {
			environment = "production"
		}
		r := NewRouters(db, jwt, &config.Config{Environment: environment, APIMaxBodyBytes: 512}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.mode = req.Header.Get("X-Fixture-Data-Mode")
			c.nextID = 100
			c.tx = nil
			c.aborted = false
			c.cleared = false
			ctx, cancel := context.WithCancel(context.WithValue(req.Context(), testDataMutationContextKey{}, true))
			defer cancel()
			c.cancel = nil
			if c.mode == "cancelled" {
				c.cancel = cancel
			}
			r.Public.ServeHTTP(w, req.WithContext(ctx))
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"resetTestData", "seedTestData"} {
		for name := range pairs[op] {
			out[name] = normal
		}
	}
	out["admin"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}, false, false)
	out["non_admin"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("example.com")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Mailbox("owner@example.com")}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}
	out["session_unavailable"] = bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}
	for name, key := range tokens {
		s := normal
		s.token = key
		out[name] = s
	}
	missing := out["non_admin"]
	missing.token = tokens["key_admin"]
	out["key_holder_missing"] = missing
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: token})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "domain-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "domain-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}, normal.store}
	}
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("other.test")}}, false, false)

	for _, name := range []string{"complete", "seed_created", "lookup_domains", "lookup_mailboxes", "lookup_quota_usage", "lookup_aliases", "insert_domains", "insert_mailboxes", "insert_quota_usage", "insert_aliases", "credential_failure", "clear_failure", "missing_table", "commit_failure", "cancelled"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Set("X-Fixture-Data-Mode", name)
			normal.handler.ServeHTTP(w, r)
		}), token}, normal.store}
	}
	out["production"] = makeRouter(wild, false, true)
	return out
}

func TestTestDataMutationFailures(t *testing.T) {
	f := newTestDataMutationScenarios(t)
	for _, op := range []string{"resetTestData", "seedTestData"} {
		modes := []string{"lookup_domains", "lookup_mailboxes", "lookup_quota_usage", "lookup_aliases", "credential_failure", "cancelled", "commit_failure"}
		if op == "resetTestData" {
			modes = append(modes, "clear_failure")
		}
		for _, mode := range modes {
			t.Run(op+"/"+mode, func(t *testing.T) {
				rr := mailboxSessionEndingRequest(t, f[mode], http.MethodPost, testDataMutationPaths[op], nil)
				if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
					t.Errorf("failure status%d, sanitized generic500=%t", rr.Code, rr.Body.String() == "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n")
				}
			})
		}
	}
}

func TestTestDataMutationExamples(t *testing.T) {
	f := newTestDataMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range testDataMutationPaths {
		if len(pairs[op]) < 20 {
			t.Fatalf("incomplete mutation examples%s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				var body []byte
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				rr := mailboxSessionEndingRequest(t, s, http.MethodPost, path, body)
				if rr.Code != pair.Response.Status {
					t.Fatalf("status%d want%d", rr.Code, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				calls := []bulkQueueExampleMutation{}
				if s.store != nil {
					calls = s.store.snapshot()[before:]
				}
				begins, commits, rollbacks, passwords, clears := 0, 0, 0, 0, 0
				lookups := map[string]int{}
				deleted := map[string]bool{}
				for _, c := range calls {
					switch c.query {
					case "BEGIN":
						begins++
					case "COMMIT":
						commits++
					case "ROLLBACK":
						rollbacks++
					case "PASSWORD_CREATED":
						passwords++
					case "DATA_CONTEXT":
						if c.args[0].Value != true {
							t.Fatal("test-data work lost HTTP context")
						}
					}
					if strings.HasPrefix(c.query, "DELETE FROM ") {
						clears++
						deleted[strings.Fields(c.query)[2]] = true
					}
					for _, table := range []string{"domains", "mailboxes", "quota_usage", "aliases"} {
						if strings.Contains(c.query, `FROM "`+table+`"`) && strings.HasPrefix(c.query, "SELECT") {
							lookups[table]++
						}
					}
				}
				if rr.Code == 200 {
					if begins != 1 || commits != 1 || rollbacks != 0 {
						t.Fatalf("successful fixture not one transaction: begin%d commit%d rollback%d", begins, commits, rollbacks)
					}
					expectedPasswords := 0
					if op == "resetTestData" || name == "seed_created" {
						expectedPasswords = 9
					}
					if passwords != expectedPasswords {
						t.Fatalf("password creations%d want%d", passwords, expectedPasswords)
					}
					for table, n := range map[string]int{"domains": 3, "mailboxes": 9, "quota_usage": 9, "aliases": 4} {
						if lookups[table] != n {
							t.Fatal(fmt.Sprintf("%s lookups%d want%d", table, lookups[table], n))
						}
					}
					if op == "resetTestData" {
						for _, table := range []string{"quota_usage", "quarantine", "domain_sender_rules"} {
							if !deleted[table] {
								t.Fatalf("reset did not target actual model table%s", table)
							}
						}
						if clears != 22 {
							t.Fatalf("clear statements%d want22", clears)
						}
					} else if clears != 0 {
						t.Fatal("seed cleared existing data")
					}
				} else if name == "commit_failure" {
					if begins != 1 || commits != 0 {
						t.Fatal("failed commit reported a successful commit")
					}
				} else if rr.Code == 500 {
					if begins != 1 || commits != 0 || rollbacks != 1 {
						t.Fatalf("failed mutation escaped rollback: begin%d commit%d rollback%d", begins, commits, rollbacks)
					}
				} else if len(calls) != 0 {
					t.Fatal("refused mutation accessed fixture resources")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := testDataMutationPaths[w.OperationID]; ok && gapWarningApplies(map[string]gapOperation{w.OperationID: {}}, w) {
				t.Errorf("mutation warning:%v", w)
			}
		}
	}
}
func TestTestDataMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newTestDataMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range testDataMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				checkQueueMutationPairWithVertrag(t, binary, f[name], http.MethodPost, path, name, pair, 513)
			})
		}
	}
}
func TestTestDataMutationProductionRoutesAbsent(t *testing.T) {
	s := newTestDataMutationScenarios(t)["production"]
	for _, path := range testDataMutationPaths {
		rr := mailboxSessionEndingRequest(t, s, http.MethodPost, path, nil)
		if rr.Code != 404 || len(s.store.snapshot()) != 0 {
			t.Fatal("production exposed test-data mutation")
		}
	}
}
