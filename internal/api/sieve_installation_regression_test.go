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
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
)

var sieveInstallationTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var sieveUpdateBinding = regexp.MustCompile(`"([a-z_]+)"\s*=\s*\$([0-9]+)`)

type sieveInstallationConnection struct{ mailRemovalConnection }

func (c sieveInstallationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c sieveInstallationConnection) Driver() driver.Driver { return c }

func (c sieveInstallationConnection) Open(string) (driver.Conn, error) { return c, nil }

func sieveInstallationFields(q string, args []driver.NamedValue) (map[string]driver.Value, error) {
	fields := map[string]driver.Value{}
	if strings.HasPrefix(q, `INSERT INTO "sieve_scripts"`) {
		start := strings.Index(q, "(")
		end := strings.Index(q, ") VALUES")
		if end < 0 {
			end = strings.Index(q, ")\n")
		}
		if start < 0 || end < start {
			return nil, errors.New("unexpected script insert layout")
		}
		cols := strings.Split(q[start+1:end], ",")
		if len(cols) != len(args) {
			return nil, errors.New("script insert field count changed")
		}
		for i, col := range cols {
			fields[strings.Trim(col, " \"\n\t")] = args[i].Value
		}
	} else {
		for _, match := range sieveUpdateBinding.FindAllStringSubmatch(q, -1) {
			index, err := strconv.Atoi(match[2])
			if err != nil || index < 1 || index > len(args) {
				return nil, errors.New("invalid update binding")
			}
			fields[match[1]] = args[index-1].Value
		}
	}
	return fields, nil
}

func (c sieveInstallationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "sieve_scripts"`) {
		c.record(q, args)
		fields, err := sieveInstallationFields(q, args)
		if err != nil {
			return nil, err
		}
		if strings.Contains(fmt.Sprint(fields["script"]), "# create unavailable") {
			return nil, errors.New("private Sieve insert detail")
		}
		if strings.Contains(q, `RETURNING "id", "mailbox_id"`) {
			return &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "script", "script_hash", "validated_at", "active", "created_at", "updated_at"}, rows: [][]driver.Value{{int64(1), fields["mailbox_id"], fields["script"], fields["script_hash"], fields["validated_at"], fields["active"], fields["created_at"], fields["updated_at"]}}}, nil
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "sieve_scripts"`) {
		if len(args) != 2 || !strings.Contains(q, "mailbox_id =") || args[1].Value != int64(1) {
			return nil, errors.New("script lookup lost mailbox or bound")
		}
		c.record(q, args)
		if args[0].Value == int64(4) {
			return nil, errors.New("private Sieve lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "script", "script_hash", "validated_at", "active", "created_at", "updated_at"}}
		if args[0].Value != int64(2) {
			r.rows = [][]driver.Value{{int64(1), args[0].Value, "keep;", strings.Repeat("a", 64), sieveInstallationTime, false, sieveInstallationTime, sieveInstallationTime}}
		}
		return r, nil
	}
	return c.mailRemovalConnection.QueryContext(ctx, q, args)
}

func (c sieveInstallationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "sieve_scripts" SET`) {
		return nil, errors.New("unexpected ancillary script write")
	}
	fields, err := sieveInstallationFields(q, args)
	if err != nil {
		return nil, err
	}
	if fields["id"] != int64(1) || len(fields) != 8 {
		return nil, errors.New("script save lost exact primary key or fields")
	}
	c.record(q, args)
	if strings.Contains(fmt.Sprint(fields["script"]), "# update unavailable") {
		return nil, errors.New("private Sieve update detail")
	}
	return driver.RowsAffected(1), nil
}

func newSieveInstallationScenarios(t *testing.T) mailRemovalScenarios {
	return newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector {
		return sieveInstallationConnection{base}
	})
}

func newMailRulesWriteScenarios(t *testing.T, connector func(mailRemovalConnection) driver.Connector, limits ...int64) mailRemovalScenarios {
	t.Helper()
	maxBodyBytes := int64(256)
	if len(limits) != 0 {
		maxBodyBytes = limits[0]
	}
	unavailable, _, _, jwt := authReadBatchRouters(t)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_read":         {Capability: "mail:read", Scope: "mailbox:linked@example.com"},
		"key_write_only":   {Capability: "mail:write", Scope: "mailbox:linked@example.com"},
		"key_write_domain": {Capability: "mail:write", Scope: "domain:example.com"},
		"key_wrong_scope":  {Capability: "mail:write", Scope: "mailbox:other@example.com"},
	} {
		token, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		scope, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{p}})
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
	store := &bulkQueueExampleStore{}
	makeRouter := func(mode string, limited bool) http.Handler {
		base := mailResourceReadConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}}
		pool := sql.OpenDB(connector(mailRemovalConnection{base, store}))
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return sieveInstallationTime }
		cfg := &config.Config{Environment: "test", APIMaxBodyBytes: maxBodyBytes, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}
		r := NewRouters(db, jwt, cfg, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	normal, current := makeRouter("normal", false), mailboxToken(t, jwt)
	out := map[string]accountReadScenario{}
	for _, name := range []string{"found", "valid", "script_missing", "script_null", "created", "active_false", "active_true", "active_null", "empty_script", "utf8_script", "invalid_syntax", "malformed_json", "create_unavailable", "update_unavailable", "lookup_unavailable", "invalid_id", "invalid_format", "request_too_large", "target_unavailable", "primary", "missing", "inaccessible"} {
		out[name] = accountReadScenario{normal, current}
	}
	for name, mode := range map[string]string{"no_grants": "no_grants", "domain_grant": "domain_manager", "write_only": "write_only", "grant_unavailable": "grant_failure", "ownership_unavailable": "ownership_failure"} {
		out[name] = accountReadScenario{makeRouter(mode, false), current}
	}
	out["primary_only"] = out["no_grants"]
	for name, token := range tokens {
		out[name] = accountReadScenario{normal, token}
	}
	out["key_write_holder_missing"] = accountReadScenario{out["no_grants"].handler, tokens["key_write_only"]}
	basic := accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.SetBasicAuth("user@example.com", password)
		normal.ServeHTTP(w, r)
	}), ""}
	out["app_password"] = basic
	out["app_password_other"] = basic
	setup, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	out["setup_required"] = accountReadScenario{normal, setup.AccessToken}
	out["unauthenticated"] = accountReadScenario{normal, ""}
	out["session_unavailable"] = accountReadScenario{unavailable, current}
	limited := makeRouter("normal", true)
	out["rate_limited"] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prime := httptest.NewRecorder()
		limited.ServeHTTP(prime, r.Clone(r.Context()))
		if prime.Code != 401 && prime.Code != 429 {
			t.Errorf("unexpected primer:%d", prime.Code)
		}
		limited.ServeHTTP(w, r)
	}), "rmk_invalid"}
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		out[name] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: current})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "install-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "install-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong-csrf")
			}
			normal.ServeHTTP(w, r)
		}), ""}
	}
	return mailRemovalScenarios{out, store}
}

func TestSieveInstallationDoesNotCreateOnLookupFailure(t *testing.T) {
	f := newSieveInstallationScenarios(t)
	s := f.scenarios["lookup_unavailable"]
	req := httptest.NewRequest(http.MethodPut, "/api/v1/accounts/4/sieve", strings.NewReader(`{"script":"keep;"}`))
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)
	if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
		t.Fatalf("got %d %s, want sanitized500", rr.Code, rr.Body.String())
	}
	if len(f.store.snapshot()) != 1 {
		t.Fatal("failed lookup attempted installation")
	}
}

func TestSieveInstallationPreservesExplicitInactiveCreation(t *testing.T) {
	f := newSieveInstallationScenarios(t)
	s := f.scenarios["active_false"]
	req := httptest.NewRequest(http.MethodPut, "/api/v1/accounts/2/sieve", strings.NewReader(`{"script":"keep;","active":false}`))
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)
	var value struct{ Data struct{ Active bool } }
	if err := json.Unmarshal(rr.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 201 || value.Data.Active {
		t.Fatalf("explicit false: got %d %s", rr.Code, rr.Body.String())
	}
	calls := f.store.snapshot()
	if len(calls) != 2 {
		t.Fatalf("lookup/insert calls=%d", len(calls))
	}
	fields, err := sieveInstallationFields(calls[1].query, calls[1].args)
	if err != nil {
		t.Fatal(err)
	}
	if fields["active"] != false {
		t.Fatalf("persisted active=%v, want false", fields["active"])
	}
}
