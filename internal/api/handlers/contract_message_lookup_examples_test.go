package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
	"gorm.io/gorm"
)

// Missing IDs must provoke the documented refusal without changing the stored
// queue entry or delivered-message trace used by the successful lookup.
func TestMessageLookups_DocumentedMissingExamples(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]struct {
			Get struct {
				OperationID string `yaml:"operationId"`
				Parameters  []struct {
					Name     string `yaml:"name"`
					In       string `yaml:"in"`
					Required bool   `yaml:"required"`
					Examples map[string]struct {
						Value any `yaml:"value"`
					} `yaml:"examples"`
				} `yaml:"parameters"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"get"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, operation, message string
		routes                   func(*gorm.DB) http.Handler
	}{
		{"/api/v1/admin/queue/{id}", "getQueueEntry", "Queue entry not found", func(db *gorm.DB) http.Handler {
			return queueRoutes(NewQueueHandler(db))
		}},
		{"/api/v1/admin/messages/{id}/trace", "getMessageTrace", "No pipeline trace for this message", func(db *gorm.DB) http.Handler {
			return pipelineRoutes(NewPipelineHandler(db, nil, filters.JSSidecar{}))
		}},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			op := doc.Paths[tc.path].Get
			if op.OperationID != tc.operation || len(op.Parameters) != 1 {
				t.Fatalf("unexpected operation or parameters: %+v", op)
			}
			parameter := op.Parameters[0]
			if parameter.Name != "id" || parameter.In != "path" || !parameter.Required || len(parameter.Examples) != 2 {
				t.Fatalf("unexpected ID parameter: %+v", parameter)
			}
			missing := contractPathExampleID(t, parameter.Examples["missing"].Value)
			if missing != 2147483647 || contractPathExampleID(t, parameter.Examples["found"].Value) != 1 {
				t.Fatalf("unexpected path examples: %+v", parameter.Examples)
			}
			examples := op.Responses["404"].Content["application/json"].Examples
			want := map[string]interface{}{"error": map[string]interface{}{"code": "not_found", "message": tc.message}}
			if len(examples) != 1 || !reflect.DeepEqual(jsonValue(t, examples["missing"].Value), want) {
				t.Fatalf("unexpected 404 examples: %+v", examples)
			}

			t.Run("PostgreSQL", func(t *testing.T) {
				gdb, err := connectTestDB()
				if err != nil {
					t.Skipf("message lookup contract test needs PostgreSQL: %v", err)
				}
				if err := gdb.AutoMigrate(&models.OutboundQueue{}, &models.MessageTrace{}); err != nil {
					t.Fatal(err)
				}
				tx := gdb.Begin()
				if tx.Error != nil {
					t.Fatal(tx.Error)
				}
				t.Cleanup(func() { tx.Rollback() })
				var record interface{}
				var lookupID, storedID uint
				column := "id"
				if tc.operation == "getQueueEntry" {
					item := &models.OutboundQueue{
						Sender: "sender@restmail.test", Recipient: "recipient@example.test", Domain: "example.test",
						Status: "deferred", RawMessage: "Subject: contract lookup\r\n\r\nbody\r\n", BodyType: "8BITMIME",
						NextAttempt: time.Now().Add(24 * time.Hour), ExpiresAt: time.Now().Add(48 * time.Hour),
					}
					if err := tx.Create(item).Error; err != nil {
						t.Fatal(err)
					}
					record, lookupID, storedID = item, item.ID, item.ID
				} else {
					// MessageID is a nullable indexed correlation column, not a GORM
					// association. Use a distinct delivered ID beside the missing one.
					mid := missing - 1
					var count int64
					if err := tx.Model(&models.MessageTrace{}).Where("message_id = ?", mid).Count(&count).Error; err != nil || count != 0 {
						t.Fatalf("fixture message ID must be unused: count %d, error %v", count, err)
					}
					trace := &models.MessageTrace{MessageID: &mid, RFCMessageID: "<contract-lookup@example.test>", Direction: "inbound", Outcome: outcomeDelivered, FinalAction: "continue", Stages: json.RawMessage(`[]`)}
					if err := tx.Create(trace).Error; err != nil {
						t.Fatal(err)
					}
					record, lookupID, storedID, column = trace, mid, trace.ID, "message_id"
				}
				// Snapshot PostgreSQL's stored timestamps and JSONB representation.
				if err := tx.First(record, storedID).Error; err != nil {
					t.Fatal(err)
				}
				snapshot := jsonValue(t, record)
				model := reflect.New(reflect.TypeOf(record).Elem()).Interface()
				var count int64
				if err := tx.Model(model).Where(column+" = ?", missing).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("missing ID must not exist: count %d, error %v", count, err)
				}
				routes := tc.routes(tx)
				lookup := func(id uint) *httptest.ResponseRecorder {
					rr := httptest.NewRecorder()
					routes.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, strings.ReplaceAll(tc.path, "{id}", fmt.Sprint(id)), nil))
					return rr
				}
				rr := lookup(missing)
				if rr.Code != http.StatusNotFound || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
					t.Fatalf("missing ID answered %d %s, want 404 %s", rr.Code, rr.Body, mustJSON(want))
				}
				rr = lookup(lookupID)
				wantSuccess := map[string]interface{}{"data": snapshot}
				if rr.Code != http.StatusOK || !reflect.DeepEqual(decodeShapeBody(t, rr), wantSuccess) {
					t.Fatalf("created ID answered %d %s, want 200 %s", rr.Code, rr.Body, mustJSON(wantSuccess))
				}
				stored := reflect.New(reflect.TypeOf(record).Elem()).Interface()
				if err := tx.First(stored, storedID).Error; err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(stored, record) {
					t.Fatal("lookup changed the stored record")
				}
				if err := tx.Model(model).Where(column+" = ?", missing).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("lookup created missing ID: count %d, error %v", count, err)
				}
			})
		})
	}
}
