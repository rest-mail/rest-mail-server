package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

const rotationContractPath = "/api/v1/admin/dkim/{id}/rotation"

func TestSetDKIMRotation_ExamplePairs(t *testing.T) {
	requests, responses := operationExamples(t, rotationContractPath, "put", "setDKIMRotation")
	want := map[string][]string{
		"request": {"body_array", "configured", "field_unknown", "interval_missing", "interval_negative", "interval_null", "interval_too_large", "interval_wrong_type"},
		"200":     {"configured"},
		"422":     {"body_array", "field_unknown", "interval_missing", "interval_negative", "interval_null", "interval_too_large", "interval_wrong_type"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("setDKIMRotation pairs = %v, want %v", got, want)
	}
	for name, body := range map[string]string{
		"configured":          `{"rotation_interval_days":90}`,
		"body_array":          `[]`,
		"field_unknown":       `{"rotation_interval_days":90,"private_key":"injected"}`,
		"interval_wrong_type": `{"rotation_interval_days":"90"}`,
		"interval_missing":    `{}`,
		"interval_null":       `{"rotation_interval_days":null}`,
		"interval_negative":   `{"rotation_interval_days":-1}`,
		"interval_too_large":  `{"rotation_interval_days":3651}`,
	} {
		if !reflect.DeepEqual(requests[name], parseShapeJSON(t, body)) {
			t.Errorf("%s request = %s, want %s", name, mustJSON(requests[name]), body)
		}
	}
}

// Run every documented pair against the real handler and a real domain. The
// refusals must preserve all domain fields both before and after configuration.
func TestSetDKIMRotation_DocumentedExamples(t *testing.T) {
	requests, responses := operationExamples(t, rotationContractPath, "put", "setDKIMRotation")
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("DKIM rotation contract test requires PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.DKIMRotationKey{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	installed := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	domain := models.Domain{
		Name: "contract-rotation.test", DKIMRotationIntervalDays: 180,
		DKIMSelector: "mail", DKIMPrivateKey: "signing-material-sentinel", DKIMCreatedAt: &installed,
	}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	h := NewDKIMHandler(tx, "")
	id := fmt.Sprint(domain.ID)
	load := func(t *testing.T) models.Domain {
		t.Helper()
		var saved models.Domain
		if err := tx.First(&saved, domain.ID).Error; err != nil {
			t.Fatal(err)
		}
		return saved
	}
	call := func(t *testing.T, name string, status int, expected interface{}) {
		t.Helper()
		rr := httptest.NewRecorder()
		dkimRoutes(h).ServeHTTP(rr, rotationRequest(http.MethodPut, id, string(mustJSON(requests[name]))))
		if rr.Code != status {
			t.Fatalf("%s: got %d %s, want %d", name, rr.Code, rr.Body, status)
		}
		if rr.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: unexpected content type %q", name, rr.Header().Get("Content-Type"))
		}
		if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, expected) {
			t.Fatalf("%s: got %s, want %s", name, rr.Body, mustJSON(expected))
		}
	}
	refusals := func(stage string) {
		t.Helper()
		before := load(t)
		for _, status := range []int{400, 422} {
			for name, expected := range responses[fmt.Sprint(status)] {
				t.Run(stage+"/"+name, func(t *testing.T) {
					call(t, name, status, expected)
					if after := load(t); !reflect.DeepEqual(after, before) {
						t.Fatalf("%s changed the saved domain", name)
					}
				})
			}
		}
	}
	refusals("before_configuration")
	expected := jsonValue(t, responses["200"]["configured"])
	expected.(map[string]interface{})["data"].(map[string]interface{})["domain_id"] = float64(domain.ID)
	before := load(t)
	call(t, "configured", http.StatusOK, expected)
	after := load(t)
	if after.DKIMRotationIntervalDays != 90 {
		t.Fatalf("saved interval = %d, want 90", after.DKIMRotationIntervalDays)
	}
	// A successful policy write changes only the interval and update timestamp.
	after.DKIMRotationIntervalDays = before.DKIMRotationIntervalDays
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(after, before) {
		t.Fatal("configuration changed domain fields beyond its interval")
	}
	refusals("after_configuration")
}
