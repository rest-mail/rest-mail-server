package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func certificateReadFixture(t *testing.T) (*gorm.DB, models.Certificate) {
	t.Helper()
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Certificate{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "certificate-read.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cert := models.Certificate{DomainID: domain.ID, CertPEM: "public-certificate-fixture", KeyPEM: "private-key-must-stay-hidden",
		Issuer: "fixture", NotBefore: stamp, NotAfter: stamp.Add(24 * time.Hour)}
	if err := db.Create(&cert).Error; err != nil {
		t.Fatal(err)
	}
	return db, cert
}

func serveCertificateRequest(db *gorm.DB, method, path string, ctx context.Context) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, nil).WithContext(ctx)
	certificateRoutes(NewCertificateHandler(db, "")).ServeHTTP(rr, r)
	return rr
}

func requireCertificateInternalError(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), `"internal_error"`) || strings.Contains(rr.Body.String(), "private") {
		t.Fatalf("want sanitized storage failure, got %d %s", rr.Code, rr.Body)
	}
}

func TestCertificateListCountFailureIsNotSuccessfulPagination(t *testing.T) {
	db, cert := certificateReadFixture(t)
	const callback = "certificate_count_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		// Fail only the count, leaving the page query able to succeed. This
		// catches a handler that discards the error and answers total=0.
		if _, counting := tx.Statement.Dest.(*int64); counting && tx.Statement.Table == "certificates" {
			_ = tx.AddError(errors.New("private certificate count detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	rr := serveCertificateRequest(db, http.MethodGet, "/api/v1/admin/certificates", t.Context())
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	requireCertificateInternalError(t, rr)

	other := cert
	other.ID = 0
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	for offset := range 2 {
		rr = serveCertificateRequest(db, http.MethodGet, fmt.Sprintf("/api/v1/admin/certificates?limit=1&offset=%d", offset), t.Context())
		var answer struct {
			Data       []Certificate
			Pagination struct {
				Total   int64 `json:"total"`
				HasMore bool  `json:"has_more"`
			}
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
			t.Fatal(err)
		}
		if rr.Code != http.StatusOK || len(answer.Data) != 1 || answer.Pagination.Total != 2 || answer.Pagination.HasMore != (offset == 0) {
			t.Fatalf("successful pagination changed: %d %s", rr.Code, rr.Body)
		}
		if answer.Data[0].Domain.Name != "certificate-read.test" || strings.Contains(rr.Body.String(), cert.KeyPEM) || strings.Contains(rr.Body.String(), cert.CertPEM) {
			t.Fatalf("list lost domain metadata or exposed PEM material: %s", rr.Body)
		}
	}
}

func TestCertificateReadsAndDeleteHonorCancelledRequests(t *testing.T) {
	for _, operation := range []string{"list", "details", "delete"} {
		t.Run(operation, func(t *testing.T) {
			db, cert := certificateReadFixture(t)
			method, path := http.MethodGet, "/api/v1/admin/certificates"
			if operation != "list" {
				path += fmt.Sprintf("/%d", cert.ID)
			}
			if operation == "delete" {
				method = http.MethodDelete
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			rr := serveCertificateRequest(db, method, path, ctx)
			requireCertificateInternalError(t, rr)
			var stored models.Certificate
			if err := db.First(&stored, cert.ID).Error; err != nil || stored.KeyPEM != cert.KeyPEM {
				t.Fatalf("cancelled request changed certificate: %v", err)
			}
		})
	}
}

func TestCertificateDeleteCancellationAfterLookupPreservesRow(t *testing.T) {
	db, cert := certificateReadFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const callback = "certificate_cancel_after_lookup"
	if err := db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "certificates" {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	path := fmt.Sprintf("/api/v1/admin/certificates/%d", cert.ID)
	rr := serveCertificateRequest(db, http.MethodDelete, path, ctx)
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	requireCertificateInternalError(t, rr)
	var stored models.Certificate
	if err := db.First(&stored, cert.ID).Error; err != nil || stored.KeyPEM != cert.KeyPEM {
		t.Fatalf("cancelled deletion removed or changed certificate: %v", err)
	}
	rr = serveCertificateRequest(db, http.MethodDelete, path, t.Context())
	if rr.Code != http.StatusNoContent {
		t.Fatalf("retry deletion: %d %s", rr.Code, rr.Body)
	}
	if err := db.First(&stored, cert.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("successful retry did not delete certificate: %v", err)
	}
	rr = serveCertificateRequest(db, http.MethodGet, path, t.Context())
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing certificate changed response: %d %s", rr.Code, rr.Body)
	}
}
