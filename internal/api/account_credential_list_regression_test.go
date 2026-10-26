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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
)

type accountListKey struct {
	accountReadKey
	kind string
}

// Fixed SQL rows exercise the real repositories, grant loader, secret verifiers,
// production routes and response writers. They do not prove PostgreSQL joins,
// ordering, revocation filtering or credential-use/audit persistence.
type accountCredentialListConnection struct {
	capabilityExampleConnection
	mode string
	keys map[string]accountListKey
}

func (c accountCredentialListConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c accountCredentialListConnection) Driver() driver.Driver { return c }

func (c accountCredentialListConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c accountCredentialListConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("credential list attempted a write")
}

func (c accountCredentialListConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if strings.Contains(query, "JOIN grants g") {
		for _, restriction := range []string{"g.scope = 'mailbox:'", "g.holder_kind =", "g.holder_id =", "g.mode =", "g.expires_at", "c.name IN", "mailboxes.id <>", "ORDER BY mailboxes.address", "DISTINCT"} {
			if !strings.Contains(query, restriction) {
				return nil, fmt.Errorf("account list lost restriction %s", restriction)
			}
		}
		if c.mode == "list_failure" {
			return nil, errors.New("private account list detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "address", "display_name"}}
		if c.mode != "domain_manager" && c.mode != "no_grants" {
			r.rows = [][]driver.Value{{int64(2), "empty@example.com", "Empty"}, {int64(1), "linked@example.com", "Linked café"}}
		}
		return r, nil
	}
	if strings.Contains(query, "JOIN role_capabilities") {
		if !strings.Contains(query, "g.holder_id") {
			return nil, errors.New("grant list lost holder")
		}
		if c.mode == "grant_failure" {
			return nil, errors.New("private grant list detail")
		}
		r := &authReadSnapshotRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}}
		switch c.mode {
		case "no_grants":
		case "write_only":
			r.rows = [][]driver.Value{{"domain:example.com", "mail:write", nil, nil}}
		case "domain_manager":
			r.rows = [][]driver.Value{{"domain:example.com", "mailbox:manage", nil, nil}}
		default:
			for _, address := range []string{"linked@example.com", "empty@example.com", "account4@example.com"} {
				for _, cap := range []string{"mail:read", "mailbox:manage"} {
					r.rows = append(r.rows, []driver.Value{"mailbox:" + address, cap, nil, nil})
				}
			}
		}
		return r, nil
	}
	if len(args) == 0 {
		return nil, errors.New("credential list lost restriction")
	}
	id := fmt.Sprint(args[0].Value)
	if strings.Contains(query, `FROM "credentials"`) {
		if strings.Contains(query, "public_id =") {
			key, ok := c.keys[id]
			r := &authReadSnapshotRows{columns: []string{"id", "account_id", "kind", "public_id", "secret_hash", "scope", "last_used_at"}}
			if ok {
				r.rows = [][]driver.Value{{int64(90), int64(10), key.kind, id, key.hash, key.scope, time.Now()}}
			}
			return r, nil
		}
		for _, restriction := range []string{"account_id =", "revoked_at IS NULL", "kind IN", "created_at DESC, id DESC"} {
			if !strings.Contains(query, restriction) {
				return nil, fmt.Errorf("credential list lost restriction %s", restriction)
			}
		}
		if id == "4" {
			return nil, errors.New("private credential list detail")
		}
		if len(args) != 2 {
			return nil, errors.New("credential list lost kind")
		}
		kind := fmt.Sprint(args[1].Value)
		if kind != models.CredentialKindAppPassword && kind != models.CredentialKindAPIKey {
			return nil, errors.New("unexpected credential kind")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "account_id", "kind", "name", "public_id", "created_at", "last_used_at", "expires_at", "secret_hash", "encrypted_secret", "scope"}}
		if id != "2" {
			permissions := []byte(`{"permissions":[{"capability":"mail:read","scope":"mailbox:linked@example.com"},{"capability":"mail:send","scope":"domain:example.com"}]}`)
			r.rows = [][]driver.Value{
				{int64(102), int64(1), kind, "Phone café", "public-new", stamp, stamp.Add(time.Hour), stamp.AddDate(1, 0, 0), "private-hash-sentinel", "private-secret-sentinel", permissions},
				{int64(101), int64(1), kind, "Expired", "public-old", stamp.AddDate(0, 0, -2), nil, stamp.AddDate(-1, 0, 0), "private-hash-sentinel", "private-secret-sentinel", []byte(`{}`)},
			}
		}
		return r, nil
	}
	if strings.Contains(query, `FROM "mailboxes"`) {
		if strings.Contains(query, `SELECT "address"`) {
			if c.mode == "ownership_failure" {
				return nil, errors.New("private owner detail")
			}
			if id != "10" || !strings.Contains(query, "active") {
				return nil, errors.New("implicit owner lost active account")
			}
			return &authReadSnapshotRows{columns: []string{"address"}, rows: [][]driver.Value{{"user@example.com"}}}, nil
		}
		if strings.Contains(query, "address") && !strings.Contains(query, `"mailboxes"."id" =`) {
			// The real app-password verifier checks the addressed mailbox too.
			id = "10"
		}
		if id == "3" || (id == "10" && c.mode == "primary_failure") {
			return nil, errors.New("private primary account detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "address", "display_name", "active", "password_change_required", "two_factor_required"}}
		if id != "2147483647" && !(id == "10" && c.mode == "primary_missing") {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, err
			}
			r.rows = [][]driver.Value{{n, accountReadAddress(id), "Account café", true, false, false}}
		}
		return r, nil
	}
	return nil, fmt.Errorf("unexpected credential list SQL: %s", query)
}

func accountCredentialListScenarios(t *testing.T) map[string]accountReadScenario {
	t.Helper()
	return accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return accountCredentialListConnection{mode: mode, keys: keys}
	})
}

func accountListScenarios(t *testing.T, connection func(string, map[string]accountListKey) driver.Connector, configure ...func(*config.Config)) map[string]accountReadScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, permission := range map[string]auth.ScopedPermission{
		"key_read":        {Capability: "mail:read", Scope: "mailbox:linked@example.com"},
		"key_wrong_scope": {Capability: "mail:read", Scope: "mailbox:other@example.com"},
		"key_write_only":  {Capability: "mail:write", Scope: "mailbox:linked@example.com"},
		"key_domain":      {Capability: "mail:read", Scope: "domain:example.com"},
	} {
		token, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		scope, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{permission}})
		if err != nil {
			t.Fatal(err)
		}
		keys[public] = accountListKey{accountReadKey{hash: hash, scope: scope}, models.CredentialKindAPIKey}
		tokens[name] = token
	}
	password, public, hash, err := auth.NewAppPassword()
	if err != nil {
		t.Fatal(err)
	}
	keys[public] = accountListKey{accountReadKey{hash: hash, scope: []byte(`{}`)}, models.CredentialKindAppPassword}
	makeRouter := func(mode string, limited bool) http.Handler {
		pool := sql.OpenDB(connection(mode, keys))
		t.Cleanup(func() { _ = pool.Close() })
		cfg := &config.Config{Environment: "test", APIMaxBodyBytes: 8, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}
		for _, apply := range configure {
			apply(cfg)
		}
		r := NewRouters(openAccountReadDB(t, pool), jwt, cfg, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	normal, current := makeRouter("normal", false), mailboxToken(t, jwt)
	setup, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	withBasic := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.SetBasicAuth("user@example.com", password)
		normal.ServeHTTP(w, r)
	})
	withCookie := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Del("Authorization")
		r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: current})
		normal.ServeHTTP(w, r)
	})
	out := map[string]accountReadScenario{}
	for _, name := range []string{"found", "primary", "empty", "missing", "inaccessible", "target_unavailable", "content_unavailable", "invalid_id", "invalid_format", "request_too_large"} {
		out[name] = accountReadScenario{normal, current}
	}
	for name, mode := range map[string]string{"no_grants": "no_grants", "domain_manager": "domain_manager", "write_only": "write_only", "grant_unavailable": "grant_failure", "ownership_unavailable": "ownership_failure", "primary_unavailable": "primary_failure", "primary_missing": "primary_missing", "list_unavailable": "list_failure"} {
		out[name] = accountReadScenario{makeRouter(mode, false), current}
	}
	out["app_password"] = accountReadScenario{withBasic, ""}
	out["cookie_session"] = accountReadScenario{withCookie, ""}
	out["unauthenticated"] = accountReadScenario{normal, ""}
	out["setup_required"] = accountReadScenario{normal, setup.AccessToken}
	out["session_unavailable"] = accountReadScenario{unavailable, current}
	for name, token := range tokens {
		out[name] = accountReadScenario{normal, token}
	}
	out["key_holder_missing"] = accountReadScenario{makeRouter("write_only", false), tokens["key_read"]}
	out["key_permissions_unavailable"] = accountReadScenario{makeRouter("grant_failure", false), tokens["key_read"]}
	limited := makeRouter("normal", true)
	out["rate_limited"] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prime := httptest.NewRecorder()
		limited.ServeHTTP(prime, r.Clone(r.Context()))
		if prime.Code != 401 && prime.Code != 429 {
			t.Errorf("unexpected refusal primer: %d", prime.Code)
		}
		limited.ServeHTTP(w, r)
	}), "rmk_invalid"}
	return out
}

func TestAccountListDependencyFailureIsNotMissing(t *testing.T) {
	scenario := accountCredentialListScenarios(t)["primary_unavailable"]
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+scenario.token)
	rr := httptest.NewRecorder()
	scenario.handler.ServeHTTP(rr, req)
	if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
		t.Fatalf("got %d %s, want sanitized 500", rr.Code, rr.Body.String())
	}
}
