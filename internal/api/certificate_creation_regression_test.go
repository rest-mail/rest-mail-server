package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

const certificateCreationPath = "/api/v1/admin/certificates"

const certificateCreationMaster = "public-test-certificate-master"

const certificateCreationBodyLimit = 8192

var certificateCreationTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func certificateCreationMaterial(t *testing.T) (string, string) {
	t.Helper()
	key := signingSetterKey(t)
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		t.Fatal("public key fixture is not PEM")
	}
	var private crypto.Signer
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		private = k
	} else {
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			t.Fatal("parse public key fixture")
		}
		var ok bool
		private, ok = k.(crypto.Signer)
		if !ok {
			t.Fatal("public fixture cannot sign certificates")
		}
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "contract.example.org"}, DNSNames: []string{"contract.example.org"}, NotBefore: certificateCreationTime, NotAfter: certificateCreationTime.AddDate(0, 0, 30)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, private.Public(), private)
	if err != nil {
		t.Fatal("create public certificate fixture")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), key
}

func certificateCreationBody(t *testing.T, raw []byte) []byte {
	t.Helper()
	var body map[string]interface{}
	if json.Unmarshal(raw, &body) != nil {
		return raw
	}
	cert, key := certificateCreationMaterial(t)
	if body["cert_pem"] == "certificate" {
		body["cert_pem"] = cert
	}
	if body["key_pem"] == "key" {
		body["key_pem"] = key
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal("encode public certificate fixture")
	}
	return out
}

type certificateCreationConnection struct {
	domainMutationConnection
	target int64
	cancel context.CancelFunc
}

func (c *certificateCreationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *certificateCreationConnection) Driver() driver.Driver { return c }

func (c *certificateCreationConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *certificateCreationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "certificates"`) {
		c.mutation(q, args)
		f, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		c.current = f
		if f["domain_id"] != c.target {
			return nil, errors.New("certificate insert lost exact domain")
		}
		if c.target == 4 {
			return nil, errors.New("private certificate insert detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "certificates"`) {
		c.record(q, args)
		if len(args) < 2 || args[0].Value != int64(1) || !strings.Contains(q, `"certificates"."id" =`) {
			return nil, errors.New("certificate reload lost exact ID")
		}
		if c.target == 5 {
			return nil, errors.New("private certificate reload detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "cert_pem", "key_pem", "issuer", "not_before", "not_after", "auto_renew", "created_at", "updated_at"}}
		if c.target == 6 {
			return r, nil
		}
		f := c.current
		if f == nil {
			return nil, errors.New("certificate reloaded before insert")
		}
		r.rows = [][]driver.Value{{int64(1), f["domain_id"], f["cert_pem"], f["key_pem"], f["issuer"], f["not_before"], f["not_after"], f["auto_renew"], f["created_at"], f["updated_at"]}}
		return r, nil
	}
	if strings.Contains(q, `FROM "domains"`) {
		c.record(q, args)
		if len(args) < 1 {
			return nil, errors.New("certificate domain lookup lost ID")
		}
		id := args[0].Value.(int64)
		if c.current == nil {
			c.target = id
		}
		if id != c.target {
			return nil, errors.New("certificate domain association changed identity")
		}
		if id == 3 {
			return nil, errors.New("private certificate domain detail")
		}
		if c.current != nil && id == 7 {
			return nil, errors.New("private certificate association detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "sending_policy", "dmarc_policy", "created_at", "updated_at"}}
		if id == 2147483647 {
			return r, nil
		}
		r.rows = [][]driver.Value{{id, "contract.example.org", "traditional", false, int64(1073741824), "", "", "none", certificateCreationTime, certificateCreationTime}}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *certificateCreationConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("unexpected ancillary certificate write")
}

func newCertificateCreationScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: middleware.CapDomainsWrite, Scope: "*"},
		"key_read":         {Capability: middleware.CapDomainsRead, Scope: "*"},
		"key_domain":       {Capability: middleware.CapDomainsWrite, Scope: "domain:example.com"},
		"key_mailbox":      {Capability: middleware.CapDomainsWrite, Scope: "mailbox:owner@example.com"},
		"key_wrong_domain": {Capability: middleware.CapDomainsWrite, Scope: "domain:other.test"},
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
					return grants.Held{}, errors.New("private domain grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &certificateCreationConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return certificateCreationTime }
		if err := db.Callback().Query().After("gorm:after_query").Register("certificate:cancel-after-lookup", func(tx *gorm.DB) {
			if c.cancel != nil && tx.Statement.Table == "domains" && c.current == nil {
				c.cancel()
			}
		}); err != nil {
			t.Fatal(err)
		}
		r := NewRouters(db, jwt, &config.Config{Environment: "test", MasterKey: certificateCreationMaster, APIMaxBodyBytes: certificateCreationBodyLimit, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.reads = 0
			c.current = nil
			c.createdName = ""
			if req.Header.Get("X-Example-Cancel") == "after-certificate-lookup" {
				var ctx context.Context
				ctx, c.cancel = context.WithCancel(req.Context())
				defer func() { c.cancel(); c.cancel = nil }()
				req = req.WithContext(ctx)
			}
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"association_unavailable",
		"auto_renew_null",
		"auto_renew_true",
		"auto_renew_wrong_type",
		"body_boundary",
		"cancelled_after_lookup",
		"cert_empty",
		"cert_invalid",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"default_renew",
		"domain_scope",
		"domain_wrong_type",
		"domain_zero",
		"fields_missing",
		"insert_unavailable",
		"invalid_body",
		"key_domain",
		"key_empty",
		"key_holder_missing",
		"key_mailbox",
		"key_read",
		"key_write",
		"key_wrong_domain",
		"lookup_unavailable",
		"mailbox_scope",
		"malformed_json",
		"missing",
		"permission_store_unavailable",
		"rate_limited",
		"read_only",
		"reload_missing",
		"reload_unavailable",
		"request_too_large",
		"session_unavailable",
		"unauthenticated",
		"valid",
		"write_only",
		"wrong_domain",
	} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.Domain("example.com")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.Mailbox("owner@example.com")}}, false, false)
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
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.Domain("other.test")}}, false, false)

	out["cancelled_after_lookup"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Cancel", "after-certificate-lookup")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}
	return out
}

func TestCertificateCreationFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		id, status int
	}{{"lookup", 3, 500}, {"cancelled", 1, 500}, {"reload", 5, 500}, {"association", 7, 500}, {"explicit_false", 1, 201}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCertificateCreationScenarios(t)["write_only"]
			if tc.name == "cancelled" {
				s = newCertificateCreationScenarios(t)["cancelled_after_lookup"]
			}
			raw, _ := json.Marshal(map[string]interface{}{"domain_id": tc.id, "cert_pem": "certificate", "key_pem": "key", "auto_renew": false})
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, certificateCreationPath, certificateCreationBody(t, raw))
			if rr.Code != tc.status {
				t.Fatalf("got%d want%d", rr.Code, tc.status)
			}
			if tc.name == "explicit_false" {
				var v struct {
					Data struct {
						AutoRenew bool `json:"auto_renew"`
					}
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
					t.Fatal(err)
				}
				if v.Data.AutoRenew {
					t.Fatal("explicit false overwritten by database default")
				}
			}
		})
	}
}
