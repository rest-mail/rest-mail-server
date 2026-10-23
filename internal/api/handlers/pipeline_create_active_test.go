package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// This test never migrates or clears shared tables: all writes are confined to
// a newly created database, whose identity is checked before migrating.
func openPipelineCreateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	admin, err := connectTestDB()
	if err != nil {
		t.Skip("pipeline create DB test: no database reachable")
	}
	name := fmt.Sprintf("restmail_pipelinecreate_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skip("pipeline create DB test: cannot create scratch database")
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Errorf("drop scratch database: %v", err)
		} else {
			t.Logf("dropped isolated scratch database %s", name)
		}
	})
	cfg := testDBConfig()
	cfg.DBName = name
	scratch, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal("connect to scratch database failed")
	}
	sqlDB, err := scratch.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var current string
	if err := scratch.Raw("SELECT current_database()").Scan(&current).Error; err != nil {
		t.Fatal(err)
	}
	if current != name {
		t.Fatalf("database = %q, want scratch %q", current, name)
	}
	t.Logf("verified isolated scratch database %s", current)
	if err := scratch.AutoMigrate(&models.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	return scratch
}

func TestPipelineCreate_ActivePersistence(t *testing.T) {
	db := openPipelineCreateTestDB(t)
	h := NewPipelineHandler(db, nil, filters.JSSidecar{})
	for _, tc := range []struct {
		name   string
		active string
		want   bool
	}{
		{"explicit_false", `,"active":false`, false},
		{"explicit_true", `,"active":true`, true},
		{"omitted", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"domain_id":1,"direction":"inbound","filters":[]` + tc.active + `}`
			rr := httptest.NewRecorder()
			pipelineRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/pipelines", strings.NewReader(body)))
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
			}
			var response struct {
				Data struct {
					ID     uint  `json:"id"`
					Active *bool `json:"active"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Data.Active == nil || *response.Data.Active != tc.want {
				t.Errorf("returned JSON = %s, want active:%t", rr.Body.String(), tc.want)
			}
			if response.Data.ID == 0 {
				t.Fatal("returned pipeline ID is zero")
			}
			var stored models.Pipeline
			if err := db.First(&stored, response.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Active != tc.want {
				t.Errorf("stored active = %t, want %t", stored.Active, tc.want)
			}
			if stored.DomainID != 1 || stored.Direction != "inbound" || string(stored.Filters) != "[]" || stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
				t.Errorf("created pipeline lost fields: %+v", stored)
			}
		})
	}
	t.Run("model_default_unchanged", func(t *testing.T) {
		p := models.Pipeline{DomainID: 1, Direction: "inbound", Filters: json.RawMessage(`[]`)}
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		var stored models.Pipeline
		if err := db.First(&stored, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !p.Active || !stored.Active {
			t.Fatal("ordinary model creation must still default active to true")
		}
	})
	t.Run("create_failure_leaves_no_row", func(t *testing.T) {
		// Reject an otherwise valid request in PostgreSQL to exercise the
		// handler's insert failure path, within this scratch database only.
		if err := db.Exec("ALTER TABLE pipelines ADD CONSTRAINT test_refuse_outbound CHECK (direction <> 'outbound')").Error; err != nil {
			t.Fatal(err)
		}
		var before, after int64
		if err := db.Model(&models.Pipeline{}).Count(&before).Error; err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		body := `{"domain_id":1,"direction":"outbound","filters":[],"active":false}`
		pipelineRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/pipelines", strings.NewReader(body)))
		if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), `"code":"internal_error"`) {
			t.Errorf("failed create = %d %s, want internal_error/500", rr.Code, rr.Body.String())
		}
		if err := db.Model(&models.Pipeline{}).Count(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Errorf("failed create changed row count from %d to %d", before, after)
		}
	})
}
