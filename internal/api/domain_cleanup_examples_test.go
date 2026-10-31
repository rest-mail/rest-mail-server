package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var domainCleanupTables = []string{"pipelines", "custom_filters", "domain_sender_rules", "certificates", "dkim_rotation_keys", "mtasts_policies", "domain_relays", "suppressions"}

type domainCleanupLedger struct {
	domains map[int64]map[string]any
	live    map[string]map[int64]int64
	grants  []map[string]any
	audit   []map[string]any
}

func (s domainCleanupLedger) clone() domainCleanupLedger {
	c := domainCleanupLedger{domains: map[int64]map[string]any{}, live: map[string]map[int64]int64{}, grants: append([]map[string]any(nil), s.grants...), audit: append([]map[string]any(nil), s.audit...)}
	for id, row := range s.domains {
		c.domains[id] = row
	}
	for name, rows := range s.live {
		c.live[name] = map[int64]int64{}
		for id, n := range rows {
			c.live[name][id] = n
		}
	}
	return c
}

// SQL is a controlled ledger, while routing, capability checks, grant removal,
// audit chaining and cache invalidation use their production implementations.
// PostgreSQL constraints and rollback are covered separately with DB_HOST in CI.
type domainCleanupConnection struct {
	importConnection
	ledger        domainCleanupLedger
	inTransaction bool
	readerLoads   int
	cancel        context.CancelFunc
}

func (c *domainCleanupConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *domainCleanupConnection) Driver() driver.Driver                        { return c }
func (c *domainCleanupConnection) Open(string) (driver.Conn, error)             { return c, nil }

type domainCleanupTransaction struct {
	c        *domainCleanupConnection
	saved    domainCleanupLedger
	imported importLedger
}

func (t domainCleanupTransaction) Commit() error { t.c.inTransaction = false; return nil }
func (t domainCleanupTransaction) Rollback() error {
	t.c.ledger = t.saved
	t.c.state = t.imported
	t.c.inTransaction = false
	return nil
}
func (c *domainCleanupConnection) Begin() (driver.Tx, error) {
	c.inTransaction = true
	return domainCleanupTransaction{c, c.ledger.clone(), c.state}, nil
}
func (c *domainCleanupConnection) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Begin()
}

func cleanupTable(q string) string {
	start := strings.Index(q, `"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(q[start+1:], `"`)
	if end < 0 {
		return ""
	}
	return q[start+1 : start+1+end]
}
func cleanupID(args []driver.NamedValue) int64 {
	for _, arg := range args {
		if id, ok := arg.Value.(int64); ok {
			return id
		}
	}
	return 0
}
func (c *domainCleanupConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.queries = append(c.queries, q)
	if strings.HasPrefix(q, "SELECT count(*)") {
		var n int64
		if strings.Contains(q, "grants AS g") {
			n = 1
		} else if strings.Contains(q, `FROM "mailboxes"`) && c.mode == "account" {
			n = 1
		} else if strings.Contains(q, `FROM "aliases"`) && c.mode == "alias" {
			n = 1
		}
		return invitationRows([]string{"count"}, []driver.Value{n}), nil
	}
	if strings.Contains(q, "JOIN role_capabilities") {
		cols := []string{"scope", "capability", "expires_at", "activation_expires_at"}
		if cleanupID(args) != 2 {
			return invitationRows(cols, []driver.Value{"*", "*", nil, nil}), nil
		}
		c.readerLoads++
		var rows [][]driver.Value
		for _, g := range c.ledger.grants {
			if g["scope"] == "domain:example.test" {
				rows = append(rows, []driver.Value{g["scope"], "domains:read", nil, nil})
			}
		}
		return grantLookupRows(cols, rows...), nil
	}
	if strings.Contains(q, `FROM "domains"`) {
		id := cleanupID(args)
		row, ok := c.ledger.domains[id]
		if !ok {
			return invitationRows([]string{"id"}), nil
		}
		if strings.HasPrefix(q, `SELECT "name"`) {
			return invitationRows([]string{"name"}, []driver.Value{row["name"]}), nil
		}
		return importRows(row), nil
	}
	if strings.Contains(q, `FROM "grants"`) {
		return importRows(c.ledger.grants...), nil
	}
	if strings.Contains(q, `FROM "roles"`) {
		return importRows(map[string]any{"id": int64(7), "name": "domain-reader"}), nil
	}
	if strings.Contains(q, `FROM "grant_activations"`) {
		return invitationRows([]string{"id"}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "domains"`) {
		row, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		row["id"] = int64(50 + len(c.ledger.domains))
		c.ledger.domains[row["id"].(int64)] = row
		return invitationRows([]string{"id"}, []driver.Value{row["id"]}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "pipelines"`) {
		row, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		c.ledger.live["pipelines"][row["domain_id"].(int64)]++
		return invitationRows([]string{"id"}, []driver.Value{int64(900)}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
		row, err := invitationFields(q, args)
		if err != nil {
			return nil, err
		}
		if c.mode == "audit_failure" && c.inTransaction {
			c.failed = true
			return nil, errors.New("private grant audit failure")
		}
		c.ledger.audit = append(c.ledger.audit, row)
		c.state.auditSeq = row["seq"].(int64)
		c.state.auditHash = row["hash"].(string)
		return invitationRows([]string{"seq"}, []driver.Value{row["seq"]}), nil
	}
	return c.importConnection.QueryContext(ctx, q, args)
}
func (c *domainCleanupConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.queries = append(c.queries, q)
	if strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
		rows, err := c.QueryContext(ctx, q, args)
		if rows != nil {
			_ = rows.Close()
		}
		return driver.RowsAffected(1), err
	}
	if strings.HasPrefix(q, "DELETE FROM ") {
		table, id := cleanupTable(q), cleanupID(args)
		if c.mode == "cleanup_failure" && table == "custom_filters" {
			c.failed = true
			return nil, errors.New("private cleanup failure")
		}
		if c.mode == "cancel" && table == "certificates" {
			c.failed = true
			c.cancel()
			return nil, ctx.Err()
		}
		if table == "domains" {
			delete(c.ledger.domains, id)
		} else if table == "grants" {
			kept := []map[string]any{}
			for _, g := range c.ledger.grants {
				if g["id"] != id {
					kept = append(kept, g)
				}
			}
			c.ledger.grants = kept
		} else if table != "grant_activations" {
			rows, ok := c.ledger.live[table]
			if !ok {
				return nil, fmt.Errorf("unexpected deletion of %s", table)
			}
			delete(rows, id)
		}
		return driver.RowsAffected(1), nil
	}
	return c.importConnection.ExecContext(ctx, q, args)
}

type domainCleanupFixture struct {
	c             *domainCleanupConnection
	router        http.Handler
	admin, reader string
}

func newDomainCleanupFixture(t *testing.T, mode string) *domainCleanupFixture {
	t.Helper()
	c := &domainCleanupConnection{importConnection: importConnection{mode: mode}, ledger: domainCleanupLedger{domains: map[int64]map[string]any{}, live: map[string]map[int64]int64{}}}
	for id, name := range map[int64]string{7: "example.test", 8: "other.test"} {
		c.ledger.domains[id] = map[string]any{"id": id, "name": name, "active": false, "server_type": "traditional"}
	}
	for _, table := range domainCleanupTables {
		c.ledger.live[table] = map[int64]int64{0: 1, 7: 1, 8: 1}
	}
	for i, scope := range []string{"domain:example.test", "mailbox:former@example.test", "domain:other.test"} {
		c.ledger.grants = append(c.ledger.grants, map[string]any{"id": int64(21 + i), "holder_id": int64(2), "holder_kind": "account", "role_id": int64(7), "scope": scope, "mode": "active"})
	}
	pool := sql.OpenDB(c)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, 7*24*time.Hour)
	admin, err := jwt.GenerateSession(1, "admin@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := jwt.GenerateSession(2, "reader@other.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	routers := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 2048}, nil)
	t.Cleanup(routers.Close)
	return &domainCleanupFixture{c, routers.Public, admin.AccessToken, reader.AccessToken}
}
func (f *domainCleanupFixture) request(ctx context.Context, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func TestDomainCleanupRegisteredRoutesRemoveLiveStateAndInvalidateCache(t *testing.T) {
	useRealGrants(t)
	f := newDomainCleanupFixture(t, "")
	ctx := t.Context()
	w := f.request(ctx, "GET", "/api/v1/admin/domains/7", "", f.reader)
	if w.Code != 200 {
		t.Fatalf("warm reader: %d %s; %v", w.Code, w.Body, f.c.queries)
	}
	w = f.request(ctx, "DELETE", "/api/v1/admin/domains/7", "", f.admin)
	if w.Code != 204 {
		t.Fatalf("delete: %d %s; %v", w.Code, w.Body, f.c.queries)
	}
	if _, ok := f.c.ledger.domains[7]; ok {
		t.Fatal("domain remains")
	}
	for table, rows := range f.c.ledger.live {
		if rows[7] != 0 || rows[8] != 1 || rows[0] != 1 {
			t.Fatalf("configuration escaped domain: %s %v", table, rows)
		}
	}
	if len(f.c.ledger.grants) != 1 || f.c.ledger.grants[0]["scope"] != "domain:other.test" {
		t.Fatalf("grants: %v", f.c.ledger.grants)
	}
	var grantAudits int
	for _, entry := range f.c.ledger.audit {
		if entry["action"] == "grants.delete" {
			grantAudits++
			if entry["actor_id"] != int64(1) || entry["status"] != int64(204) {
				t.Fatalf("wrong audit identity: %v", entry)
			}
		}
	}
	if grantAudits != 2 {
		t.Fatalf("grant audits=%d", grantAudits)
	}
	w = f.request(ctx, "POST", "/api/v1/admin/domains", `{"name":"example.test"}`, f.admin)
	if w.Code != 201 {
		t.Fatalf("recreate: %d %s; %v", w.Code, w.Body, f.c.queries)
	}
	var reply struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Data.ID == 7 {
		t.Fatal("domain identity reused")
	}
	w = f.request(ctx, "GET", fmt.Sprintf("/api/v1/admin/domains/%d", reply.Data.ID), "", f.reader)
	if w.Code != 403 || f.c.readerLoads < 2 {
		t.Fatalf("stale cached authority: %d %s loads=%d", w.Code, w.Body, f.c.readerLoads)
	}
}

func TestDomainCleanupRegisteredRoutesRollbackOnFailureAndCancellation(t *testing.T) {
	useRealGrants(t)
	for _, tc := range []struct {
		mode   string
		status int
	}{{"account", 409}, {"alias", 409}, {"cleanup_failure", 500}, {"audit_failure", 500}, {"cancel", 500}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newDomainCleanupFixture(t, tc.mode)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.c.cancel = cancel
			w := f.request(ctx, "GET", "/api/v1/admin/domains/7", "", f.reader)
			if w.Code != 200 {
				t.Fatalf("warm reader: %d %s", w.Code, w.Body)
			}
			loads := f.c.readerLoads
			w = f.request(ctx, "DELETE", "/api/v1/admin/domains/7", "", f.admin)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("refusal: %d %s; queries=%v", w.Code, w.Body, f.c.queries)
			}
			if _, ok := f.c.ledger.domains[7]; !ok {
				t.Fatal("domain deletion escaped rollback")
			}
			for table, rows := range f.c.ledger.live {
				if rows[7] != 1 || rows[8] != 1 || rows[0] != 1 {
					t.Fatalf("configuration changed after rollback: %s %v", table, rows)
				}
			}
			if len(f.c.ledger.grants) != 3 {
				t.Fatal("grant removal escaped rollback")
			}
			for _, entry := range f.c.ledger.audit {
				if entry["action"] == "grants.delete" {
					t.Fatal("grant audit escaped rollback")
				}
			}
			w = f.request(t.Context(), "GET", "/api/v1/admin/domains/7", "", f.reader)
			if w.Code != 200 || f.c.readerLoads != loads {
				t.Fatalf("rollback invalidated cache: %d %s loads=%d want%d", w.Code, w.Body, f.c.readerLoads, loads)
			}
			if strings.HasSuffix(tc.mode, "_failure") || tc.mode == "cancel" {
				if !f.c.failed {
					t.Fatal("fault was not reached")
				}
			}
		})
	}
}
