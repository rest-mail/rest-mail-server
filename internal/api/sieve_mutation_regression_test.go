package api

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
)

type sieveMutationConnection struct{ mailRemovalConnection }

func (c sieveMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c sieveMutationConnection) Driver() driver.Driver { return c }

func (c sieveMutationConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c sieveMutationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `DELETE FROM "sieve_scripts" WHERE mailbox_id =`) || len(args) != 1 {
		return nil, errors.New("unexpected Sieve mutation or lost mailbox")
	}
	c.record(q, args)
	if args[0].Value == int64(4) {
		return nil, errors.New("private Sieve delete detail")
	}
	if args[0].Value == int64(2) {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func newSieveMutationScenarios(t *testing.T) mailRemovalScenarios {
	t.Helper()
	store := &bulkQueueExampleStore{}
	key, public, hash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	scope, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: "mail:write", Scope: "domain:example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	out := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		keys[public] = accountListKey{accountReadKey{hash: hash, scope: scope}, models.CredentialKindAPIKey}
		base := mailResourceReadConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}}
		return sieveMutationConnection{mailRemovalConnection{base, store}}
	}, func(cfg *config.Config) { cfg.APIMaxBodyBytes = 256 })
	for _, name := range []string{"script_missing", "script_null", "valid", "empty_script", "valid_keep", "invalid_syntax", "malformed_json", "internal_error"} {
		out[name] = out["found"]
	}
	out["domain_grant"] = out["domain_manager"]
	out["primary_only"] = out["no_grants"]
	out["app_password_other"] = out["app_password"]
	out["key_write_domain"] = accountReadScenario{out["found"].handler, key}
	out["key_write_holder_missing"] = accountReadScenario{out["no_grants"].handler, out["key_write_only"].token}
	normal := out["found"]
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		out[name] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: normal.token})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "sieve-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "sieve-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong-csrf")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}
	}
	return mailRemovalScenarios{out, store}
}

func TestSieveDeletionFailureIsNotSuccess(t *testing.T) {
	f := newSieveMutationScenarios(t)
	s := f.scenarios["found"]
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/4/sieve", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)
	if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
		t.Fatalf("got %d %s, want sanitized500", rr.Code, rr.Body.String())
	}
}

func TestSieveValidationNeverWrites(t *testing.T) {
	f := newSieveMutationScenarios(t)
	s := f.scenarios["found"]
	for _, script := range []string{"", "keep;", "if"} {
		t.Run(script, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"script": script})
			before := len(f.store.snapshot())
			req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/1/sieve/validate", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+s.token)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			var value struct {
				Data struct {
					Valid   bool
					Message string
				}
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if rr.Code != 200 || value.Data.Valid != (script != "if") || value.Data.Message == "" {
				t.Fatalf("validation %q: %d %s", script, rr.Code, rr.Body.String())
			}
			if len(f.store.snapshot()) != before {
				t.Fatal("validation mutated Sieve storage")
			}
			t.Logf("%q: %s", script, rr.Body.String())
		})
	}
}
