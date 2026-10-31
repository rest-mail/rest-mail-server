package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const accountReadScript = "require [\"fileinto\"];\nfileinto \"Archive\";\n"

type accountReadKey struct {
	hash  string
	scope []byte
}

type accountReadState struct {
	mode  string
	keys  map[string]accountReadKey
	mu    sync.Mutex
	reads map[string]int
}

// The real account resolver loads grants and implicit ownership from these SQL
// rows. Ephemeral API keys pass the production parser, hash check and ceiling.
// No PostgreSQL semantics, credential-use persistence or refusal-audit claim.
type accountReadConnection struct {
	capabilityExampleConnection
	state *accountReadState
}

func (c accountReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c accountReadConnection) Driver() driver.Driver { return c }

func (c accountReadConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c accountReadConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("account read attempted a write")
}

func accountReadAddress(id string) string {
	switch id {
	case "1":
		return "linked@example.com"
	case "2":
		return "empty@example.com"
	case "6":
		return "unlimited@example.com"
	case "10":
		return "user@example.com"
	case "20":
		return "other@example.com"
	}
	return "account" + id + "@example.com"
}

func (c accountReadConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, ok := defaultServerCeilingRows(query, args); ok {
		return rows, nil
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if strings.Contains(query, "JOIN role_capabilities") {
		if c.state.mode == "grant_failure" {
			return nil, errors.New("private grant lookup detail")
		}
		if !strings.Contains(query, "g.holder_id") {
			return nil, errors.New("grant lookup lost holder")
		}
		r := &authReadSnapshotRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}}
		capability := "mail:read"
		if c.state.mode == "write_only" {
			capability = "mail:write"
		}
		if c.state.mode == "domain_grant" || c.state.mode == "write_only" {
			r.rows = [][]driver.Value{{"domain:example.com", capability, nil, nil}}
		} else {
			for _, id := range []string{"1", "2", "4", "5", "6", "7", "8"} {
				r.rows = append(r.rows, []driver.Value{"mailbox:" + accountReadAddress(id), capability, nil, nil})
			}
		}
		r.rows = withSessionRows(r.rows)
		return r, nil
	}
	if len(args) == 0 {
		return nil, errors.New("account read lost restriction")
	}
	id := fmt.Sprint(args[0].Value)
	if strings.Contains(query, `FROM "credentials"`) {
		key, ok := c.state.keys[id]
		r := &authReadSnapshotRows{columns: []string{"id", "account_id", "kind", "public_id", "secret_hash", "scope", "last_used_at"}}
		if ok {
			r.rows = [][]driver.Value{{int64(90), int64(10), "api_key", id, key.hash, key.scope, time.Now()}}
		}
		return r, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(query, `FROM "mailboxes"`):
		if strings.Contains(query, `SELECT "address"`) {
			if c.state.mode == "own_failure" {
				return nil, errors.New("private implicit-owner lookup detail")
			}
			if id != "10" || !strings.Contains(query, "active") {
				return nil, errors.New("implicit ownership lost active holder restriction")
			}
			return &authReadSnapshotRows{columns: []string{"address"}, rows: [][]driver.Value{{"user@example.com"}}}, nil
		}
		if !strings.Contains(query, `"mailboxes"."id" =`) {
			return nil, errors.New("account lookup lost ID")
		}
		if id == "3" {
			return nil, errors.New("private mailbox query detail")
		}
		c.state.mu.Lock()
		c.state.reads[id]++
		count := c.state.reads[id]
		c.state.mu.Unlock()
		if id == "8" && count > 1 {
			return nil, errors.New("private second mailbox query detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "address", "display_name", "active", "quota_bytes", "quota_used_bytes", "password_change_required", "two_factor_required"}}
		if id != "2147483647" && !(id == "7" && count > 1) {
			quota := int64(1024)
			if id == "6" {
				quota = 0
			}
			r.rows = [][]driver.Value{{n, accountReadAddress(id), "Account café", true, quota, int64(256), false, false}}
		}
		return r, nil
	case strings.Contains(query, `FROM "messages"`):
		if !strings.Contains(query, "count(*)") || !strings.Contains(query, "mailbox_id =") {
			return nil, errors.New("quota lost account count restriction")
		}
		if id == "5" {
			return nil, errors.New("private message count detail")
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(3)}}}, nil
	case strings.Contains(query, `FROM "sieve_scripts"`):
		if !strings.Contains(query, "mailbox_id =") {
			return nil, errors.New("Sieve lost account restriction")
		}
		if id == "4" {
			return nil, errors.New("private Sieve lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "script", "script_hash", "validated_at", "active", "created_at", "updated_at"}}
		if id != "2" {
			r.rows = [][]driver.Value{{n + 100, n, accountReadScript, fmt.Sprintf("%x", sha256.Sum256([]byte(accountReadScript))), stamp, true, stamp, stamp}}
		}
		return r, nil
	case strings.Contains(query, `FROM "vacation_configs"`):
		if !strings.Contains(query, "mailbox_id =") {
			return nil, errors.New("vacation lost account restriction")
		}
		if id == "4" {
			return nil, errors.New("private vacation lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "enabled", "subject", "body", "start_date", "end_date"}}
		if id != "2" {
			r.rows = [][]driver.Value{{n, n, true, "Away", "Back on Monday.", stamp, stamp.AddDate(0, 0, 7)}}
		}
		return r, nil
	}
	return nil, fmt.Errorf("unexpected account read SQL: %s", query)
}

type accountReadScenario struct {
	handler http.Handler
	token   string
}

func openAccountReadDB(t *testing.T, pool *sql.DB) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func accountReadScenarios(t *testing.T) map[string]accountReadScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	keys := map[string]accountReadKey{}
	tokens := map[string]string{}
	for name, permission := range map[string]auth.ScopedPermission{
		"key_read":        {Capability: "mail:read", Scope: "mailbox:linked@example.com"},
		"key_wrong_scope": {Capability: "mail:read", Scope: "mailbox:other@example.com"},
		"key_write_only":  {Capability: "mail:write", Scope: "mailbox:linked@example.com"},
	} {
		token, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		scope, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{permission}})
		if err != nil {
			t.Fatal(err)
		}
		keys[public] = accountReadKey{hash: hash, scope: scope}
		tokens[name] = token
	}
	makeRouter := func(mode string, limited bool) http.Handler {
		state := &accountReadState{mode: mode, keys: keys, reads: map[string]int{}}
		pool := sql.OpenDB(accountReadConnection{state: state})
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	normal := makeRouter("normal", false)
	current := mailboxToken(t, jwt)
	setup, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	limited := makeRouter("normal", true)
	// Prime the real malformed-key refusal budget using the same transport peer.
	// Refusal auditing is attempted but this read-only fixture does not persist it.
	throttled := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prime := httptest.NewRecorder()
		limited.ServeHTTP(prime, r.Clone(r.Context()))
		if prime.Code != 401 && prime.Code != 429 {
			t.Errorf("unexpected throttle primer: %d %s", prime.Code, prime.Body.String())
		}
		limited.ServeHTTP(w, r)
	})
	out := map[string]accountReadScenario{}
	for _, name := range []string{"found", "primary", "defaults", "unlimited", "missing", "disappeared", "internal_error", "content_unavailable", "count_unavailable", "mailbox_unavailable", "invalid_id", "invalid_format", "request_too_large"} {
		out[name] = accountReadScenario{normal, current}
	}
	out["grant_unavailable"] = accountReadScenario{makeRouter("grant_failure", false), current}
	out["ownership_unavailable"] = accountReadScenario{makeRouter("own_failure", false), current}
	out["write_only"] = accountReadScenario{makeRouter("write_only", false), current}
	out["domain_grant"] = accountReadScenario{makeRouter("domain_grant", false), current}
	out["unauthenticated"] = accountReadScenario{normal, ""}
	out["setup_required"] = accountReadScenario{normal, setup.AccessToken}
	out["session_unavailable"] = accountReadScenario{unavailable, current}
	out["rate_limited"] = accountReadScenario{throttled, "rmk_invalid"}
	for name, token := range tokens {
		out[name] = accountReadScenario{normal, token}
	}
	out["key_holder_missing"] = accountReadScenario{makeRouter("write_only", false), tokens["key_read"]}
	return out
}

func TestAccountReadFailuresAreNotEmptyOrDenied(t *testing.T) {
	scenarios := accountReadScenarios(t)
	for _, path := range []string{"/api/v1/accounts/3", "/api/v1/accounts/3/quota", "/api/v1/accounts/3/sieve", "/api/v1/accounts/3/vacation", "/api/v1/accounts/4/sieve", "/api/v1/accounts/4/vacation", "/api/v1/accounts/5/quota", "/api/v1/accounts/8/quota"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+scenarios["found"].token)
		rr := httptest.NewRecorder()
		scenarios["found"].handler.ServeHTTP(rr, req)
		if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
			t.Errorf("%s: got %d %s, want sanitized 500", path, rr.Code, rr.Body.String())
		}
	}
}
