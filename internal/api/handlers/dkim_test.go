package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

const dkimTestMasterKey = "dkim-set-key-test-master-key"

// putDKIMKey calls PUT /api/v1/admin/dkim/{id} for domain with the given
// selector and PEM key.
func putDKIMKey(t *testing.T, tx *gorm.DB, domain models.Domain, selector, keyPEM string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"selector": selector, "private_key": keyPEM})
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/admin/dkim/%d", domain.ID), strings.NewReader(string(body)))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", fmt.Sprint(domain.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	dkimRoutes(NewDKIMHandler(tx, dkimTestMasterKey)).ServeHTTP(rr, req)
	return rr
}

// seedDKIMDomain creates a domain, in a transaction rolled back when the test
// ends, that already signs with selector "old" and a key of its own.
func seedDKIMDomain(t *testing.T, gdb *gorm.DB) (*gorm.DB, models.Domain) {
	t.Helper()
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	key, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("generate a DKIM key: %v", err)
	}
	stored, err := models.EncryptDKIMPrivateKey(key, dkimTestMasterKey)
	if err != nil {
		t.Fatalf("encrypt the DKIM key: %v", err)
	}
	domain := models.Domain{
		Name:           fmt.Sprintf("dkim-set-%d.test", time.Now().UnixNano()),
		DKIMSelector:   "old",
		DKIMPrivateKey: stored,
	}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	return tx, domain
}

// A key the PUT accepts is one the domain's mail is then signed with: the
// signer loads it and the signature verifies against the key's DNS record.
// Ed25519 is accepted on import, though the server generates only RSA keys.
func TestSetDKIMKey_AcceptedKeySignsTheDomainsMail(t *testing.T) {
	gdb := openAuthzTestDB(t)

	rsaKey, rsaPub, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 key: %v", err)
	}
	edDER, err := x509.MarshalPKCS8PrivateKey(edPriv)
	if err != nil {
		t.Fatalf("marshal Ed25519 key: %v", err)
	}
	edPubDER, err := x509.MarshalPKIXPublicKey(edPub)
	if err != nil {
		t.Fatalf("marshal Ed25519 public key: %v", err)
	}

	cases := []struct {
		name, key, pub string
	}{
		{"RSA 2048", rsaKey, rsaPub},
		{"Ed25519", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edDER})),
			string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: edPubDER}))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, domain := seedDKIMDomain(t, gdb)
			rr := putDKIMKey(t, tx, domain, "rm2026", tc.key)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
			}

			raw := "From: alice@" + domain.Name + "\r\nTo: bob@example.net\r\nSubject: hi\r\n" +
				"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n\r\nHello.\r\n"
			signed, err := outbound.SignDKIM(tx, dkimTestMasterKey, domain.Name, raw)
			if err != nil {
				t.Fatalf("sign with the key the PUT stored: %v", err)
			}
			record, err := dkim.RecordValue(tc.pub)
			if err != nil {
				t.Fatalf("DKIM record: %v", err)
			}
			resolver := func(_ context.Context, name string) ([]string, error) {
				if name == "rm2026._domainkey."+domain.Name {
					return []string{record}, nil
				}
				return nil, fmt.Errorf("no TXT record for %s", name)
			}
			results := dkim.Verify(context.Background(), []byte(signed), resolver)
			if len(results) != 1 || results[0].Result != dkim.ResultPass {
				t.Fatalf("verify = %+v, want one pass", results)
			}
		})
	}
}

// A key or selector the domain could not sign with is refused with 422,
// naming the field at fault, and the domain keeps the key it had.
func TestSetDKIMKey_RefusesWhatCannotSign(t *testing.T) {
	gdb := openAuthzTestDB(t)

	good, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	smallPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(small)}))

	cases := []struct {
		name, selector, key string
		field               string
	}{
		{"selector not a DNS label", "rm_2026", good, "selector"},
		{"selector longer than 63 characters", strings.Repeat("s", 64), good, "selector"},
		{"key not PEM", "rm2026", "not a key", "private_key"},
		{"RSA key under 2048 bits", "rm2026", smallPEM, "private_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, domain := seedDKIMDomain(t, gdb)
			rr := putDKIMKey(t, tx, domain, tc.selector, tc.key)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rr.Code, rr.Body.String())
			}
			var resp struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Error.Code != "validation_failed" || resp.Error.Details.Fields[tc.field] == "" ||
				resp.Error.Message != "Request body failed validation" {
				t.Fatalf("error = %+v, want validation_failed naming %s", resp.Error, tc.field)
			}

			var after models.Domain
			if err := tx.First(&after, domain.ID).Error; err != nil {
				t.Fatalf("reload domain: %v", err)
			}
			if after.DKIMSelector != domain.DKIMSelector || after.DKIMPrivateKey != domain.DKIMPrivateKey {
				t.Fatalf("a refused PUT changed the domain's key: selector %q", after.DKIMSelector)
			}
		})
	}
}

func TestListDKIMKeys_ReportsKnownAndUnknownAgeWithoutPrivateMaterial(t *testing.T) {
	gdb := openAuthzTestDB(t)
	tx, domain := seedDKIMDomain(t, gdb)
	created := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	if err := tx.Model(&domain).Update("dkim_created_at", created).Error; err != nil {
		t.Fatal(err)
	}
	legacy := models.Domain{Name: "legacy-age.test", DKIMSelector: "legacy", DKIMPrivateKey: "private-sentinel"}
	if err := tx.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Domain{Name: "no-key-age.test"}).Error; err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	before := time.Now()
	dkimRoutes(NewDKIMHandler(tx, dkimTestMasterKey)).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/dkim", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("got %d configured keys, want 2", len(resp.Data))
	}
	for _, entry := range resp.Data {
		// An allowlist catches private_key, dkim_private_key, and future fields
		// accidentally added by serializing the persistence model.
		if len(entry) != 6 {
			t.Fatalf("unexpected response fields: %v", entry)
		}
		for _, field := range []string{"domain_id", "domain", "selector", "has_key", "created_at", "age_seconds"} {
			if _, ok := entry[field]; !ok {
				t.Fatalf("missing %s", field)
			}
		}
		for _, field := range []string{"private_key", "dkim_private_key"} {
			if _, ok := entry[field]; ok {
				t.Fatalf("response exposes %s", field)
			}
		}
		var got dkimEntry
		body, _ := json.Marshal(entry)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if !got.HasKey {
			t.Fatal("configured key reported absent")
		}
		switch got.DomainID {
		case domain.ID:
			if got.Selector != "old" || got.CreatedAt == nil || !got.CreatedAt.Equal(created) ||
				got.AgeSeconds == nil || *got.AgeSeconds < before.Sub(created).Seconds() || *got.AgeSeconds > time.Since(created).Seconds() {
				t.Fatalf("incorrect known key metadata: %+v", got)
			}
		case legacy.ID:
			if got.Selector != "legacy" || got.CreatedAt != nil || got.AgeSeconds != nil {
				t.Fatalf("legacy age should be unknown: %+v", got)
			}
		default:
			t.Fatalf("unexpected domain %d", got.DomainID)
		}
	}
	for _, secret := range []string{domain.DKIMPrivateKey, "private-sentinel", models.DKIMKeyCipherPrefix, "PRIVATE KEY"} {
		if strings.Contains(rr.Body.String(), secret) {
			t.Fatal("response contains private material")
		}
	}
}

func TestSetDKIMKey_TracksInstallationAndDeleteClearsAge(t *testing.T) {
	gdb := openAuthzTestDB(t)
	tx, domain := seedDKIMDomain(t, gdb)
	if err := tx.Model(&domain).Update("dkim_rotation_interval_days", 90).Error; err != nil {
		t.Fatal(err)
	}
	key, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	rr := putDKIMKey(t, tx, domain, "new", key)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Data dkimEntry `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	got := response.Data
	if got.Selector != "new" || got.CreatedAt == nil || got.CreatedAt.Before(before) || got.CreatedAt.After(time.Now()) ||
		got.AgeSeconds == nil || *got.AgeSeconds < 0 || *got.AgeSeconds > time.Since(*got.CreatedAt).Seconds() {
		t.Fatalf("incorrect installed key metadata: %+v", got)
	}
	if strings.Contains(rr.Body.String(), "private_key") || strings.Contains(rr.Body.String(), key) {
		t.Fatal("PUT response exposes private key")
	}
	// Re-importing the same key (even under another selector) and unrelated
	// domain updates must not reset a known key age.
	created := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	if err := tx.Model(&domain).Updates(map[string]any{"dkim_created_at": created, "default_quota_bytes": 42}).Error; err != nil {
		t.Fatal(err)
	}
	if rr := putDKIMKey(t, tx, domain, "renamed", key); rr.Code != http.StatusOK {
		t.Fatalf("re-import status %d", rr.Code)
	}
	var stored models.Domain
	if err := tx.First(&stored, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.DKIMCreatedAt == nil || !stored.DKIMCreatedAt.Equal(created) || !models.DKIMKeyIsEncrypted(stored.DKIMPrivateKey) {
		t.Fatal("re-import lost key age or encryption")
	}
	if stored.DKIMRotationIntervalDays != 90 {
		t.Fatal("re-import changed rotation policy")
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/dkim/"+fmt.Sprint(domain.ID), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", fmt.Sprint(domain.ID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr = httptest.NewRecorder()
	dkimRoutes(NewDKIMHandler(tx, dkimTestMasterKey)).ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status %d", rr.Code)
	}
	stored = models.Domain{}
	if err := tx.First(&stored, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.DKIMCreatedAt != nil || stored.DKIMSelector != "" || stored.DKIMPrivateKey != "" {
		t.Fatal("delete retained key metadata")
	}
	if stored.DKIMRotationIntervalDays != 90 {
		t.Fatal("delete changed rotation policy")
	}
}
