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
	NewDKIMHandler(tx, dkimTestMasterKey).SetKey(rr, req)
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
				!strings.HasPrefix(resp.Error.Message, tc.field+" ") {
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
