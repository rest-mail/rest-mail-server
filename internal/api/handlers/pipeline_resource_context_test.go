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

type pipelineResourceProbe struct{ name, method, path, table string }

func pipelineResourceFixture(t *testing.T) (*gorm.DB, []pipelineResourceProbe) {
	t.Helper()
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Pipeline{}, &models.MessageTrace{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "pipeline-resource.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	var firstPipeline, firstFilter uint
	for i := range 2 {
		p := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(`[]`), Active: true}
		cf := models.CustomFilter{DomainID: domain.ID, Name: fmt.Sprintf("filter-%d", i), FilterType: "action", Direction: "inbound", Config: json.RawMessage(`{}`)}
		mid := uint(i + 1)
		trace := models.MessageTrace{MessageID: &mid, PipelineID: 1, Direction: "inbound", FinalAction: "continue", Outcome: "delivered", Stages: json.RawMessage(`[]`)}
		for _, row := range []any{&p, &cf, &trace} {
			if err := db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			firstPipeline, firstFilter = p.ID, cf.ID
		}
	}
	return db, []pipelineResourceProbe{
		{"pipeline_list", "GET", "/api/v1/admin/pipelines?limit=1", "pipelines"},
		{"trace_list", "GET", "/api/v1/admin/pipelines/logs?limit=1", "message_traces"},
		{"trace_get", "GET", "/api/v1/admin/messages/1/trace", "message_traces"},
		{"filter_list", "GET", "/api/v1/admin/custom-filters?limit=1", "custom_filters"},
		{"filter_get", "GET", fmt.Sprintf("/api/v1/admin/custom-filters/%d", firstFilter), "custom_filters"},
		{"pipeline_delete", "DELETE", fmt.Sprintf("/api/v1/admin/pipelines/%d", firstPipeline), "pipelines"},
		{"filter_delete", "DELETE", fmt.Sprintf("/api/v1/admin/custom-filters/%d", firstFilter), "custom_filters"},
	}
}

func pipelineResourceRequest(db *gorm.DB, tc pipelineResourceProbe, ctx context.Context) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(tc.method, tc.path, nil).WithContext(ctx)
	pipelineRoutes(NewPipelineHandler(db, nil, filters.JSSidecar{})).ServeHTTP(rr, req)
	return rr
}

func TestPipelineResourceContextAndCancellation(t *testing.T) {
	for index := range 7 {
		for _, mode := range []string{"active", "cancel_first", "cancel_page"} {
			if mode == "cancel_page" && index != 0 && index != 1 && index != 3 {
				continue
			}
			t.Run(fmt.Sprintf("%d/%s", index, mode), func(t *testing.T) {
				db, cases := pipelineResourceFixture(t)
				tc := cases[index]
				t.Log(tc.name)
				type requestMarker struct{}
				ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), requestMarker{}, "request"), time.Minute)
				defer cancel()
				deadline, _ := ctx.Deadline()
				hits, matched := 0, true
				observe := func(tx *gorm.DB) {
					if tx.Statement.Table != tc.table {
						return
					}
					hits++
					d, ok := tx.Statement.Context.Deadline()
					matched = matched && ok && d.Equal(deadline) && tx.Statement.Context.Value(requestMarker{}) == "request"
					_, counting := tx.Statement.Dest.(*int64)
					if mode == "cancel_first" || mode == "cancel_page" && !counting {
						cancel()
					}
				}
				const callback = "pipeline_resource_context"
				if err := db.Callback().Query().Before("gorm:query").Register(callback, observe); err != nil {
					t.Fatal(err)
				}
				if err := db.Callback().Delete().Before("gorm:delete").Register(callback, observe); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = db.Callback().Query().Remove(callback)
					_ = db.Callback().Delete().Remove(callback)
				})
				rr := pipelineResourceRequest(db, tc, ctx)
				if err := db.Callback().Query().Remove(callback); err != nil {
					t.Fatal(err)
				}
				if err := db.Callback().Delete().Remove(callback); err != nil {
					t.Fatal(err)
				}
				if hits == 0 || !matched {
					t.Errorf("%s SQL lost request values/deadline (observed %d calls)", tc.name, hits)
				}
				want := http.StatusOK
				if mode != "active" {
					want = http.StatusInternalServerError
				}
				if rr.Code != want || strings.Contains(rr.Body.String(), "context canceled") {
					t.Errorf("%s: got %d %s, want sanitized %d", tc.name, rr.Code, rr.Body, want)
				}
				if tc.method == http.MethodDelete && mode != "active" {
					var count int64
					if err := db.Table(tc.table).Count(&count).Error; err != nil || count != 2 {
						t.Fatalf("cancelled delete changed rows: %d %v", count, err)
					}
					if retry := pipelineResourceRequest(db, tc, t.Context()); retry.Code != 200 {
						t.Fatalf("delete retry: %d %s", retry.Code, retry.Body)
					}
					if retry := pipelineResourceRequest(db, tc, t.Context()); retry.Code != 404 {
						t.Fatalf("missing delete: %d %s", retry.Code, retry.Body)
					}
				}
			})
		}
	}
}

func TestPipelineResourceCountFailuresAndPagination(t *testing.T) {
	for _, index := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			db, cases := pipelineResourceFixture(t)
			tc := cases[index]
			reached := false
			const callback = "pipeline_resource_count_failure"
			if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if _, count := tx.Statement.Dest.(*int64); count && tx.Statement.Table == tc.table {
					reached = true
					_ = tx.AddError(errors.New("private count diagnostic"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
			rr := pipelineResourceRequest(db, tc, t.Context())
			if err := db.Callback().Query().Remove(callback); err != nil {
				t.Fatal(err)
			}
			if !reached || rr.Code != 500 || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("count failure: %d %s, reached=%t", rr.Code, rr.Body, reached)
			}
			for offset := range 2 {
				page := tc
				page.path += fmt.Sprintf("&offset=%d", offset)
				rr = pipelineResourceRequest(db, page, t.Context())
				var response struct {
					Data       []json.RawMessage
					Pagination struct {
						Total   int64
						HasMore bool `json:"has_more"`
					}
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if rr.Code != 200 || len(response.Data) != 1 || response.Pagination.Total != 2 || response.Pagination.HasMore != (offset == 0) {
					t.Fatalf("pagination changed: %d %s", rr.Code, rr.Body)
				}
			}
		})
	}
}

// A real PostgreSQL row lock proves cancellation interrupts SQL already
// waiting in the server, rather than only rejecting work before dispatch.
func TestPipelineResourceDeleteLockCancellation(t *testing.T) {
	for _, index := range []int{5, 6} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			db, cases := pipelineResourceFixture(t)
			tc := cases[index]
			lock := db.Begin()
			if lock.Error != nil {
				t.Fatal(lock.Error)
			}
			t.Cleanup(func() { _ = lock.Rollback().Error })
			if err := lock.Exec("SELECT id FROM " + tc.table + " WHERE id = 1 FOR UPDATE").Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- pipelineResourceRequest(db, tc, ctx) }()
			// The database name is the verified private scratch database.
			// Wait until this DELETE is actually blocked on our row lock.
			until := time.Now().Add(3 * time.Second)
			for {
				var waiting int64
				err := db.Raw(`SELECT count(*) FROM pg_stat_activity
WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE ?`, `DELETE FROM "`+tc.table+`"%`).Scan(&waiting).Error
				if err != nil {
					t.Fatal(err)
				}
				if waiting > 0 {
					break
				}
				if time.Now().After(until) {
					t.Fatal("DELETE did not reach the held row lock")
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			var rr *httptest.ResponseRecorder
			select {
			case rr = <-done:
			case <-time.After(time.Second):
				t.Error("cancelled request kept waiting for the row lock")
				_ = lock.Rollback().Error
				select {
				case rr = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("DELETE did not finish after releasing its backstop lock")
				}
			}
			if rr.Code != 500 || strings.Contains(rr.Body.String(), "context canceled") {
				t.Errorf("cancelled locked DELETE: %d %s", rr.Code, rr.Body)
			}
			_ = lock.Rollback().Error
			var remaining int64
			if err := db.Table(tc.table).Count(&remaining).Error; err != nil || remaining != 2 {
				t.Fatalf("cancelled locked DELETE changed rows: %d %v", remaining, err)
			}
			if retry := pipelineResourceRequest(db, tc, t.Context()); retry.Code != 200 {
				t.Fatalf("retry: %d %s", retry.Code, retry.Body)
			}
		})
	}
}
