package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// updatePipeline's filters, when supplied, are an array of objects
// (UpdatePipelineRequest, #388), as createPipeline's are. These tests PATCH a
// pipeline stored in a scratch database and read the row back: a refusal must
// leave it exactly as it was, active included.

// openPipelineUpdateScratchDB is a fresh database of its own, never the shared
// test pool: its identity is checked before anything is migrated or written,
// and it is dropped, and checked gone, when the test ends.
func openPipelineUpdateScratchDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := testDBConfig()
	cfg.DBName = "postgres"
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("pipeline update DB test: admin database unavailable: %v", err)
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
	name := fmt.Sprintf("restmail_pipelinepatch_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatalf("create isolated scratch database: %v", err)
	}
	t.Cleanup(func() {
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
	if err := scratch.AutoMigrate(&models.Domain{}, &models.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	return scratch
}

func TestUpdatePipelineFilterShape_ScratchDB(t *testing.T) {
	db := openPipelineUpdateScratchDB(t)
	domain := models.Domain{Name: "pipeline-patch.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	const stored = `[{"name":"header_validate","config":{}}]`
	p := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(stored), Active: true}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(p.ID)
	h := NewPipelineHandler(db, nil, filters.JSSidecar{})

	patch := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/pipelines/"+id, strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		pipelineRoutes(h).ServeHTTP(rr, req)
		return rr
	}
	// reset puts the stored pipeline back to its seeded filters, active.
	reset := func(t *testing.T) models.Pipeline {
		t.Helper()
		if err := db.Exec("UPDATE pipelines SET filters = ?::jsonb, active = true WHERE id = ?", stored, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		var row models.Pipeline
		if err := db.First(&row, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	// row reads the pipeline back, and checks it is still the only one.
	row := func(t *testing.T) models.Pipeline {
		t.Helper()
		var n int64
		if err := db.Model(&models.Pipeline{}).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("pipelines count = %d, err %v, want 1", n, err)
		}
		var got models.Pipeline
		if err := db.First(&got, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		return got
	}
	// unchanged checks a refused PATCH left the stored row exactly as reset made it.
	unchanged := func(t *testing.T, before models.Pipeline) {
		t.Helper()
		after := row(t)
		if !sameJSON(t, after.Filters, before.Filters) || after.Active != before.Active ||
			!after.UpdatedAt.Equal(before.UpdatedAt) || after.Direction != before.Direction || after.DomainID != before.DomainID {
			t.Errorf("refused PATCH changed the stored pipeline: before %+v, after %+v", before, after)
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

	t.Run("refused", func(t *testing.T) {
		want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation",`+
			`"details":{"fields":{"filters":"must be an array of filter objects"}}}}`)
		for name, filters := range map[string]string{
			"null":           `null`,
			"object":         `{"name":"header_validate","config":{}}`,
			"empty object":   `{}`,
			"string":         `"header_validate"`,
			"number":         `1`,
			"true":           `true`,
			"false":          `false`,
			"null item":      `[null]`,
			"string item":    `["header_validate"]`,
			"number item":    `[1]`,
			"boolean item":   `[true]`,
			"array item":     `[[]]`,
			"one bad item":   `[{"name":"header_validate","config":{}},null]`,
			"nested arrays":  `[[{"name":"header_validate"}]]`,
			"numeric name":   `[{"name":12345}]`,
			"null name":      `[{"name":null}]`,
			"boolean name":   `[{"name":true}]`,
			"object name":    `[{"name":{}}]`,
			"array name":     `[{"name":[]}]`,
			"null config":    `[{"config":null}]`,
			"string config":  `[{"config":"bad"}]`,
			"numeric config": `[{"config":12345}]`,
			"boolean config": `[{"config":true}]`,
			"array config":   `[{"config":[]}]`,
			"bad later name": `[{"name":"header_validate","config":{}},{"name":12345}]`,
		} {
			t.Run(name, func(t *testing.T) {
				before := reset(t)
				body := `{"active":false,"filters":` + filters + `}`
				rr := patch(id, body)
				if rr.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
				}
				if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
					t.Errorf("%s answered %s, want %s", body, rr.Body, mustJSON(want))
				}
				unchanged(t, before)
			})
		}
	})

	// An empty list, an empty object and objects with keys beyond name and
	// config are the documented shape: each is stored, and so is active:false.
	t.Run("accepted", func(t *testing.T) {
		for name, filters := range map[string]string{
			"empty":            `[]`,
			"empty object":     `[{}]`,
			"empty name":       `[{"name":"","config":{}}]`,
			"arbitrary config": `[{"config":{"nested":[null,1,"x"],"enabled":false}}]`,
			"documented":       `[{"name":"spf_check","config":{}}]`,
			"extra keys":       `[{"name":"header_validate","type":"check","enabled":true,"config":{}}]`,
			"several": `[{"name":"header_validate","config":{}},` +
				`{"name":"duplicate","enabled":false,"config":{"webhook_url":"https://example.com/hook"}}]`,
		} {
			t.Run(name, func(t *testing.T) {
				reset(t)
				rr := patch(id, `{"active":false,"filters":`+filters+`}`)
				if rr.Code != http.StatusOK {
					t.Fatalf("status %d, want 200; body %s", rr.Code, rr.Body)
				}
				got := row(t)
				if !sameJSON(t, got.Filters, json.RawMessage(filters)) || got.Active {
					t.Errorf("stored filters %s active %t, want %s active false", got.Filters, got.Active, filters)
				}
			})
		}
	})

	// With no filters the stored list stays as it was; only active changes.
	t.Run("omitted", func(t *testing.T) {
		for _, body := range []string{`{"active":false}`, `{}`} {
			before := reset(t)
			rr := patch(id, body)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status %d, want 200; body %s", body, rr.Code, rr.Body)
			}
			got := row(t)
			if !sameJSON(t, got.Filters, before.Filters) || got.Active != (body == `{}`) {
				t.Errorf("%s stored filters %s active %t", body, got.Filters, got.Active)
			}
		}
	})

	// The other refusals keep their status, code and order: an id that is not
	// one, a pipeline that is not there (before the body is read), a body that
	// is not JSON, a broken Sieve script and a duplicate's queue_recipient.
	t.Run("other refusals kept", func(t *testing.T) {
		for name, tc := range map[string]struct {
			id, body string
			status   int
			code     string
		}{
			"bad id":          {"abc", `{"active":false,"filters":null}`, http.StatusUnprocessableEntity, "validation_failed"},
			"missing":         {"987654", `{"active":false,"filters":null}`, http.StatusNotFound, "not_found"},
			"not json":        {id, `{"active":false,"filters":`, http.StatusBadRequest, "bad_request"},
			"broken sieve":    {id, `{"active":false,"filters":` + missingRequireFilters + `}`, http.StatusUnprocessableEntity, "validation_failed"},
			"queue recipient": {id, `{"active":false,"filters":` + duplicateQueueFilters + `}`, http.StatusUnprocessableEntity, "validation_failed"},
		} {
			t.Run(name, func(t *testing.T) {
				before := reset(t)
				rr := patch(tc.id, tc.body)
				if rr.Code != tc.status || errorCode(t, rr) != tc.code {
					t.Fatalf("%s: status %d body %s, want %d %s", tc.body, rr.Code, rr.Body, tc.status, tc.code)
				}
				unchanged(t, before)
			})
		}
	})
}

// sameJSON compares two JSON documents by value: jsonb does not keep the
// whitespace or key order it was sent.
func sameJSON(t *testing.T, a, b json.RawMessage) bool {
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
