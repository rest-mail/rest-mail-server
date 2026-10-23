package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/db/models"
)

// Deleting a pipeline or a custom filter that is not there is a 404 not_found, as for every
// other admin delete; one that is there is deleted, and answered 200 "deleted". Needs the
// unit-test Postgres; skips when none is reachable.
func TestPipelineAndCustomFilterDelete_MissingIsNotFound(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.Pipeline{}, &models.CustomFilter{}); err != nil {
		t.Skipf("pipeline delete test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	h := &PipelineHandler{db: tx}

	del := func(handler http.HandlerFunc, path string, id uint) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("%s/%d", path, id), nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprint(id))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr
	}

	domain := models.Domain{Name: "pipeline-delete.test", ServerType: "traditional"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	pipe := models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: []byte("[]"), Active: true}
	if err := tx.Create(&pipe).Error; err != nil {
		t.Fatal(err)
	}
	filter := models.CustomFilter{DomainID: domain.ID, Name: "delete-me", FilterType: "action", Direction: "inbound", Config: []byte("{}")}
	if err := tx.Create(&filter).Error; err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name    string
		handler http.HandlerFunc
		path    string
		id      uint
		message string
	}{
		{"pipeline", pipelineRoutes(h).ServeHTTP, "/api/v1/admin/pipelines", pipe.ID, "Pipeline not found"},
		{"custom filter", pipelineRoutes(h).ServeHTTP, "/api/v1/admin/custom-filters", filter.ID, "Custom filter not found"},
	} {
		if rr := del(c.handler, c.path, c.id); rr.Code != http.StatusOK || errorCodeOf(rr) != "" {
			t.Fatalf("%s: deleting one that is there: %d %s", c.name, rr.Code, rr.Body)
		}
		rr := del(c.handler, c.path, c.id)
		if rr.Code != http.StatusNotFound || errorCodeOf(rr) != "not_found" || !strings.Contains(rr.Body.String(), c.message) {
			t.Fatalf("%s: deleting it again: %d %s, want 404 not_found %q", c.name, rr.Code, rr.Body, c.message)
		}
	}
}
