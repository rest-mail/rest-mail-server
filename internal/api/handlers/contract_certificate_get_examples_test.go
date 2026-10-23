package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// getCertificate has no request body: the missing parameter example and the hook
// select the 404, while createCertificate's link supplies the successful lookup.
func TestCertificateGet_DocumentedExamples(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Get struct {
				OperationID string `yaml:"operationId"`
				Parameters  []struct {
					Name     string `yaml:"name"`
					In       string `yaml:"in"`
					Required bool   `yaml:"required"`
					Examples map[string]struct {
						Value uint `yaml:"value"`
					} `yaml:"examples"`
				} `yaml:"parameters"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"get"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/admin/certificates/{id}"
	op := doc.Paths[path].Get
	if op.OperationID != "getCertificate" || len(op.Parameters) != 1 {
		t.Fatalf("unexpected getCertificate operation or parameters: %+v", op)
	}
	parameter := op.Parameters[0]
	if parameter.Name != "id" || parameter.In != "path" || !parameter.Required || len(parameter.Examples) != 2 {
		t.Fatalf("unexpected ID parameter: %+v", parameter)
	}
	missing := parameter.Examples["missing"].Value
	if missing != 2147483647 || parameter.Examples["found"].Value != 1 {
		t.Fatalf("unexpected path examples: %+v", parameter.Examples)
	}
	examples := op.Responses["404"].Content["application/json"].Examples
	if len(examples) != 1 {
		t.Fatalf("404 examples: %+v", examples)
	}
	want := jsonValue(t, examples["missing"].Value)
	if !reflect.DeepEqual(want, parseShapeJSON(t, `{"error":{"code":"not_found","message":"Certificate not found"}}`)) {
		t.Fatalf("404 documents %s", mustJSON(want))
	}

	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("certificate lookup contract test needs PostgreSQL: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Certificate{}); err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "contract-certificate-get.test", ServerType: "restmail"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	certificate := models.Certificate{DomainID: domain.ID, CertPEM: "public-certificate", KeyPEM: "private-key-must-stay-hidden", Issuer: "contract", NotBefore: now, NotAfter: now.Add(24 * time.Hour), AutoRenew: true}
	if err := tx.Create(&certificate).Error; err != nil {
		t.Fatal(err)
	}
	// Reload PostgreSQL's stored timestamp precision and the association.
	if err := tx.Preload("Domain").First(&certificate, certificate.ID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Model(&models.Certificate{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("missing ID must not exist: count %d, error %v", count, err)
	}
	lookup := func(id uint) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/certificates/%d", id), nil)
		ctx := chi.NewRouteContext()
		ctx.URLParams.Add("id", fmt.Sprint(id))
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
		rr := httptest.NewRecorder()
		certificateRoutes(NewCertificateHandler(tx, "")).ServeHTTP(rr, r)
		return rr
	}
	rr := lookup(missing)
	if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
		t.Fatalf("missing ID answered %d %s, want 404 %s", rr.Code, rr.Body, mustJSON(want))
	}
	rr = lookup(certificate.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("created ID answered %d %s", rr.Code, rr.Body)
	}
	var response struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	wantData := map[string]interface{}{
		"id": float64(certificate.ID), "domain_id": float64(domain.ID),
		"cert_pem": certificate.CertPEM, "issuer": certificate.Issuer,
		"not_before": certificate.NotBefore.Format(time.RFC3339),
		"not_after":  certificate.NotAfter.Format(time.RFC3339),
		"created_at": certificate.CreatedAt.Format(time.RFC3339),
		"updated_at": certificate.UpdatedAt.Format(time.RFC3339),
		"auto_renew": true, "domain": parseShapeJSON(t, string(mustJSON(certificate.Domain))),
	}
	// Exact equality also proves key_pem and every other private field stay hidden.
	if !reflect.DeepEqual(response.Data, wantData) {
		t.Fatalf("lookup returned %s, want %s", mustJSON(response.Data), mustJSON(wantData))
	}
	var stored models.Certificate
	if err := tx.Preload("Domain").First(&stored, certificate.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, certificate) {
		t.Fatalf("lookup changed stored certificate")
	}
	if err := tx.Model(&models.Certificate{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("lookup created missing ID: count %d, error %v", count, err)
	}
}
