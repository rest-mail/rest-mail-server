package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

const banCreationPath = "/api/v1/admin/bans"

// Real GORM upsert/credential wiring uses controlled rows. The isolated database
// regression separately checks persistence and failed-update atomicity.
type banCreationConnection struct {
	accountCredentialListConnection
	store *bulkQueueExampleStore
}

func (c *banCreationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *banCreationConnection) Driver() driver.Driver                        { return c }
func (c *banCreationConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *banCreationConnection) Begin() (driver.Tx, error)                    { return bulkQueueExampleTx{}, nil }
func (c *banCreationConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}
func (c *banCreationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "bans"`) {
		c.record(q, args)
		fields, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		if fields["ip"] == "192.0.2.40" {
			return nil, errors.New("private ban insertion detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "bans"`) {
		c.record(q, args)
		if len(args) != 2 || !strings.Contains(q, "ip =") || args[1].Value != int64(1) {
			return nil, errors.New("ban lookup lost bound address or first row")
		}
		ip, ok := args[0].Value.(string)
		if !ok {
			return nil, errors.New("non-string ban address")
		}
		if ip == "192.0.2.30" {
			return nil, errors.New("private ban lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "ip", "reason", "protocol", "created_by", "expires_at", "created_at"}}
		if ip == "192.0.2.20" || ip == "192.0.2.21" {
			id := int64(20)
			if ip == "192.0.2.21" {
				id = 21
			}
			r.rows = [][]driver.Value{{id, ip, "Old reason", "smtp", "previous@restmail.test", sieveInstallationTime.Add(24 * time.Hour), sieveInstallationTime.Add(-24 * time.Hour)}}
		}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func (c *banCreationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "bans" SET`) {
		return nil, errors.New("unexpected ancillary ban write")
	}
	c.record(q, args)
	fields, err := vacationMutationFields(q, args)
	if err != nil {
		return nil, err
	}
	if len(fields) != 5 || (fields["id"] != int64(20) && fields["id"] != int64(21)) {
		return nil, errors.New("ban update changed identity, created time or selector")
	}
	if fields["id"] == int64(21) {
		return nil, errors.New("private ban update detail")
	}
	return driver.RowsAffected(1), nil
}
func newBanCreationScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":   {Capability: middleware.CapBansWrite, Scope: "*"},
		"key_read":    {Capability: middleware.CapBansRead, Scope: "*"},
		"key_domain":  {Capability: middleware.CapBansWrite, Scope: "domain:restmail.test"},
		"key_mailbox": {Capability: middleware.CapBansWrite, Scope: "mailbox:owner@restmail.test"},
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
	makeRouter := func(entries []grants.Entry, fail, limited bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private ban grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &banCreationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return sieveInstallationTime }
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{r.Public, token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	for _, name := range []string{"created", "duration_invalid", "ip_missing", "updated", "permanent", "clear_expiry", "null_duration", "zero_duration", "negative_duration", "fractional_duration", "compound_duration", "ipv6", "lookup_unavailable", "insert_unavailable", "update_unavailable", "protocol_missing", "protocol_invalid", "duration_overflow", "duration_wrong_type", "created_by_missing", "ip_empty", "ip_wrong_type", "unknown_field", "body_array", "malformed_json", "request_too_large", "body_boundary"} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapBansWrite, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapBansRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapBansWrite, Scope: grants.Domain("restmail.test")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapBansWrite, Scope: grants.Mailbox("owner@restmail.test")}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}
	out["session_unavailable"] = bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}
	for name, key := range tokens {
		s := normal
		s.token = key
		out[name] = s
	}
	missing := out["read_only"]
	missing.token = tokens["key_write"]
	out["key_holder_missing"] = missing
	limited := makeRouter(wild, false, true)
	out["rate_limited"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limited.handler.ServeHTTP(httptest.NewRecorder(), r.Clone(r.Context()))
		limited.handler.ServeHTTP(w, r)
	}), "rmk_invalid"}, limited.store}
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: token})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "ban-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "ban-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}, normal.store}
	}
	for _, name := range []string{"protocol_smtp", "protocol_imap", "protocol_pop3", "created_by_empty"} {
		out[name] = normal
	}
	return out
}

func TestBanCreationFailuresDoNotSucceed(t *testing.T) {
	s := newBanCreationScenarios(t)["created"]
	for _, ip := range []string{"192.0.2.30", "192.0.2.21"} {
		t.Run(ip, func(t *testing.T) {
			body, _ := json.Marshal(map[string]interface{}{"ip": ip, "protocol": "all", "created_by": "admin@restmail.test"})
			r := httptest.NewRequest(http.MethodPost, banCreationPath, bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, r)
			if rr.Code != 500 {
				t.Fatalf("ban dependency failure got%d %s want500", rr.Code, rr.Body)
			}
		})
	}
}
func TestBanCreationExamples(t *testing.T) {
	f := newBanCreationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["createBan"]) < 30 {
		t.Fatal("incomplete ban creation pairs")
	}
	for name, pair := range pairs["createBan"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			if name == "body_boundary" {
				if len(body) > 512 {
					t.Fatal("oversized boundary")
				}
				body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			r := httptest.NewRequest(http.MethodPost, banCreationPath, bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			if s.token != "" {
				r.Header.Set("Authorization", "Bearer "+s.token)
			}
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, r)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			writes := 0
			if s.store != nil {
				for _, m := range s.store.snapshot()[before:] {
					if strings.HasPrefix(m.query, "SELECT") {
						continue
					}
					writes++
					var fields map[string]driver.Value
					var err error
					insert := strings.HasPrefix(m.query, "INSERT")
					if insert {
						fields, err = exampleInsertFields(m.query, m.args)
					} else {
						fields, err = vacationMutationFields(m.query, m.args)
					}
					if err != nil {
						t.Fatal(err)
					}
					if rr.Code < 300 {
						var answer struct {
							Data struct {
								ID        uint
								IP        string
								Reason    string
								Protocol  string
								CreatedBy string     `json:"created_by"`
								ExpiresAt *time.Time `json:"expires_at"`
								CreatedAt time.Time  `json:"created_at"`
							}
						}
						if err := json.Unmarshal(pair.Response.Body, &answer); err != nil {
							t.Fatal(err)
						}
						for k, want := range map[string]interface{}{"reason": answer.Data.Reason, "protocol": answer.Data.Protocol, "created_by": answer.Data.CreatedBy} {
							if fields[k] != want {
								t.Fatalf("stored%s=%v want%v", k, fields[k], want)
							}
						}
						if insert {
							if fields["ip"] != answer.Data.IP || fields["created_at"] != sieveInstallationTime {
								t.Fatal("insert changed address/time")
							}
						} else if fields["id"] != int64(answer.Data.ID) {
							t.Fatal("upsert changed primary key")
						}
						if answer.Data.ExpiresAt == nil {
							if fields["expires_at"] != nil {
								t.Fatal("permanent ban retained an expiry")
							}
						} else if got, ok := fields["expires_at"].(time.Time); !ok || !got.Equal(*answer.Data.ExpiresAt) {
							t.Fatalf("expiry binding%v want%v", fields["expires_at"], answer.Data.ExpiresAt)
						}
					}
				}
			}
			want := rr.Code < 300 || name == "insert_unavailable" || name == "update_unavailable"
			if (writes == 1) != want || writes > 1 {
				t.Fatalf("writes%d want%t", writes, want)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "createBan" {
				t.Errorf("ban creation warning:%v", w)
			}
		}
	}
}
func TestBanCreationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for ban creation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newBanCreationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["createBan"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, s, http.MethodPost, banCreationPath, name, pair, 513)
		})
	}
}
