package handlers

import (
	"context"
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

// getAlias has no request body: the missing parameter example and the hook
// select the 404, while createAlias's link supplies the successful lookup.
func TestAliasGet_DocumentedExamples(t *testing.T) {
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
	const path = "/api/v1/admin/aliases/{id}"
	op := doc.Paths[path].Get
	if op.OperationID != "getAlias" || len(op.Parameters) != 1 {
		t.Fatalf("unexpected getAlias operation or parameters: %+v", op)
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
	if !reflect.DeepEqual(want, parseShapeJSON(t, `{"error":{"code":"not_found","message":"Alias not found"}}`)) {
		t.Fatalf("404 documents %s", mustJSON(want))
	}

	gdb, err := connectTestDB()
	if err != nil {
		t.Skipf("alias lookup contract test needs PostgreSQL: %v", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Alias{}); err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "contract-alias-get.test", ServerType: "restmail"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	alias := models.Alias{DomainID: domain.ID, SourceAddress: "sales@" + domain.Name, DestinationAddress: "admin@restmail.test", Active: true}
	if err := tx.Create(&alias).Error; err != nil {
		t.Fatal(err)
	}
	// Reload PostgreSQL's stored timestamp precision and the association.
	if err := tx.Preload("Domain").First(&alias, alias.ID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := tx.Model(&models.Alias{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("missing ID must not exist: count %d, error %v", count, err)
	}
	lookup := func(id uint) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/aliases/%d", id), nil)
		ctx := chi.NewRouteContext()
		ctx.URLParams.Add("id", fmt.Sprint(id))
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
		rr := httptest.NewRecorder()
		aliasRoutes(NewAliasHandler(tx)).ServeHTTP(rr, r)
		return rr
	}
	rr := lookup(missing)
	if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
		t.Fatalf("missing ID answered %d %s, want 404 %s", rr.Code, rr.Body, mustJSON(want))
	}
	rr = lookup(alias.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("created ID answered %d %s", rr.Code, rr.Body)
	}
	wantSuccess := map[string]interface{}{"data": parseShapeJSON(t, string(mustJSON(alias)))}
	if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, wantSuccess) {
		t.Fatalf("lookup returned %s, want %s", mustJSON(got), mustJSON(wantSuccess))
	}
	var stored models.Alias
	if err := tx.Preload("Domain").First(&stored, alias.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, alias) {
		t.Fatalf("lookup changed stored alias")
	}
	if err := tx.Model(&models.Alias{}).Where("id = ?", missing).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("lookup created missing ID: count %d, error %v", count, err)
	}
}
