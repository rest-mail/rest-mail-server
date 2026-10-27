package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"

	yaml "go.yaml.in/yaml/v2"
)

// Each missing-target request from both descriptions must reach the registered
// route and leave existing pipelines untouched, including their filter config.
func TestPipelineOperations_DocumentedMissingExamples(t *testing.T) {
	type operation struct {
		OperationID string `yaml:"operationId"`
		Parameters  []struct {
			Name     string `yaml:"name"`
			In       string `yaml:"in"`
			Required bool   `yaml:"required"`
			Examples map[string]struct {
				Value any `yaml:"value"`
			} `yaml:"examples"`
		} `yaml:"parameters"`
		RequestBody struct {
			Content map[string]namedExamples `yaml:"content"`
		} `yaml:"requestBody"`
		Responses map[string]struct {
			Content map[string]namedExamples `yaml:"content"`
		} `yaml:"responses"`
	}
	var doc struct {
		Paths map[string]struct {
			Patch  operation `yaml:"patch"`
			Delete operation `yaml:"delete"`
			Post   operation `yaml:"post"`
		} `yaml:"paths"`
	}
	data, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	data, err = restmail.Examples.ReadFile("openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]map[string]struct {
		Request struct {
			// Raw, because other operations' examples use non-integer IDs on purpose.
			Path map[string]json.RawMessage `json:"path"`
			Body interface{}                `json:"body"`
		} `json:"request"`
		Response struct {
			Status int         `json:"status"`
			Body   interface{} `json:"body"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	const missing = 2147483647
	want := parseShapeJSON(t, `{"error":{"code":"not_found","message":"Pipeline not found"}}`)
	for _, tc := range []struct {
		id, method, path, body string
	}{
		{"updatePipeline", http.MethodPatch, "/api/v1/admin/pipelines/{id}", `{"active":false}`},
		{"deletePipeline", http.MethodDelete, "/api/v1/admin/pipelines/{id}", ""},
		{"testPipeline", http.MethodPost, "/api/v1/admin/pipelines/test", `{"pipeline_id":2147483647,"email":{}}`},
	} {
		t.Run(tc.id, func(t *testing.T) {
			path := doc.Paths[tc.path]
			op := map[string]operation{http.MethodPatch: path.Patch, http.MethodDelete: path.Delete, http.MethodPost: path.Post}[tc.method]
			if op.OperationID != tc.id {
				t.Fatalf("operation = %q, want %q", op.OperationID, tc.id)
			}
			pair := generated[tc.id]["missing"]
			refusals := op.Responses["404"].Content["application/json"].Examples
			if len(refusals) != 1 || !reflect.DeepEqual(jsonValue(t, refusals["missing"].Value), want) || pair.Response.Status != 404 || !reflect.DeepEqual(pair.Response.Body, want) {
				t.Fatalf("missing refusal differs between descriptions: YAML %+v, generated %+v", refusals, pair)
			}
			if tc.method != http.MethodPost {
				if len(op.Parameters) != 1 {
					t.Fatalf("expected one named ID parameter: %+v", op.Parameters)
				}
				p := op.Parameters[0]
				if p.Name != "id" || p.In != "path" || !p.Required || contractPathExampleID(t, p.Examples["found"].Value) != 1 || contractPathExampleID(t, p.Examples["missing"].Value) != missing || string(pair.Request.Path["id"]) != strconv.FormatUint(uint64(missing), 10) {
					t.Fatalf("missing path differs: parameter %+v, generated %+v", p, pair.Request.Path)
				}
			}
			requests := op.RequestBody.Content["application/json"].Examples
			if tc.body == "" {
				if len(requests) != 0 || pair.Request.Body != nil {
					t.Fatal("delete must not require a body")
				}
			} else {
				body := parseShapeJSON(t, tc.body)
				if !reflect.DeepEqual(jsonValue(t, requests["missing"].Value), body) || !reflect.DeepEqual(pair.Request.Body, body) {
					t.Fatalf("missing request differs: YAML %+v, generated %+v", requests, pair.Request.Body)
				}
			}

			t.Run("PostgreSQL", func(t *testing.T) {
				gdb, err := connectTestDB()
				if err != nil {
					t.Skipf("pipeline missing-target contract test requires PostgreSQL: %v", err)
				}
				if err := gdb.AutoMigrate(&models.Domain{}, &models.Pipeline{}); err != nil {
					t.Fatal(err)
				}
				tx := gdb.Begin()
				if tx.Error != nil {
					t.Fatal(tx.Error)
				}
				t.Cleanup(func() { tx.Rollback() })
				domain := models.Domain{Name: "pipeline-missing-contract.test"}
				if err := tx.Create(&domain).Error; err != nil {
					t.Fatal(err)
				}
				pipeline := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(`[{"name":"header_validate","config":{}}]`), Active: true}
				if err := tx.Create(&pipeline).Error; err != nil {
					t.Fatal(err)
				}
				var before, after models.Pipeline
				if err := tx.First(&before, pipeline.ID).Error; err != nil {
					t.Fatal(err)
				}
				var absent, countBefore, countAfter int64
				if err := tx.Model(&models.Pipeline{}).Where("id = ?", missing).Count(&absent).Error; err != nil || absent != 0 {
					t.Fatalf("missing pipeline must be absent: count %d, error %v", absent, err)
				}
				if err := tx.Model(&models.Pipeline{}).Count(&countBefore).Error; err != nil {
					t.Fatal(err)
				}
				target := strings.Replace(tc.path, "{id}", fmt.Sprint(missing), 1)
				req := httptest.NewRequest(tc.method, target, bytes.NewBufferString(tc.body))
				if tc.body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				rr := httptest.NewRecorder()
				pipelineRoutes(NewPipelineHandler(tx, nil, filters.JSSidecar{})).ServeHTTP(rr, req)
				if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
					t.Fatalf("missing pipeline answered %d %s, want404 %s", rr.Code, rr.Body, mustJSON(want))
				}
				if err := tx.First(&after, pipeline.ID).Error; err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("refusal changed pipeline: before %+v, after %+v, error %v", before, after, err)
				}
				if err := tx.Model(&models.Pipeline{}).Where("id = ?", missing).Count(&absent).Error; err != nil || absent != 0 {
					t.Fatalf("refusal created missing pipeline: count %d, error %v", absent, err)
				}
				if err := tx.Model(&models.Pipeline{}).Count(&countAfter).Error; err != nil || countAfter != countBefore {
					t.Fatalf("refusal changed pipeline count: before %d, after %d, error %v", countBefore, countAfter, err)
				}
			})
		})
	}
}
