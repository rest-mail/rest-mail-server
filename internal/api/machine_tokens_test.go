package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type machineRouteConnection struct {
	domainAliasReadConnection
	granted, revoked bool
	creates          int
}

func (c *machineRouteConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *machineRouteConnection) Driver() driver.Driver                        { return c }
func (c *machineRouteConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *machineRouteConnection) Begin() (driver.Tx, error)                    { return bulkQueueExampleTx{}, nil }
func (c *machineRouteConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(q, `FROM "credentials"`):
		scope, _ := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{
			{Capability: "domains:read", Scope: "domain:example.com"},
			{Capability: "pipelines:read", Scope: "domain:example.com"},
			{Capability: "pipelines:write", Scope: "domain:example.com"},
		}})
		var revoked driver.Value
		if c.revoked {
			revoked = time.Now()
		}
		return &authReadSnapshotRows{columns: []string{"id", "account_id", "kind", "scope", "revoked_at"}, rows: [][]driver.Value{{int64(34), int64(12), models.CredentialKindClientSecret, string(scope), revoked}}}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		return &authReadSnapshotRows{columns: []string{"id", "address", "active", "owner_id"}, rows: [][]driver.Value{{int64(12), "robot@example.com", true, nil}}}, nil
	case strings.Contains(q, "FROM grants AS g"):
		rows := &authReadSnapshotRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}}
		if c.granted {
			for _, capability := range []string{"domains:read", "pipelines:read", "pipelines:write"} {
				rows.rows = append(rows.rows, []driver.Value{"domain:example.com", capability, nil, nil})
			}
		}
		rows.rows = withSessionRows(rows.rows)
		return rows, nil
	case strings.HasPrefix(q, `INSERT INTO "pipelines"`):
		if len(args) == 0 || args[0].Value != int64(1) {
			return nil, errors.New("pipeline write escaped its domain")
		}
		c.creates++
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(33)}}}, nil
	case strings.Contains(q, `FROM "pipelines"`) || strings.Contains(q, `FROM "custom_filters"`):
		if strings.Contains(q, "JOIN domains") {
			name := "example.com"
			if len(args) > 0 && args[0].Value == int64(2) {
				name = "other.test"
			}
			return &authReadSnapshotRows{columns: []string{"name"}, rows: [][]driver.Value{{name}}}, nil
		}
		if !strings.Contains(q, "domain_id IN (SELECT id FROM domains") {
			return nil, errors.New("pipeline list lost domain filtering")
		}
		if strings.Contains(q, "count(*)") {
			return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, nil
		}
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	}
	return c.domainAliasReadConnection.QueryContext(ctx, q, args)
}

// In-process authorization wiring with controlled SQL rows, not an external
// service or end-to-end environment. The repository and live grants run normally.
func TestMachineTokenRoutesUseLiveCredentialAndDomainScopes(t *testing.T) {
	useRealGrants(t)
	connection := &machineRouteConnection{granted: true}
	pool := sql.OpenDB(connection)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewJWTService("machine-route-secret", time.Hour, 24*time.Hour)
	token, err := tokens.GenerateMachineToken(12, "robot@example.com", 34)
	if err != nil {
		t.Fatal(err)
	}
	routers := NewRouters(db, tokens, &config.Config{Environment: "test", APIMaxBodyBytes: 4096}, nil)
	t.Cleanup(routers.Close)
	send := func(method, path, body string, want int) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		out := httptest.NewRecorder()
		routers.Public.ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, out.Code, out.Body, want)
		}
	}
	send("GET", "/api/v1/admin/domains/1", "", 200)
	send("GET", "/api/v1/admin/domains/2", "", 403)
	send("GET", "/api/v1/admin/pipelines", "", 200)
	send("GET", "/api/v1/admin/custom-filters", "", 200)
	send("PATCH", "/api/v1/admin/pipelines/2", `{"active":false}`, 403)
	send("POST", "/api/v1/admin/pipelines", `{"domain_id":2,"direction":"inbound","filters":[]}`, 403)
	send("POST", "/api/v1/admin/pipelines", `{"domain_id":1,"direction":"inbound","filters":[]}`, 201)
	if connection.creates != 1 {
		t.Fatalf("pipeline writes %d, want 1", connection.creates)
	}
	send("GET", "/api/v1/accounts/12/client-secrets", "", 401)
	send("POST", "/api/v1/auth/protocol-sign-in", `{"protocol":"imap"}`, 400)
	connection.granted = false
	send("GET", "/api/v1/admin/domains/1", "", 403)
	connection.granted = true
	connection.revoked = true
	send("GET", "/api/v1/admin/domains/1", "", 401)
}
