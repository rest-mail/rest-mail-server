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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// Dry-run exercises the real GORM create callbacks without a database or
// socket connection. Checking bound values catches default-tag substitution.
func TestMTASTSInsert_ExplicitDefaultsSQL(t *testing.T) {
	for _, prefix := range []string{"", "isolated_"} {
		t.Run("prefix_"+prefix, func(t *testing.T) {
			db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=unused dbname=unused sslmode=disable"), &gorm.Config{
				DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true,
				NamingStrategy: schema.NamingStrategy{TablePrefix: prefix},
			})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pool.Close() })
			var captured *gorm.Statement
			if err := db.Callback().Create().After("gorm:create").Register("test:capture_mtasts_insert", func(tx *gorm.DB) {
				captured = tx.Statement
			}); err != nil {
				t.Fatal(err)
			}
			wantSchema := &gorm.Statement{DB: db}
			if err := wantSchema.Parse(&models.MTASTSPolicy{}); err != nil {
				t.Fatal(err)
			}
			policy := models.MTASTSPolicy{DomainID: 42, Mode: "none", MXHosts: "mx.restmail.test", MaxAge: 0, Active: false}
			if err := insertMTASTSPolicy(db, &policy); err != nil {
				t.Fatal(err)
			}
			if captured == nil || captured.Table != wantSchema.Schema.Table {
				t.Fatalf("insert table = %v, want %q", captured, wantSchema.Schema.Table)
			}
			values, ok := captured.Clauses["VALUES"].Expression.(clause.Values)
			if !ok || len(values.Values) != 1 {
				t.Fatalf("insert values = %#v", captured.Clauses["VALUES"])
			}
			bound := map[string]interface{}{}
			for i, column := range values.Columns {
				value := reflect.ValueOf(values.Values[0][i])
				for value.IsValid() && value.Kind() == reflect.Ptr && !value.IsNil() {
					value = value.Elem()
				}
				if value.IsValid() {
					bound[column.Name] = value.Interface()
				}
			}
			if bound["max_age"] != 0 || bound["active"] != false || bound["domain_id"] != uint(42) || bound["mode"] != "none" || bound["mx_hosts"] != "mx.restmail.test" {
				t.Fatalf("bound INSERT fields = %#v", bound)
			}
			if policy.MaxAge != 0 || policy.Active || policy.CreatedAt.IsZero() || policy.UpdatedAt.IsZero() {
				t.Fatalf("insert changed explicit values or lost timestamps: %+v", policy)
			}
			// This is the callback behavior that caused the original bug; the
			// HTTP insert overlay must not change ordinary model defaults.
			ordinary := models.MTASTSPolicy{DomainID: 43}
			if err := db.Create(&ordinary).Error; err != nil {
				t.Fatal(err)
			}
			if ordinary.MaxAge != 604800 || !ordinary.Active {
				t.Fatalf("ordinary model defaults changed: %+v", ordinary)
			}
		})
	}
}

// Every write is confined to a newly created, verified scratch database.
func openMTASTSPersistenceDB(t *testing.T) *gorm.DB {
	t.Helper()
	admin, err := connectTestDB()
	if err != nil {
		t.Skipf("MTA-STS persistence test: no database reachable (%v)", err)
	}
	name := fmt.Sprintf("restmail_mtasts_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Skipf("MTA-STS persistence test: cannot create scratch database (%v)", err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Errorf("drop isolated MTA-STS database: %v", err)
		}
	})
	cfg := testDBConfig()
	cfg.DBName = name
	db, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var current string
	if err := db.Raw("SELECT current_database()").Scan(&current).Error; err != nil || current != name {
		t.Fatalf("scratch identity = %q, want %q (error %v)", current, name, err)
	}
	t.Logf("verified isolated scratch database %s", current)
	if err := db.AutoMigrate(&models.Domain{}, &models.MTASTSPolicy{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func mtastsRequest(method string, domainID uint, body string) *http.Request {
	r := httptest.NewRequest(method, fmt.Sprintf("/api/v1/admin/domains/%d/mta-sts", domainID), strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", fmt.Sprint(domainID))
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestMTASTSPolicy_PersistedLifecycle(t *testing.T) {
	db := openMTASTSPersistenceDB(t)
	h := NewMTASTSHandler(db)
	for _, tc := range []struct {
		name, body string
		age        int
		active     bool
	}{
		{"omitted_defaults", `{}`, 604800, true},
		{"zero_and_false", `{"max_age":0,"active":false}`, 0, false},
		{"zero_and_true", `{"max_age":0,"active":true}`, 0, true},
		{"false_only", `{"active":false}`, 604800, false},
		{"custom_and_false", `{"max_age":71,"active":false}`, 71, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			domain := models.Domain{Name: tc.name + ".restmail.test", ServerType: "restmail"}
			if err := db.Create(&domain).Error; err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			policyRoutes(h).ServeHTTP(rr, mtastsRequest(http.MethodPut, domain.ID, tc.body))
			if rr.Code != http.StatusCreated {
				t.Fatalf("create = %d %s, want 201", rr.Code, rr.Body.String())
			}
			var response struct {
				Data models.MTASTSPolicy `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			created := response.Data
			if created.ID == 0 || created.DomainID != domain.ID || created.MaxAge != tc.age || created.Active != tc.active || created.Mode != "testing" || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
				t.Fatalf("created policy = %+v, want age %d active %t", created, tc.age, tc.active)
			}
			var stored models.MTASTSPolicy
			if err := db.First(&stored, created.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.MaxAge != tc.age || stored.Active != tc.active {
				t.Fatalf("persisted policy = %+v, want age %d active %t", stored, tc.age, tc.active)
			}
			if !tc.active {
				rr = httptest.NewRecorder()
				policyFile(h)(rr, httptest.NewRequest(http.MethodGet, "https://mta-sts."+domain.Name+"/.well-known/mta-sts.txt", nil))
				if rr.Code != http.StatusNotFound {
					t.Fatalf("new unpublished policy served: %d %s", rr.Code, rr.Body.String())
				}
			}
			// Existing-row saves must preserve explicit zero and false too.
			rr = httptest.NewRecorder()
			policyRoutes(h).ServeHTTP(rr, mtastsRequest(http.MethodPut, domain.ID, `{"mode":"none","mx_hosts":"mx.restmail.test,*.restmail.test","max_age":0,"active":false}`))
			if rr.Code != http.StatusOK {
				t.Fatalf("update = %d %s, want 200", rr.Code, rr.Body.String())
			}
			rr = httptest.NewRecorder()
			policyRoutes(h).ServeHTTP(rr, mtastsRequest(http.MethodPut, domain.ID, `{}`))
			if rr.Code != http.StatusOK {
				t.Fatalf("empty update = %d %s, want 200", rr.Code, rr.Body.String())
			}
			rr = httptest.NewRecorder()
			policyRoutes(h).ServeHTTP(rr, mtastsRequest(http.MethodGet, domain.ID, ""))
			if rr.Code != http.StatusOK {
				t.Fatalf("get = %d %s, want 200", rr.Code, rr.Body.String())
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			read := response.Data
			if read.ID != created.ID || !read.CreatedAt.Equal(stored.CreatedAt) || read.MaxAge != 0 || read.Active || read.Mode != "none" || read.MXHosts != "mx.restmail.test,*.restmail.test" {
				t.Fatalf("GET after update lost fields: %+v", read)
			}
			rr = httptest.NewRecorder()
			serve := httptest.NewRequest(http.MethodGet, "https://mta-sts."+domain.Name+"/.well-known/mta-sts.txt", nil)
			policyFile(h)(rr, serve)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("unpublished policy served: %d %s", rr.Code, rr.Body.String())
			}
			rr = httptest.NewRecorder()
			policyRoutes(h).ServeHTTP(rr, mtastsRequest(http.MethodDelete, domain.ID, ""))
			if rr.Code != http.StatusNoContent {
				t.Fatalf("delete = %d %s, want 204", rr.Code, rr.Body.String())
			}
			rr = httptest.NewRecorder()
			policyRoutes(h).ServeHTTP(rr, mtastsRequest(http.MethodGet, domain.ID, ""))
			if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "No MTA-STS policy found for this domain") {
				t.Fatalf("get after delete = %d %s", rr.Code, rr.Body.String())
			}
		})
	}
	// Ordinary model users still receive the schema defaults.
	domain := models.Domain{Name: "model-default.restmail.test", ServerType: "restmail"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	p := models.MTASTSPolicy{DomainID: domain.ID}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if p.MaxAge != 604800 || !p.Active {
		t.Fatalf("ordinary model defaults changed: %+v", p)
	}
	t.Run("insert_failure_leaves_no_policy", func(t *testing.T) {
		table := db.NamingStrategy.TableName("MTASTSPolicy")
		if err := db.Exec("ALTER TABLE " + table + " ADD CONSTRAINT test_refuse_age CHECK (max_age <> 13)").Error; err != nil {
			t.Fatal(err)
		}
		domain := models.Domain{Name: "failed-insert.restmail.test", ServerType: "restmail"}
		if err := db.Create(&domain).Error; err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		policyRoutes(h).ServeHTTP(rr, mtastsRequest(http.MethodPut, domain.ID, `{"max_age":13,"active":false}`))
		if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), `"code":"internal_error"`) {
			t.Fatalf("insert failure = %d %s, want 500/internal_error", rr.Code, rr.Body.String())
		}
		var count int64
		if err := db.Model(&models.MTASTSPolicy{}).Where("domain_id = ?", domain.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed insert left %d policy rows", count)
		}
	})
}
