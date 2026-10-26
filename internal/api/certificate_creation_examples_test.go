package api

import (
	"bytes"
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
	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmcrypto "github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
func (c *certificateCreationConnection) Driver() driver.Driver                        { return c }
func (c *certificateCreationConnection) Open(string) (driver.Conn, error)             { return c, nil }
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
		t.Cleanup(func() { pool.Close() })
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
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name := range pairs["createCertificate"] {
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

func certificateCreationRequestBody(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	body = certificateCreationBody(t, body)
	if name == "malformed_json" {
		return []byte("{")
	}
	if name == "request_too_large" {
		return bytes.Repeat([]byte("x"), certificateCreationBodyLimit+1)
	}
	if name == "body_boundary" {
		body = append(body, bytes.Repeat([]byte(" "), certificateCreationBodyLimit-len(body))...)
	}
	return body
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
func TestCertificateCreationExamples(t *testing.T) {
	f := newCertificateCreationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["createCertificate"]) != 40 {
		t.Fatal("certificate pair count changed")
	}
	for name, pair := range pairs["createCertificate"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatal("missing certificate scenario")
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, certificateCreationPath, certificateCreationRequestBody(t, name, pair.Request.Body))
			if bytes.Contains(rr.Body.Bytes(), []byte(signingSetterKey(t))) || bytes.Contains(rr.Body.Bytes(), []byte(certificateCreationMaster)) {
				t.Fatal("private material exposed")
			}
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d want%d", rr.Code, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			if s.store == nil {
				return
			}
			calls := s.store.snapshot()[before:]
			inserts := 0
			for _, q := range calls {
				if strings.HasPrefix(q.query, `INSERT INTO "certificates"`) {
					inserts++
					v, err := exampleInsertFields(q.query, q.args)
					if err != nil {
						t.Fatal(err)
					}
					plain, err := rmcrypto.DecryptString(v["key_pem"].(string), certificateCreationMaster)
					if err != nil || plain != signingSetterKey(t) {
						t.Fatal("certificate key did not round-trip encrypted")
					}
					var req struct {
						DomainID  int64 `json:"domain_id"`
						AutoRenew *bool `json:"auto_renew"`
					}
					if err := json.Unmarshal(pair.Request.Body, &req); err != nil {
						t.Fatal(err)
					}
					want := req.AutoRenew == nil || *req.AutoRenew
					if v["domain_id"] != req.DomainID || v["auto_renew"] != want {
						t.Fatal("certificate domain or renewal policy changed")
					}
				}
			}
			begins, commits, rollbacks := 0, 0, 0
			for _, q := range calls {
				switch q.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "ROLLBACK":
					rollbacks++
				}
			}
			if inserts > 0 {
				if begins != 1 || commits+rollbacks != 1 || (commits == 1) != (rr.Code == 201) {
					t.Fatal("certificate upload did not commit or roll back exactly once")
				}
			} else if begins+commits+rollbacks != 0 {
				t.Fatal("early refusal began an upload transaction")
			}
			want := rr.Code == 201 || name == "insert_unavailable" || name == "reload_unavailable" || name == "reload_missing" || name == "association_unavailable"
			if (inserts == 1) != want || inserts > 1 {
				t.Fatal("unexpected certificate mutation count")
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "createCertificate" {
				t.Errorf("certificate warning:%v", w)
			}
		}
	}
}
func TestCertificateCreationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newCertificateCreationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["createCertificate"] {
		t.Run(name, func(t *testing.T) {
			checkCertificateCreationPairWithVertrag(t, binary, f[name], http.MethodPost, certificateCreationPath, name, pair, certificateCreationBodyLimit+1)
		})
	}
}
func checkCertificateCreationPairWithVertrag(t *testing.T, binary string, s bulkQueueExampleScenario, method, path, name string, pair oapifly.ExamplePair, oversizedBytes ...int) {
	t.Helper()
	doc := contractDocument(t, describedRouters(t).generator("bulk queue boundary"))
	verb := strings.ToLower(method)
	op := doc["paths"].(map[string]interface{})[path].(map[string]interface{})[verb].(map[string]interface{})
	doc["paths"] = map[string]interface{}{path: map[string]interface{}{verb: op}}
	if body, ok := op["requestBody"].(map[string]interface{}); ok {
		media := body["content"].(map[string]interface{})["application/json"].(map[string]interface{})
		examples := media["examples"].(map[string]interface{})
		selected, ok := examples[name]
		if !ok {
			t.Fatalf("missing published request %s", name)
		}
		media["examples"] = map[string]interface{}{name: selected}
	}
	uri := messageCollectionReadURI(path, pair)
	if parameters, ok := op["parameters"].([]interface{}); ok {
		for _, value := range parameters {
			parameter := value.(map[string]interface{})
			delete(parameter, "examples")
			var illustration interface{} = "compiler-value"
			if schema, ok := parameter["schema"].(map[string]interface{}); ok && (schema["type"] == "integer" || schema["type"] == "number") {
				illustration = 1
			}
			parameter["example"] = illustration
		}
	}
	status := strconv.Itoa(pair.Response.Status)
	response := op["responses"].(map[string]interface{})[status].(map[string]interface{})
	op["responses"] = map[string]interface{}{status: response}
	content, _ := response["content"].(map[string]interface{})
	for _, v := range content {
		m := v.(map[string]interface{})
		e := m["examples"].(map[string]interface{})
		chosen, ok := e[name]
		if !ok {
			t.Fatal("missing published response")
		}
		m["examples"] = map[string]interface{}{name: chosen}
	}
	dir := t.TempDir()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "openapi.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	{
		hook := "import vertrag_hooks as hooks\n@hooks.before_each\ndef published_request(transaction):\n    transaction['fullPath'] = " + strconv.Quote(uri) + "\n"
		if name == "request_too_large" {
			payload := "oversized"
			if len(oversizedBytes) != 0 {
				payload = strings.Repeat("x", oversizedBytes[0])
			}
			hook += "    transaction['request']['body'] = " + strconv.Quote(payload) + "\n"
		} else if name == "malformed_json" {
			hook += "    transaction['request']['body'] = '{'\n"
		} else if name == "body_boundary" {
			payload := string(pair.Request.Body)
			if len(payload) > 512 {
				t.Fatal("boundary example exceeds the configured cap")
			}
			hook += "    transaction['request']['body'] = " + strconv.Quote(payload+strings.Repeat(" ", 512-len(payload))) + "\n"
		}
		cert, key := certificateCreationMaterial(t)
		if err := os.WriteFile(filepath.Join(dir, "certificate.pem"), []byte(cert), 0600); err != nil {
			t.Fatal(err)
		}
		keyPath, err := filepath.Abs("../../tests/contract/fixtures/dkim-key.pem")
		if err != nil {
			t.Fatal(err)
		}
		hook += "    import json\n    from pathlib import Path\n    try:\n        body = json.loads(transaction['request']['body'])\n    except (ValueError, TypeError):\n        body = None\n    if isinstance(body, dict):\n        if body.get('cert_pem') == 'certificate':\n            body['cert_pem'] = Path('certificate.pem').read_text()\n        if body.get('key_pem') == 'key':\n            body['key_pem'] = Path(" + strconv.Quote(keyPath) + ").read_text()\n        transaction['request']['body'] = json.dumps(body)\n"
		_ = key

		if name == "body_boundary" {
			hook += "    payload = transaction['request']['body']\n    transaction['request']['body'] = payload + ' ' * (8192 - len(payload.encode('utf-8')))\n"
		}
		if err = os.WriteFile(filepath.Join(dir, "pair.py"), []byte(hook), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./pair.py\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(s.handler)
	defer server.Close()
	args := []string{"run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json"}
	if s.token != "" {
		args = append(args, "--header", "Authorization: Bearer "+s.token)
	}
	args = append(args, "openapi.json")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Vertrag rejected certificate exchange: %v (request material withheld)", err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Summary struct{ Total, Passing, Failing, Errors, Skipped int }
	}
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.Total != 1 || report.Summary.Passing != 1 || report.Summary.Failing != 0 || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
		t.Fatalf("exchange did not run and pass: %s", data)
	}
}
