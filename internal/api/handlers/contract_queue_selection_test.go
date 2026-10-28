package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

// The contract runner used to generate {} and {"ids":[]} as valid selections
// (#388). Keep the served schema aligned with the handlers' selection guard.
func TestBulkQueue_SelectionSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				AnyOf []struct {
					Required   []string `yaml:"required"`
					Properties map[string]struct {
						MinItems int `yaml:"minItems"`
					} `yaml:"properties"`
				} `yaml:"anyOf"`
				Properties map[string]struct {
					MaxItems int `yaml:"maxItems"`
					Items    struct {
						Minimum *int `yaml:"minimum"`
					} `yaml:"items"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["BulkQueueRequest"]
	if len(schema.AnyOf) != 2 {
		t.Fatalf("want two selection alternatives, got %v", schema.AnyOf)
	}
	if !reflect.DeepEqual(schema.AnyOf[0].Required, []string{"ids"}) || schema.AnyOf[0].Properties["ids"].MinItems != 1 ||
		!reflect.DeepEqual(schema.AnyOf[1].Required, []string{"filter"}) {
		t.Errorf("selection must require nonempty IDs or a filter: %+v", schema.AnyOf)
	}
	ids := schema.Properties["ids"]
	if ids.MaxItems != 1000 || ids.Items.Minimum == nil || *ids.Items.Minimum != 0 {
		t.Errorf("IDs must be unsigned and limited to 1000: %+v", ids)
	}
}

// Exercise the same boundary bodies against every handler, including the
// important distinction between empty IDs alone and empty IDs with a filter.
func TestBulkQueue_SelectionBoundaries(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("bulk queue selections require PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	h := NewQueueHandler(tx)
	for _, action := range []string{"retry", "bounce", "delete"} {
		for _, tc := range []struct {
			name, body string
			status     int
		}{
			// A body that selects nothing fails the request type's AnyOf at
			// binding: 422, like any other body the schema refuses.
			{"missing_selection", `{}`, 422},
			{"empty_ids", `{"ids":[]}`, 422},
			{"null_filter", `{"filter":null}`, 422},
			{"negative_id", `{"ids":[-1]}`, 422},
			{"zero_id", `{"ids":[0]}`, 200},
			{"empty_filter", `{"filter":{}}`, 200},
			{"empty_ids_with_filter", `{"ids":[],"filter":{}}`, 200},
			{"ids_with_filter", `{"ids":[0],"filter":{}}`, 200},
			{"1000_ids", bulkSelectionIDs(t, 1000, false), 200},
			{"1001_ids", bulkSelectionIDs(t, 1001, false), 422},
			{"1001_ids_with_filter", bulkSelectionIDs(t, 1001, true), 422},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				method := http.MethodPost
				if action == "delete" {
					method = http.MethodDelete
				}
				rr := httptest.NewRecorder()
				queueRoutes(h).ServeHTTP(rr, httptest.NewRequest(method, "/api/v1/admin/queue/bulk-"+action, strings.NewReader(tc.body)))
				if rr.Code != tc.status {
					t.Fatalf("want %d, got %d: %s", tc.status, rr.Code, rr.Body)
				}
			})
		}
	}
}

func bulkSelectionIDs(t *testing.T, count int, filter bool) string {
	t.Helper()
	body := map[string]interface{}{"ids": make([]uint, count)}
	if filter {
		body["filter"] = map[string]string{}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// An empty filter has no conditions, so it selects the whole queue: the app's
// "delete the entire queue" sends exactly this, after asking the admin.
func TestBulkQueue_EmptyFilterDeletesEveryEntry(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Skipf("bulk queue deletes require PostgreSQL: %v", err)
	}
	if err := db.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	for _, domain := range []string{"a.test", "b.test", "c.test"} {
		if err := tx.Create(&models.OutboundQueue{Sender: "s@x.test", Recipient: "r@" + domain, Domain: domain}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{`{"filter":{}}`, `{"ids":[],"filter":{"status":""}}`} {
		if err := tx.Create(&models.OutboundQueue{Sender: "s@x.test", Recipient: "r@d.test", Domain: "d.test"}).Error; err != nil {
			t.Fatal(err)
		}
		var present int64
		if err := tx.Model(&models.OutboundQueue{}).Count(&present).Error; err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		queueRoutes(NewQueueHandler(tx)).ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/queue/bulk-delete", strings.NewReader(body)))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", body, rr.Code, rr.Body)
		}
		var got struct {
			Data BulkQueueResult `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var left int64
		if err := tx.Model(&models.OutboundQueue{}).Count(&left).Error; err != nil {
			t.Fatal(err)
		}
		if got.Data.Affected != present || left != 0 {
			t.Fatalf("%s: affected %d of %d, %d left; want every entry deleted", body, got.Data.Affected, present, left)
		}
	}
}
