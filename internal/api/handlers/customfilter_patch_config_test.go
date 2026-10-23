package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// updateCustomFilter's config, when supplied, is an object
// (UpdateCustomFilterRequest, #388), as createCustomFilter's is. These tests
// PATCH a custom filter stored in a scratch database and read the row back: a
// refusal must leave it exactly as it was, enabled and name included.

// openCustomFilterPatchScratchDB is a fresh database of its own, never the
// shared test pool: its identity is checked before anything is migrated or
// written, and it is dropped, and checked gone, when the test ends.
func openCustomFilterPatchScratchDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := testDBConfig()
	cfg.DBName = "postgres"
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("custom filter PATCH DB test: admin database unavailable: %v", err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	var current string
	if err := admin.Raw("SELECT current_database()").Scan(&current).Error; err != nil || current != "postgres" {
		t.Fatalf("admin database identity = %q, err = %v", current, err)
	}
	name := fmt.Sprintf("restmail_cfpatch_%d_%d", os.Getpid(), time.Now().UnixNano())
	if !regexp.MustCompile(`^restmail_cfpatch_[0-9]+_[0-9]+$`).MatchString(name) {
		t.Fatalf("scratch database name %q is not letters, digits and underscores", name)
	}
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatalf("create isolated scratch database: %v", err)
	}
	// Cleanups run last-added first: the scratch pool below is closed before
	// this drop, so nothing of the test's own still holds the database.
	t.Cleanup(func() {
		t.Logf("dropping isolated scratch database %s", name)
		if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Errorf("drop scratch database: %v", err)
			return
		}
		var left int64
		if err := admin.Raw("SELECT count(*) FROM pg_database WHERE datname = ?", name).Scan(&left).Error; err != nil || left != 0 {
			t.Errorf("scratch database %s still there after drop: count %d, err %v", name, left, err)
			return
		}
		t.Logf("dropped isolated scratch database %s; none left by that name", name)
	})
	cfg.DBName = name
	scratch, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	scratchSQL, err := scratch.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratchSQL.Close() })
	if err := scratch.Raw("SELECT current_database()").Scan(&current).Error; err != nil || current != name {
		t.Fatalf("scratch database identity = %q, want %q, err = %v", current, name, err)
	}
	t.Logf("verified isolated scratch database %s before migrations/writes", current)
	if err := scratch.AutoMigrate(&models.Domain{}, &models.CustomFilter{}); err != nil {
		t.Fatal(err)
	}
	return scratch
}

func TestCustomFilterPatchConfig_ScratchDB(t *testing.T) {
	db := openCustomFilterPatchScratchDB(t)
	domain := models.Domain{Name: "customfilter-patch.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	const (
		storedName   = "invoice-tagger"
		storedConfig = `{"script":"function filter(email) { return { action: \"continue\" }; }"}`
	)
	cf := models.CustomFilter{
		DomainID: domain.ID, Name: storedName, Description: "Tags invoices",
		FilterType: "action", Direction: "inbound", Config: json.RawMessage(storedConfig), Enabled: true,
	}
	if err := db.Create(&cf).Error; err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(cf.ID)
	h := NewPipelineHandler(db, nil, filters.JSSidecar{})

	patch := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/custom-filters/"+id, strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		pipelineRoutes(h).ServeHTTP(rr, req)
		return rr
	}
	// row reads the custom filter back, and checks it is still the only one.
	row := func(t *testing.T) models.CustomFilter {
		t.Helper()
		var n int64
		if err := db.Model(&models.CustomFilter{}).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("custom_filters count = %d, err %v, want 1", n, err)
		}
		var got models.CustomFilter
		if err := db.First(&got, cf.ID).Error; err != nil {
			t.Fatal(err)
		}
		return got
	}
	// reset puts the stored filter back to its seeded name, config and enabled.
	reset := func(t *testing.T) models.CustomFilter {
		t.Helper()
		if err := db.Exec("UPDATE custom_filters SET name = ?, config = ?::jsonb, enabled = true WHERE id = ?",
			storedName, storedConfig, cf.ID).Error; err != nil {
			t.Fatal(err)
		}
		return row(t)
	}
	// unchanged checks a refused PATCH left the stored row exactly as reset made it.
	unchanged := func(t *testing.T, before models.CustomFilter) {
		t.Helper()
		after := row(t)
		if !sameCustomFilterJSON(t, after.Config, before.Config) || after.Enabled != before.Enabled ||
			after.Name != before.Name || after.Description != before.Description ||
			after.FilterType != before.FilterType || after.Direction != before.Direction ||
			after.DomainID != before.DomainID || !after.CreatedAt.Equal(before.CreatedAt) ||
			!after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("refused PATCH changed the stored custom filter: before %+v, after %+v", before, after)
		}
	}
	errorCode := func(t *testing.T, rr *httptest.ResponseRecorder) string {
		t.Helper()
		var got struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %s: %v", rr.Body, err)
		}
		return got.Error.Code
	}

	// Every body also renames the filter and disables it: a refusal that wrote
	// any part of the row would show in the read-back.
	t.Run("refused", func(t *testing.T) {
		want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation",`+
			`"details":{"fields":{"config":"must be an object"}}}}`)
		for name, config := range map[string]string{
			"null":         `null`,
			"empty array":  `[]`,
			"array":        `[{"script":"x"}]`,
			"string":       `"function filter(email) {}"`,
			"empty string": `""`,
			"number":       `1`,
			"true":         `true`,
			"false":        `false`,
		} {
			t.Run(name, func(t *testing.T) {
				before := reset(t)
				body := `{"name":"renamed","enabled":false,"config":` + config + `}`
				rr := patch(id, body)
				if rr.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
				}
				if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
					t.Errorf("%s answered %s", body, rr.Body)
				}
				unchanged(t, before)
			})
		}
	})

	// Any object is a config, the empty one included; what it holds is the
	// filter's own concern. Each is stored, and so is enabled:false.
	t.Run("accepted", func(t *testing.T) {
		for name, config := range map[string]string{
			"empty object": `{}`,
			"script":       `{"script":"function filter(email) { return { action: \"reject\" }; }"}`,
			"nested":       `{"a":{"b":[1,2,null]},"c":false}`,
		} {
			t.Run(name, func(t *testing.T) {
				reset(t)
				rr := patch(id, `{"enabled":false,"config":`+config+`}`)
				if rr.Code != http.StatusOK {
					t.Fatalf("status %d, want 200; body %s", rr.Code, rr.Body)
				}
				got := row(t)
				if !sameCustomFilterJSON(t, got.Config, json.RawMessage(config)) || got.Enabled || got.Name != storedName {
					t.Errorf("stored config %s enabled %t name %q, want %s enabled false name %q",
						got.Config, got.Enabled, got.Name, config, storedName)
				}
			})
		}
	})

	// With no config the stored one stays as it was; only what was sent changes.
	t.Run("omitted", func(t *testing.T) {
		for _, body := range []string{`{"enabled":false}`, `{}`, `{"description":"Tags invoices"}`} {
			before := reset(t)
			rr := patch(id, body)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status %d, want 200; body %s", body, rr.Code, rr.Body)
			}
			got := row(t)
			wantEnabled := body != `{"enabled":false}`
			if !sameCustomFilterJSON(t, got.Config, before.Config) || got.Enabled != wantEnabled || got.Name != storedName {
				t.Errorf("%s stored config %s enabled %t name %q", body, got.Config, got.Enabled, got.Name)
			}
		}
	})

	// The other refusals keep their status, code and order: an id that is not
	// one, a filter that is not there (before the body is read), a body that is
	// not JSON, and an emptied name or an unknown type ahead of the config.
	t.Run("other refusals kept", func(t *testing.T) {
		for name, tc := range map[string]struct {
			id, body string
			status   int
			code     string
			fields   string
		}{
			"bad id":       {"abc", `{"enabled":false,"config":null}`, http.StatusUnprocessableEntity, "validation_failed", ""},
			"missing":      {"987654", `{"enabled":false,"config":null}`, http.StatusNotFound, "not_found", ""},
			"not json":     {id, `{"enabled":false,"config":`, http.StatusBadRequest, "bad_request", ""},
			"empty name":   {id, `{"name":"","enabled":false,"config":null}`, http.StatusUnprocessableEntity, "validation_failed", `{"name":"required"}`},
			"unknown type": {id, `{"filter_type":"script","enabled":false,"config":[]}`, http.StatusUnprocessableEntity, "validation_failed", `{"filter_type":"must be action or transform"}`},
		} {
			t.Run(name, func(t *testing.T) {
				before := reset(t)
				rr := patch(tc.id, tc.body)
				if rr.Code != tc.status || errorCode(t, rr) != tc.code {
					t.Fatalf("%s: status %d body %s, want %d %s", tc.body, rr.Code, rr.Body, tc.status, tc.code)
				}
				if tc.fields != "" {
					var got struct {
						Error struct {
							Details struct {
								Fields json.RawMessage `json:"fields"`
							} `json:"details"`
						} `json:"error"`
					}
					if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
						t.Fatalf("decode %s: %v", rr.Body, err)
					}
					if !sameCustomFilterJSON(t, got.Error.Details.Fields, json.RawMessage(tc.fields)) {
						t.Errorf("%s: fields %s, want %s", tc.body, got.Error.Details.Fields, tc.fields)
					}
				}
				unchanged(t, before)
			})
		}
	})
}

// sameCustomFilterJSON compares two JSON documents by value: jsonb does not
// keep the whitespace or key order it was sent.
func sameCustomFilterJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y interface{}
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return reflect.DeepEqual(x, y)
}
