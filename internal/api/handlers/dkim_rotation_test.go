package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/db/models"
)

func rotationRequest(method, id, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/admin/dkim/"+id+"/rotation", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestDKIMRotationSettings_PersistWithoutChangingSigningMaterial(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	installed := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	domain := models.Domain{Name: "rotation-settings.test", DKIMSelector: "mail", DKIMPrivateKey: "private-sentinel", DKIMCreatedAt: &installed}
	other := models.Domain{Name: "other-rotation.test", DKIMRotationIntervalDays: 180}
	for _, d := range []*models.Domain{&domain, &other} {
		if err := tx.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := NewDKIMHandler(tx, "")
	id := fmt.Sprint(domain.ID)
	checkResponse := func(rr *httptest.ResponseRecorder, interval int) {
		t.Helper()
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		var response struct {
			Data map[string]int `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) != 2 || response.Data["domain_id"] != int(domain.ID) || response.Data["rotation_interval_days"] != interval {
			t.Fatalf("unexpected public settings: %s", rr.Body.String())
		}
	}
	rr := httptest.NewRecorder()
	dkimRoutes(h).ServeHTTP(rr, rotationRequest(http.MethodGet, id, ""))
	checkResponse(rr, 0)
	for _, days := range []int{1, 90, models.MaxDKIMRotationIntervalDays, 0} {
		rr = httptest.NewRecorder()
		dkimRoutes(h).ServeHTTP(rr, rotationRequest(http.MethodPut, id, fmt.Sprintf(`{"rotation_interval_days":%d}`, days)))
		checkResponse(rr, days)
		rr = httptest.NewRecorder()
		dkimRoutes(NewDKIMHandler(tx, "")).ServeHTTP(rr, rotationRequest(http.MethodGet, id, ""))
		checkResponse(rr, days)
		var after models.Domain
		if err := tx.First(&after, domain.ID).Error; err != nil {
			t.Fatal(err)
		}
		if after.DKIMRotationIntervalDays != days || after.DKIMSelector != domain.DKIMSelector ||
			after.DKIMPrivateKey != domain.DKIMPrivateKey || after.DKIMCreatedAt == nil || !after.DKIMCreatedAt.Equal(installed) {
			t.Fatal("settings update lost its interval or changed signing material")
		}
	}
	var unchanged models.Domain
	if err := tx.First(&unchanged, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.DKIMRotationIntervalDays != 180 {
		t.Fatal("update changed another domain's interval")
	}
}

func TestDKIMRotationSettings_InvalidBodyDoesNotChangeInterval(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "invalid-rotation.test", DKIMRotationIntervalDays: 90}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	h := NewDKIMHandler(tx, "")
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{}`, 422}, {`null`, 400}, {`{"rotation_interval_days":null}`, 422},
		{`{"rotation_interval_days":-1}`, 422}, {`{"rotation_interval_days":3651}`, 422},
		{`{"rotation_interval_days":"90"}`, 422}, {`{"rotation_interval_days":1.5}`, 422},
		{`{"rotation_interval_days":9223372036854775808}`, 422},
		{`{"rotation_interval_days":90,"private_key":"injected"}`, 422},
		{`{"rotation_interval_days":90} {}`, 400}, {`{"rotation_interval_days":90} trailing`, 400},
		{`[]`, 400}, {`{`, 400}, {``, 400},
	} {
		t.Run(tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			dkimRoutes(h).ServeHTTP(rr, rotationRequest(http.MethodPut, fmt.Sprint(domain.ID), tc.body))
			if rr.Code != tc.code {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.code, rr.Body.String())
			}
			if tc.code == 422 && !strings.Contains(rr.Body.String(), `"fields"`) {
				t.Fatalf("validation does not identify field: %s", rr.Body.String())
			}
			var after models.Domain
			if err := tx.First(&after, domain.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.DKIMRotationIntervalDays != 90 {
				t.Fatal("invalid request changed the interval")
			}
		})
	}
}

func TestDKIMRotationSettings_DomainWithoutKeyAndMissingDomain(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: "rotation-no-key.test"}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	h := NewDKIMHandler(tx, "")
	rr := httptest.NewRecorder()
	dkimRoutes(h).ServeHTTP(rr, rotationRequest(http.MethodPut, fmt.Sprint(domain.ID), `{"rotation_interval_days":30}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("keyless domain status %d: %s", rr.Code, rr.Body.String())
	}
	if err := tx.Delete(&domain).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		code int
	}{{fmt.Sprint(domain.ID), 404}, {"0", 422}, {"nope", 422}, {"-1", 422}, {"4294967296", 422}} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			rr := httptest.NewRecorder()
			req := rotationRequest(method, tc.id, `{"rotation_interval_days":30}`)
			if method == http.MethodGet {
				dkimRoutes(h).ServeHTTP(rr, req)
			} else {
				dkimRoutes(h).ServeHTTP(rr, req)
			}
			if rr.Code != tc.code {
				t.Fatalf("%s id=%s: status %d, want %d: %s", method, tc.id, rr.Code, tc.code, rr.Body.String())
			}
		}
	}
}

func TestDKIMRotationSettings_DatabaseRejectsInvalidInterval(t *testing.T) {
	db := openAuthzTestDB(t)
	for _, days := range []int{-1, models.MaxDKIMRotationIntervalDays + 1} {
		t.Run(fmt.Sprint(days), func(t *testing.T) {
			tx := db.Begin()
			t.Cleanup(func() { tx.Rollback() })
			// A direct write must enforce the same range as the HTTP endpoint.
			err := tx.Exec("INSERT INTO domains (name, dkim_rotation_interval_days) VALUES (?, ?)", "invalid-direct-rotation.test", days).Error
			if err == nil || !strings.Contains(err.Error(), "dkim_rotation_interval_range") {
				t.Fatalf("direct write should fail the interval constraint, got %v", err)
			}
		})
	}
}
