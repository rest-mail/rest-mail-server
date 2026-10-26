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

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

func TestCustomFilterMutationPersistenceAndContext(t *testing.T) {
	db := openCustomFilterCreateScratchDB(t)
	domain := models.Domain{Name: "filter-mutation.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	other := models.Domain{Name: "other-filter-mutation.test"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	h := NewPipelineHandler(db, nil, filters.JSSidecar{})
	serve := func(method, path, body string, cancel bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if cancel {
			ctx, stop := context.WithCancel(r.Context())
			stop()
			r = r.WithContext(ctx)
		}
		rr := httptest.NewRecorder()
		pipelineRoutes(h).ServeHTTP(rr, r)
		return rr
	}
	body := fmt.Sprintf(`{"domain_id":%d,"name":"Fixture","filter_type":"action","direction":"inbound","config":{"script":"noop"},"enabled":false}`, domain.ID)
	rr := serve(http.MethodPost, "/api/v1/admin/custom-filters", body, false)
	if rr.Code != 201 {
		t.Fatalf("create:%d %s", rr.Code, rr.Body)
	}
	var answer struct{ Data CustomFilter }
	if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	var before models.CustomFilter
	if err := db.First(&before, answer.Data.ID).Error; err != nil {
		t.Fatal(err)
	}
	if before.Enabled || before.DomainID != domain.ID {
		t.Fatalf("create lost false/domain:%+v", before)
	}
	patch := fmt.Sprintf(`{"domain_id":%d,"name":"Updated","description":"Changed","filter_type":"transform","direction":"both","config":{"rules":[{"label":"café","active":false}],"arbitrary":null},"enabled":true}`, other.ID)
	rr = serve(http.MethodPatch, fmt.Sprintf("/api/v1/admin/custom-filters/%d", before.ID), patch, false)
	if rr.Code != 200 {
		t.Fatalf("update:%d %s", rr.Code, rr.Body)
	}
	var after models.CustomFilter
	if err := db.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.DomainID != domain.ID || after.Name != "Updated" || after.Description != "Changed" || after.FilterType != "transform" || after.Direction != "both" || !after.Enabled || !after.CreatedAt.Equal(before.CreatedAt) || after.UpdatedAt.Before(before.UpdatedAt) {
		t.Fatalf("update lost fields/domain/time:%+v", after)
	}
	var value map[string]interface{}
	if err := json.Unmarshal(after.Config, &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != 2 || value["arbitrary"] != nil || len(value["rules"].([]interface{})) != 1 {
		t.Fatalf("config changed:%s", after.Config)
	}
	rr = serve(http.MethodPost, "/api/v1/admin/custom-filters", body, true)
	if rr.Code != 500 {
		t.Fatalf("cancelled creation:%d %s", rr.Code, rr.Body)
	}
	var count int64
	if err := db.Model(&models.CustomFilter{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("cancelled create changed rows:%d %v", count, err)
	}
	const callback = "custom_filter_mutation_lookup_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "custom_filters" {
			_ = tx.AddError(errors.New("private filter lookup detail"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	rr = serve(http.MethodPatch, fmt.Sprintf("/api/v1/admin/custom-filters/%d", before.ID), `{"enabled":false}`, false)
	if rr.Code != 500 || strings.Contains(rr.Body.String(), "private") {
		t.Fatalf("lookup failure:%d %s", rr.Code, rr.Body)
	}
	if err := db.Callback().Query().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&after, before.ID).Error; err != nil || !after.Enabled {
		t.Fatalf("failed lookup changed persisted state:%+v %v", after, err)
	}
}
