package api

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
)

var credentialRevocationPaths = map[string]string{
	"revokeCredential": "/api/v1/accounts/{id}/credentials/{credential_id}",
	"revokeAPIKey":     "/api/v1/accounts/{id}/api-keys/{credential_id}",
}

func newCredentialRevocationScenarios(t *testing.T) mailRemovalScenarios {
	t.Helper()
	f := newMailRemovalScenarios(t, func(base mailResourceReadConnection, store *bulkQueueExampleStore) driver.Connector {
		return credentialRevocationConnection{base.accountCredentialListConnection, store}
	})
	plain := f.scenarios["found"]
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	mfa, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range f.scenarios {
		if name == "setup_required" {
			continue
		}
		if _, err := jwt.ValidateAccessToken(s.token); err == nil {
			s.token = mfa.AccessToken
			f.scenarios[name] = s
		}
	}
	for _, name := range []string{"expired", "wrong_kind", "already_revoked", "foreign_owner", "audit_unavailable", "invalid_credential", "invalid_credential_format"} {
		f.scenarios[name] = f.scenarios["found"]
	}
	f.scenarios["no_two_factor"] = plain
	f.scenarios["domain_outside"] = f.scenarios["domain_manager"]
	f.scenarios["primary_only"] = f.scenarios["no_grants"]
	f.scenarios["invalid_token"] = accountReadScenario{plain.handler, "invalid-token"}
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		f.scenarios[name] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: mfa.AccessToken})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "revocation-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "revocation-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong-csrf")
			}
			plain.handler.ServeHTTP(w, r)
		}), ""}
	}
	return f
}

func TestCredentialRevocationExamples(t *testing.T) {
	f := newCredentialRevocationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range credentialRevocationPaths {
		if len(pairs[op]) < 25 {
			t.Fatalf("incomplete %s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				before := len(f.store.snapshot())
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
				calls := f.store.snapshot()[before:]
				writes, audits := 0, 0
				wantWrite := rr.Code == 204 || name == "missing" || name == "foreign_owner" || name == "wrong_kind" || name == "already_revoked" || name == "write_unavailable" || name == "audit_unavailable"
				wantAudit := rr.Code == 204 || name == "audit_unavailable"
				for _, m := range calls {
					if strings.HasPrefix(m.query, `UPDATE "credentials"`) {
						writes++
						kind := "app_password"
						if op == "revokeAPIKey" {
							kind = "api_key"
						}
						if m.args[1].Value != int64(pair.Request.Path["credential_id"].(float64)) || m.args[2].Value != int64(pair.Request.Path["id"].(float64)) || m.args[3].Value != kind {
							t.Fatalf("credential selectors changed: %v", m.args)
						}
						stamp, ok := m.args[0].Value.(time.Time)
						if !ok || stamp.Before(started) || stamp.After(finished) {
							t.Fatal("revocation timestamp outside request")
						}
					}
					if strings.HasPrefix(m.query, `INSERT INTO "audit_log"`) {
						fields, err := revocationAuditFields(m.query, m.args)
						if err != nil {
							t.Fatal(err)
						}
						// Refusal audit entries are separate from the successful revocation's entry.
						if fields["status"] != int64(204) {
							continue
						}
						audits++
						action := "credentials.revoke"
						if op == "revokeAPIKey" {
							action = "api_keys.revoke"
						}
						if fields["action"] != action || fields["outcome"] != "success" || fields["actor_id"] != int64(10) || fields["route"] != path {
							t.Fatalf("revocation audit metadata changed: %v", fields)
						}
						var target map[string]string
						if err := json.Unmarshal([]byte(fmt.Sprint(fields["target"])), &target); err != nil {
							t.Fatal(err)
						}
						id := strconv.FormatFloat(pair.Request.Path["id"].(float64), 'f', -1, 64)
						credential := strconv.FormatFloat(pair.Request.Path["credential_id"].(float64), 'f', -1, 64)
						if target["credential_id"] != credential || (target["account_id"] != id && target["id"] != id) {
							t.Fatalf("audit lost resource identity: %v", target)
						}
						if len(fmt.Sprint(fields["hash"])) != 64 || len(fmt.Sprint(fields["prev_hash"])) != 64 {
							t.Fatal("audit lost chain metadata")
						}
					}
				}
				if (writes == 1) != wantWrite || writes > 1 || (audits == 1) != wantAudit || audits > 1 {
					t.Fatalf("revocation/audit attempts %d/%d want %t/%t", writes, audits, wantWrite, wantAudit)
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := credentialRevocationPaths[w.OperationID]; ok {
				t.Errorf("credential revocation warning: %v", w)
			}
		}
	}
}

func TestCredentialRevocationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for revocation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newCredentialRevocationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range credentialRevocationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, http.MethodDelete, path, name, pair)
			})
		}
	}
}
