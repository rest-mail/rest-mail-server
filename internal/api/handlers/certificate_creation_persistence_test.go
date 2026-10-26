package handlers

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	rmcrypto "github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// The scratch database is verified before migrations or writes. These cases
// check the persisted default and rollback, not just controlled SQL bindings.
func TestCertificateCreationPersistence(t *testing.T) {
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Certificate{}); err != nil {
		t.Fatal(err)
	}
	const master = "public-test-certificate-persistence-master"
	key, err := os.ReadFile("../../../tests/contract/fixtures/dkim-key.pem")
	if err != nil {
		t.Fatal("read public fixture")
	}
	block, _ := pem.Decode(key)
	if block == nil {
		t.Fatal("fixture PEM missing")
	}
	value, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal("parse public key fixture")
	}
	signer, ok := value.(crypto.Signer)
	if !ok {
		t.Fatal("fixture cannot sign")
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "contract.example.org"}, NotBefore: at, NotAfter: at.AddDate(0, 0, 30)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	if err != nil {
		t.Fatal("create public fixture")
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	d := models.Domain{Name: "contract.example.org", ServerType: "traditional"}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"disabled", "enabled", "default", "null", "cancelled", "insert_failure", "reload_failure", "association_failure"} {
		t.Run(name, func(t *testing.T) {
			body := map[string]interface{}{"domain_id": d.ID, "cert_pem": cert, "key_pem": string(key)}
			if name == "disabled" {
				body["auto_renew"] = false
			}
			if name == "enabled" {
				body["auto_renew"] = true
			}
			if name == "null" {
				body["auto_renew"] = nil
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal("encode public fixture")
			}
			var before int64
			if err := db.Model(&models.Certificate{}).Count(&before).Error; err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/certificates", bytes.NewReader(raw))
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			req = req.WithContext(ctx)
			domainReads := 0
			if err := db.Callback().Query().After("gorm:after_query").Register("certificate-creation-query", func(tx *gorm.DB) {
				if tx.Statement.Table == "domains" {
					domainReads++
					if name == "cancelled" {
						cancel()
					}
					if name == "association_failure" && domainReads > 1 {
						tx.AddError(errors.New("private association failure"))
					}
				}
				if name == "reload_failure" && tx.Statement.Table == "certificates" {
					tx.AddError(errors.New("private reload failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Create().Before("gorm:create").Register("certificate-creation-insert", func(tx *gorm.DB) {
				if name == "insert_failure" && tx.Statement.Table == "certificates" {
					tx.AddError(errors.New("private insert failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			certificateRoutes(NewCertificateHandler(db, master)).ServeHTTP(rr, req)
			if err := db.Callback().Query().Remove("certificate-creation-query"); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Create().Remove("certificate-creation-insert"); err != nil {
				t.Fatal(err)
			}
			success := name == "disabled" || name == "enabled" || name == "default" || name == "null"
			want := 500
			if success {
				want = 201
			}
			if rr.Code != want {
				t.Fatalf("status%d want%d", rr.Code, want)
			}
			if strings.Contains(rr.Body.String(), string(key)) || strings.Contains(rr.Body.String(), master) || strings.Contains(rr.Body.String(), "private ") {
				t.Fatal("private certificate material exposed")
			}
			var after int64
			if err := db.Model(&models.Certificate{}).Count(&after).Error; err != nil {
				t.Fatal(err)
			}
			expected := before
			if success {
				expected++
			}
			if after != expected {
				t.Fatal("failed upload persisted or successful upload missing")
			}
			if !success {
				return
			}
			var response struct {
				Data Certificate `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var stored models.Certificate
			if err := db.First(&stored, response.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			plain, err := rmcrypto.DecryptString(stored.KeyPEM, master)
			if err != nil || plain != string(key) || stored.KeyPEM == string(key) {
				t.Fatal("encrypted certificate key did not round-trip")
			}
			auto := name != "disabled"
			if stored.AutoRenew != auto || response.Data.AutoRenew != auto || stored.DomainID != d.ID || response.Data.Domain.ID != d.ID || !stored.NotBefore.Equal(at) || !stored.NotAfter.Equal(template.NotAfter) {
				t.Fatal("certificate policy, domain or dates changed")
			}
		})
	}
}
