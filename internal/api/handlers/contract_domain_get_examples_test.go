package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// getDomain has no request body: the missing parameter example and the hook
// select the 404, while createDomain's link supplies the successful lookup.
func TestDomainGet_DocumentedExamples(t *testing.T) {
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
						Value any `yaml:"value"`
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
	const path = "/api/v1/admin/domains/{id}"
	op := doc.Paths[path].Get
	if op.OperationID != "getDomain" || len(op.Parameters) != 1 {
		t.Fatalf("unexpected getDomain operation or parameters: %+v", op)
	}
	parameter := op.Parameters[0]
	if parameter.Name != "id" || parameter.In != "path" || !parameter.Required || len(parameter.Examples) != 2 {
		t.Fatalf("unexpected ID parameter: %+v", parameter)
	}
	missing := contractPathExampleID(t, parameter.Examples["missing"].Value)
	if missing != 2147483647 || contractPathExampleID(t, parameter.Examples["found"].Value) != 1 {
		t.Fatalf("unexpected path examples: %+v", parameter.Examples)
	}
	examples := op.Responses["404"].Content["application/json"].Examples
	if len(examples) != 1 {
		t.Fatalf("404 examples: %+v", examples)
	}
	want := jsonValue(t, examples["missing"].Value)
	if !reflect.DeepEqual(want, parseShapeJSON(t, `{"error":{"code":"domain_not_found","message":"Domain not found"}}`)) {
		t.Fatalf("404 documents %s", mustJSON(want))
	}

	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("domain lookup contract test needs PostgreSQL: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "contract-domain-get.test", ServerType: "restmail"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	// Snapshot the stored timestamps at PostgreSQL's microsecond precision.
	if err := tx.First(&domain, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Model(&models.Domain{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("missing ID must not exist: count %d, error %v", count, err)
	}
	lookup := func(id uint) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/domains/%d", id), nil)
		ctx := chi.NewRouteContext()
		ctx.URLParams.Add("id", fmt.Sprint(id))
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
		rr := httptest.NewRecorder()
		domainRoutes(NewDomainHandler(tx, nil)).ServeHTTP(rr, r)
		return rr
	}
	rr := lookup(missing)
	if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
		t.Fatalf("missing ID answered %d %s, want 404 %s", rr.Code, rr.Body, mustJSON(want))
	}
	rr = lookup(domain.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("created ID answered %d %s", rr.Code, rr.Body)
	}
	var response struct {
		Data models.Domain `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	// Compare the complete public domain, including its stored timestamps.
	if !reflect.DeepEqual(parseShapeJSON(t, string(mustJSON(response.Data))), parseShapeJSON(t, string(mustJSON(domain)))) {
		t.Fatalf("lookup returned %+v, want %+v", response.Data, domain)
	}
	var stored models.Domain
	if err := tx.First(&stored, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parseShapeJSON(t, string(mustJSON(stored))), parseShapeJSON(t, string(mustJSON(domain)))) {
		t.Fatalf("lookup changed stored domain: %+v", stored)
	}
	if err := tx.Model(&models.Domain{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("lookup created missing ID: count %d, error %v", count, err)
	}
}
