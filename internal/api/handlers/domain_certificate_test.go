package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// A mail domain is only ever served over TLS, so it must not be live until it has
// a certificate of its own (issue #289). Creating it puts it in place; a separate,
// deliberate step turns it on, and that step is refused while there is no
// certificate to serve it with.
func TestDomainLifecycle_NotLiveWithoutACertificate(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.Pipeline{}, &models.Certificate{}); err != nil {
		t.Skipf("domain certificate test skipped: migrate failed (%v)", err)
	}

	name := fmt.Sprintf("lifecycle-%d.test", time.Now().UnixNano())

	newTx := func(t *testing.T) *gorm.DB {
		t.Helper()
		tx := gdb.Begin()
		t.Cleanup(func() { tx.Rollback() })
		return tx
	}

	create := func(t *testing.T, tx *gorm.DB, domainName string) models.Domain {
		t.Helper()
		h := NewDomainHandler(tx, nil)
		body, _ := json.Marshal(map[string]string{"name": domainName})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create status = %d, want 201 (body %s)", rr.Code, rr.Body.String())
		}
		var resp struct {
			Data models.Domain `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.Data
	}

	update := func(t *testing.T, tx *gorm.DB, id uint, active bool) *httptest.ResponseRecorder {
		t.Helper()
		h := NewDomainHandler(tx, nil)
		body, _ := json.Marshal(map[string]bool{"active": active})
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/domains/"+fmt.Sprint(id), bytes.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprint(id))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		return rr
	}

	storeCert := func(t *testing.T, tx *gorm.DB, domainID uint, notBefore, notAfter time.Time) {
		t.Helper()
		if err := tx.Create(&models.Certificate{
			DomainID:  domainID,
			CertPEM:   "-- test certificate --",
			KeyPEM:    "-- test key --",
			Issuer:    "self-signed",
			NotBefore: notBefore,
			NotAfter:  notAfter,
		}).Error; err != nil {
			t.Fatalf("store certificate: %v", err)
		}
	}

	t.Run("a new domain is not live", func(t *testing.T) {
		tx := newTx(t)
		domain := create(t, tx, name)
		if domain.Active {
			t.Error("a new domain is live before it has a certificate of its own")
		}
		var stored models.Domain
		if err := tx.First(&stored, domain.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.Active {
			t.Error("the stored domain is live before it has a certificate of its own")
		}
	})

	t.Run("it cannot be made live without a certificate", func(t *testing.T) {
		tx := newTx(t)
		domain := create(t, tx, name)

		rr := update(t, tx, domain.ID, true)
		if rr.Code == http.StatusOK {
			t.Fatalf("a domain with no certificate was made live (body %s)", rr.Body.String())
		}
		if rr.Code < 400 || rr.Code >= 500 {
			t.Errorf("status = %d, want a client error explaining the missing certificate", rr.Code)
		}

		var stored models.Domain
		if err := tx.First(&stored, domain.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.Active {
			t.Error("the domain was left live after the request was refused")
		}
	})

	t.Run("an expired certificate does not make it live either", func(t *testing.T) {
		tx := newTx(t)
		domain := create(t, tx, name)
		storeCert(t, tx, domain.ID, time.Now().Add(-72*time.Hour), time.Now().Add(-time.Hour))

		rr := update(t, tx, domain.ID, true)
		if rr.Code == http.StatusOK {
			t.Fatalf("a domain whose only certificate expired was made live (body %s)", rr.Body.String())
		}
	})

	t.Run("with a current certificate it goes live", func(t *testing.T) {
		tx := newTx(t)
		domain := create(t, tx, name)
		storeCert(t, tx, domain.ID, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))

		rr := update(t, tx, domain.ID, true)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
		}
		var stored models.Domain
		if err := tx.First(&stored, domain.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if !stored.Active {
			t.Error("the domain did not go live even though it has a current certificate")
		}
	})

	t.Run("taking a domain out of service is always allowed", func(t *testing.T) {
		tx := newTx(t)
		domain := create(t, tx, name)
		storeCert(t, tx, domain.ID, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
		if rr := update(t, tx, domain.ID, true); rr.Code != http.StatusOK {
			t.Fatalf("could not make it live: %s", rr.Body.String())
		}

		rr := update(t, tx, domain.ID, false)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
		}
		var stored models.Domain
		if err := tx.First(&stored, domain.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.Active {
			t.Error("the domain is still live after being taken out of service")
		}
	})
}
