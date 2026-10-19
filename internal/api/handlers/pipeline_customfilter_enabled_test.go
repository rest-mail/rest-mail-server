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

// Never use the shared test pool: even migrations and fixtures belong only in
// a fresh scratch database, whose identity is checked before any writes.
func openCustomFilterCreateScratchDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := testDBConfig()
	cfg.DBName = "postgres"
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("custom filter create DB test: admin database unavailable: %v", err)
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
	name := fmt.Sprintf("restmail_customfiltercreate_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatalf("create isolated scratch database: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Errorf("drop scratch database: %v", err)
		} else {
			t.Logf("dropped isolated scratch database %s", name)
		}
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

func TestCustomFilterCreate_EnabledPersistence(t *testing.T) {
	db := openCustomFilterCreateScratchDB(t)
	earliest := time.Now().Add(-time.Minute)
	domain := models.Domain{Name: "customfilter-enabled.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	h := NewPipelineHandler(db, nil, filters.JSSidecar{})
	counts := func() (int64, int64) {
		t.Helper()
		var domains, filters int64
		if err := db.Model(&models.Domain{}).Count(&domains).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&models.CustomFilter{}).Count(&filters).Error; err != nil {
			t.Fatal(err)
		}
		return domains, filters
	}
	body := func(name, enabled string) string {
		// These forged fields must never reach the model or its association.
		return fmt.Sprintf(`{"domain_id":%d,"name":%q,"description":"test filter","filter_type":"transform","direction":"both","config":{"script":"noop"},"id":987654,"created_at":"2000-01-01T00:00:00Z","updated_at":"2000-01-01T00:00:00Z","domain":{"name":"forged-domain.test","active":true}%s}`, domain.ID, name, enabled)
	}
	create := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.CreateCustomFilter(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/custom-filters", strings.NewReader(body)))
		return rr
	}
	for _, tc := range []struct {
		name    string
		enabled string
		want    bool
	}{
		{"explicit_false", `,"enabled":false`, false},
		{"explicit_true", `,"enabled":true`, true},
		{"omitted", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			domainsBefore, filtersBefore := counts()
			rr := create(body(tc.name, tc.enabled))
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", rr.Code, rr.Body.String())
			}
			var response struct {
				Data struct {
					models.CustomFilter
					Enabled *bool `json:"enabled"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Data.Enabled == nil || *response.Data.Enabled != tc.want {
				t.Errorf("returned JSON = %s, want enabled:%t", rr.Body.String(), tc.want)
			}
			if response.Data.ID == 0 || response.Data.ID == 987654 {
				t.Fatalf("invalid server-assigned ID: %d", response.Data.ID)
			}
			var stored models.CustomFilter
			if err := db.First(&stored, response.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Enabled != tc.want {
				t.Errorf("stored enabled = %t, want %t", stored.Enabled, tc.want)
			}
			for _, cf := range []models.CustomFilter{response.Data.CustomFilter, stored} {
				if cf.DomainID != domain.ID || cf.Name != tc.name || cf.Description != "test filter" || cf.FilterType != "transform" || cf.Direction != "both" || !json.Valid(cf.Config) || !strings.Contains(string(cf.Config), `"noop"`) || cf.CreatedAt.Before(earliest) || cf.UpdatedAt.Before(earliest) {
					t.Errorf("created filter lost fields or accepted forged timestamps: %+v", cf)
				}
			}
			if domainsAfter, filtersAfter := counts(); domainsAfter != domainsBefore || filtersAfter != filtersBefore+1 {
				t.Errorf("row counts = (%d, %d), want (%d, %d)", domainsAfter, filtersAfter, domainsBefore, filtersBefore+1)
			}
		})
	}
	t.Run("invalid_requests_leave_no_filter_or_domain", func(t *testing.T) {
		for _, tc := range []struct {
			name, from, to string
			status         int
		}{
			{"missing_name", `"name":"invalid"`, `"name":""`, http.StatusUnprocessableEntity},
			{"missing_type", `"filter_type":"transform"`, `"filter_type":""`, http.StatusUnprocessableEntity},
			{"missing_direction", `"direction":"both"`, `"direction":""`, http.StatusUnprocessableEntity},
			{"invalid_type", `"filter_type":"transform"`, `"filter_type":"invalid"`, http.StatusUnprocessableEntity},
			{"invalid_direction", `"direction":"both"`, `"direction":"invalid"`, http.StatusUnprocessableEntity},
			{"invalid_enabled", `"enabled":false`, `"enabled":"false"`, http.StatusBadRequest},
		} {
			t.Run(tc.name, func(t *testing.T) {
				domainsBefore, filtersBefore := counts()
				rr := create(strings.Replace(body("invalid", `,"enabled":false`), tc.from, tc.to, 1))
				if rr.Code != tc.status {
					t.Errorf("invalid create = %d %s, want %d", rr.Code, rr.Body.String(), tc.status)
				}
				if domainsAfter, filtersAfter := counts(); domainsAfter != domainsBefore || filtersAfter != filtersBefore {
					t.Errorf("invalid create changed row counts from (%d, %d) to (%d, %d)", domainsBefore, filtersBefore, domainsAfter, filtersAfter)
				}
			})
		}
	})
	t.Run("model_default_unchanged", func(t *testing.T) {
		cf := models.CustomFilter{DomainID: domain.ID, Name: "model-default", FilterType: "action", Direction: "inbound", Config: json.RawMessage(`{}`)}
		if err := db.Create(&cf).Error; err != nil {
			t.Fatal(err)
		}
		var stored models.CustomFilter
		if err := db.First(&stored, cf.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !cf.Enabled || !stored.Enabled {
			t.Fatal("ordinary model creation must still default enabled to true")
		}
	})
	t.Run("create_failure_leaves_no_filter_or_domain", func(t *testing.T) {
		if err := db.Exec("ALTER TABLE custom_filters ADD CONSTRAINT test_refuse_name CHECK (name <> 'refused')").Error; err != nil {
			t.Fatal(err)
		}
		domainsBefore, filtersBefore := counts()
		rr := create(body("refused", `,"enabled":false`))
		if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), `"code":"internal_error"`) {
			t.Errorf("failed create = %d %s, want internal_error/500", rr.Code, rr.Body.String())
		}
		if domainsAfter, filtersAfter := counts(); domainsAfter != domainsBefore || filtersAfter != filtersBefore {
			t.Errorf("failed create changed row counts from (%d, %d) to (%d, %d)", domainsBefore, filtersBefore, domainsAfter, filtersAfter)
		}
	})
}
