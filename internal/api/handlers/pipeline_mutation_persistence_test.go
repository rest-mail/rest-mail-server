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
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// No shared test database writes. The database helper verifies a fresh scratch
// database before migration; rollback/refusal and durable identity are checked.
func TestPipelineMutationPersistence(t *testing.T) {
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db.NowFunc = func() time.Time { return at }
	h := NewPipelineHandler(db, nil, filters.JSSidecar{})
	domain := models.Domain{Name: "pipeline.example.org", ServerType: "traditional"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"create_disabled", "create_default", "create_cancelled", "update_active", "update_empty", "update_clear", "update_cancelled", "update_failure", "concurrent"} {
		t.Run(name, func(t *testing.T) {
			original := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(`[{"name":"header_validate","config":{}}]`), Active: true}
			create := strings.HasPrefix(name, "create_")
			method := http.MethodPatch
			body := `{"active":false}`
			path := "/api/v1/admin/pipelines"
			if create {
				method = http.MethodPost
				body = fmt.Sprintf(`{"domain_id":%d,"direction":"outbound","filters":[]}`, domain.ID)
				if name == "create_disabled" {
					body = fmt.Sprintf(`{"domain_id":%d,"direction":"outbound","filters":[],"active":false}`, domain.ID)
				}
			} else {
				if err := db.Create(&original).Error; err != nil {
					t.Fatal(err)
				}
				path += fmt.Sprintf("/%d", original.ID)
			}
			if name == "update_empty" {
				body = `{}`
			}
			if name == "update_clear" {
				body = `{"filters":[]}`
			}
			var before int64
			if err := db.Model(&models.Pipeline{}).Count(&before).Error; err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			req = req.WithContext(ctx)
			if err := db.Callback().Query().After("gorm:after_query").Register("pipeline-mutation-query", func(tx *gorm.DB) {
				if name == "update_cancelled" && tx.Statement.Table == "pipelines" {
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Create().Before("gorm:create").Register("pipeline-mutation-create", func(tx *gorm.DB) {
				if name == "create_cancelled" && tx.Statement.Table == "pipelines" {
					cancel()
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Before("gorm:update").Register("pipeline-mutation-update", func(tx *gorm.DB) {
				if tx.Statement.Table != "pipelines" {
					return
				}
				if name == "update_failure" {
					_ = tx.AddError(errors.New("private update failure"))
				}
				if name == "concurrent" {
					_ = tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("DELETE FROM pipelines WHERE id = ?", original.ID).Error)
				}
			}); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			pipelineRoutes(h).ServeHTTP(rr, req)
			if err := db.Callback().Query().Remove("pipeline-mutation-query"); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Create().Remove("pipeline-mutation-create"); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Remove("pipeline-mutation-update"); err != nil {
				t.Fatal(err)
			}
			want := 200
			if create {
				want = 201
			}
			if strings.HasSuffix(name, "cancelled") || name == "update_failure" {
				want = 500
			}
			if name == "concurrent" {
				want = 404
			}
			if rr.Code != want {
				t.Fatalf("status%d want%d", rr.Code, want)
			}
			if strings.Contains(rr.Body.String(), "private update") {
				t.Fatal("private storage detail exposed")
			}
			var after int64
			if err := db.Model(&models.Pipeline{}).Count(&after).Error; err != nil {
				t.Fatal(err)
			}
			expected := before
			if want == 201 {
				expected++
			}
			if name == "concurrent" {
				expected--
			}
			if after != expected {
				t.Fatal("pipeline count changed incorrectly or concurrent deletion was resurrected")
			}
			if name == "concurrent" || name == "create_cancelled" {
				return
			}
			id := original.ID
			if create {
				var response struct {
					Data Pipeline `json:"data"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				id = response.Data.ID
			}
			var got models.Pipeline
			if err := db.First(&got, id).Error; err != nil {
				t.Fatal(err)
			}
			if got.DomainID != domain.ID || !got.CreatedAt.Equal(at) {
				t.Fatal("pipeline identity or installation metadata changed")
			}
			if create {
				if got.Direction != "outbound" || got.Active != (name == "create_default") || string(got.Filters) != "[]" {
					t.Fatal("creation changed explicit/default policy or filters")
				}
				return
			}
			if got.Direction != original.Direction {
				t.Fatal("update changed immutable direction")
			}
			if name == "update_active" {
				if got.Active {
					t.Fatal("active false not persisted")
				}
			} else if !got.Active {
				t.Fatal("omitted/failed active update changed current setting")
			}
			if name == "update_clear" {
				if string(got.Filters) != "[]" {
					t.Fatal("explicit empty filters not persisted")
				}
			} else {
				var a, b interface{}
				if json.Unmarshal(got.Filters, &a) != nil || json.Unmarshal(original.Filters, &b) != nil {
					t.Fatal("invalid stored filter JSON")
				}
				aa, _ := json.Marshal(a)
				bb, _ := json.Marshal(b)
				if string(aa) != string(bb) {
					t.Fatal("omitted or failed update changed filters")
				}
			}
		})
	}
}
