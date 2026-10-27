package api

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/restmail/restmail/internal/examplepairs"
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
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var adminResourceReadPaths = map[string]string{
	"getCertificate":  "/api/v1/admin/certificates/{id}",
	"getCustomFilter": "/api/v1/admin/custom-filters/{id}",
	"getDKIMRotation": "/api/v1/admin/dkim/{id}/rotation",
}

// The public RFC8032 test vector supplies deterministic signing material. Only
// its public record may leave the production rotation response mapper.
func adminReadRotationFixtureKey() string {
	seed, _ := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	der, err := x509.MarshalPKCS8PrivateKey(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// Real GORM queries map controlled SQL rows; this proves repository and scope
// wiring without claiming PostgreSQL semantics or touching a signing service.
type adminResourceReadConnection struct{ domainAliasReadConnection }

func (c adminResourceReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c adminResourceReadConnection) Driver() driver.Driver                        { return c }
func (c adminResourceReadConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c adminResourceReadConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) == 0 {
		return nil, errors.New("admin read lost its resource restriction")
	}
	id := fmt.Sprint(args[0].Value)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	switch {
	case strings.Contains(query, `FROM "certificates"`):
		if !strings.Contains(query, `"certificates"."id" =`) {
			return nil, errors.New("certificate lookup lost ID")
		}
		if id == "3" {
			return nil, errors.New("sensitive certificate lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "cert_pem", "key_pem", "issuer", "not_before", "not_after", "auto_renew", "created_at", "updated_at"}}
		if id != "2147483647" {
			domainID := int64(1)
			if id == "4" {
				domainID = 4
			}
			r.rows = [][]driver.Value{{n, domainID, "-----BEGIN CERTIFICATE-----\npublic-fixture\n-----END CERTIFICATE-----\n", "private-certificate-fixture", "Fixture CA", stamp, stamp.AddDate(1, 0, 0), id != "2", stamp, stamp}}
		}
		return r, nil
	case strings.Contains(query, `FROM "custom_filters"`):
		if !strings.Contains(query, `"custom_filters"."id" =`) {
			return nil, errors.New("filter lookup lost ID")
		}
		if id == "3" {
			return nil, errors.New("sensitive filter lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "name", "description", "filter_type", "direction", "config", "enabled", "created_at", "updated_at"}}
		if id != "2147483647" {
			r.rows = [][]driver.Value{{n, int64(1), "Subject café", "Add a public subject marker", "transform", "both", []byte(`{"prefix":"[REST Mail] "}`), id != "2", stamp, stamp}}
		}
		return r, nil
	case strings.Contains(query, `FROM "domains"`) && strings.Contains(query, `"dkim_rotation_interval_days"`):
		if strings.Contains(query, "dkim_private_key") {
			return nil, errors.New("rotation fetched the current private key")
		}
		if id == "3" {
			return nil, errors.New("sensitive rotation domain detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "name", "dkim_rotation_interval_days"}}
		if id != "2147483647" {
			interval := int64(90)
			if id == "2" {
				interval = 0
			}
			r.rows = [][]driver.Value{{n, "example.com", interval}}
		}
		return r, nil
	case strings.Contains(query, `FROM "dkim_rotation_keys"`):
		if !strings.Contains(query, "domain_id =") {
			return nil, errors.New("rotation keys lost domain restriction")
		}
		if id == "4" {
			return nil, errors.New("sensitive rotation key query detail")
		}
		r := &authReadSnapshotRows{columns: []string{"domain_id", "slot", "selector", "private_key", "published_at", "ttl_seconds", "retire_after"}}
		if id == "5" || id == "6" || id == "7" || id == "9" {
			slot, selector, key := "staged", "restmail-next", adminReadRotationFixtureKey()
			var published, retire driver.Value
			if id == "6" || id == "7" {
				published = stamp
			}
			if id == "7" {
				slot, selector, retire = "previous", "restmail-previous", stamp.Add(24*time.Hour)
			}
			if id == "9" {
				key = "unreadable-private-fixture"
			}
			r.rows = [][]driver.Value{{n, slot, selector, key, published, int64(3600), retire}}
		}
		return r, nil
	}
	return c.domainAliasReadConnection.QueryContext(ctx, query, args)
}

func adminResourceReadRouters(t *testing.T) (map[string]http.Handler, *auth.JWTService) {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(adminResourceReadConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	makeRouter := func() http.Handler {
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	global := makeRouter()
	previous := heldGrants
	defer func() { heldGrants = previous }()
	scoped := func(scope string) http.Handler {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				return grants.HeldOf(grants.Entry{Capability: middleware.CapDomainsRead, Scope: scope}, grants.Entry{Capability: middleware.CapPipelinesRead, Scope: scope}), nil
			}
		}
		return makeRouter()
	}
	domain := scoped(grants.Domain("example.com"))
	outside := scoped(grants.Domain("other.test"))
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(uint) (grants.Held, error) { return grants.Held{}, errors.New("private grant-store detail") }
	}
	grantFailure := makeRouter()
	return map[string]http.Handler{"rate_limited": apiKeyRefusalLimited(t),
		"found": global, "disabled": global, "missing": global, "internal_error": global, "domain_unavailable": global,
		"keys_unavailable": global, "awaiting_publication": global, "waiting_ttl": global, "retiring": global, "unreadable_key": global,
		"unauthenticated": unavailable, "session_unavailable": unavailable, "request_too_large": global, "invalid_id": global, "invalid_format": global,
		"domain_scope": domain, "outside_domain": outside, "permission_store_unavailable": grantFailure, "target_lookup_unavailable": domain,
	}, jwt
}

func TestAdminResourceReadDatabaseFailuresAreNotMissing(t *testing.T) {
	routers, jwt := adminResourceReadRouters(t)
	for _, tc := range []struct{ path, message string }{
		{"/api/v1/admin/certificates/3", "Failed to get certificate"},
		{"/api/v1/admin/certificates/4", "Failed to get certificate"},
		{"/api/v1/admin/custom-filters/3", "Failed to get custom filter"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
		rr := httptest.NewRecorder()
		routers["found"].ServeHTTP(rr, req)
		if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\""+tc.message+"\"}}\n" {
			t.Errorf("%s: got %d %s, want sanitized500", tc.path, rr.Code, rr.Body.String())
		}
	}
}

func TestAdminResourceReadExamples(t *testing.T) {
	routers, jwt := adminResourceReadRouters(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range adminResourceReadPaths {
		if len(pairs[operation]) < 8 {
			t.Fatalf("incomplete pairs for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				handler, ok := routers[name]
				if !ok {
					t.Fatalf("no scenario for %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, domainAliasReadURI(path, pair), nil)
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := adminResourceReadPaths[warning.OperationID]; ok {
				t.Errorf("resource read warning: %v", warning)
			}
		}
	}
}

func TestAdminResourceReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for admin resource-read exchange proof")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers, jwt := adminResourceReadRouters(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range adminResourceReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				token := mailboxToken(t, jwt)
				if name == "unauthenticated" {
					token = ""
				}
				checkMailboxLookupWithVertrag(t, binary, routers[name], token, path, name, pair)
			})
		}
	}
}
