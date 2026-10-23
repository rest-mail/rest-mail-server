package handlers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	yaml "go.yaml.in/yaml/v2"
)

// patchExamples reads the named application/json examples of the PATCH on
// path, as postExamples does for a POST.
func patchExamples(t *testing.T, path, operationID string) (map[string]interface{}, map[string]map[string]interface{}) {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths map[string]struct {
			Patch struct {
				OperationID string `yaml:"operationId"`
				RequestBody struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"requestBody"`
				Responses map[string]struct {
					Content map[string]namedExamples `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"patch"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	op := doc.Paths[path].Patch
	if op.OperationID != operationID {
		t.Fatalf("PATCH %s is %q, want %s", path, op.OperationID, operationID)
	}
	requests := map[string]interface{}{}
	for name, example := range op.RequestBody.Content["application/json"].Examples {
		requests[name] = jsonValue(t, example.Value)
	}
	responses := map[string]map[string]interface{}{}
	for status, response := range op.Responses {
		for name, example := range response.Content["application/json"].Examples {
			if responses[status] == nil {
				responses[status] = map[string]interface{}{}
			}
			responses[status][name] = jsonValue(t, example.Value)
		}
	}
	return requests, responses
}

// Each request example names exactly one response, and updated is the only
// one the 200 answers: an unnamed 200 would pair with password_short too.
func TestMailboxUpdate_ExamplePairs(t *testing.T) {
	requests, responses := patchExamples(t, "/api/v1/admin/mailboxes/{id}", "updateMailbox")
	for name := range requests {
		var statuses []string
		for status, examples := range responses {
			if _, ok := examples[name]; ok {
				statuses = append(statuses, status)
			}
		}
		if len(statuses) != 1 {
			t.Errorf("request example %q is named on responses %v, want exactly one", name, statuses)
		}
	}
	if _, ok := responses["200"]["updated"]; !ok || len(responses["200"]) != 1 {
		t.Errorf("200 examples = %v, want only updated", responses["200"])
	}
	if _, ok := responses["422"]["password_short"]; !ok || len(responses["422"]) != 1 {
		t.Errorf("422 examples = %v, want only password_short", responses["422"])
	}
	short, _ := requests["password_short"].(map[string]interface{})
	if password, _ := short["password"].(string); password == "" || len(password) >= minChosenPasswordLength {
		t.Errorf("password_short password = %q, want 1 to %d characters", password, minChosenPasswordLength-1)
	}
}

// The spec's requests, sent to the real handler for an account that exists
// (the contract run's comes from createAccount's link): updated is answered
// 200, and password_short gets exactly its documented 422 and changes nothing.
func TestMailboxUpdate_DocumentedExamples(t *testing.T) {
	db := openAuditTestDB(t)
	var database string
	if err := db.Raw("SELECT current_database()").Scan(&database).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(database, "restmail_auditapi_") {
		t.Fatalf("refusing to write to non-scratch database %q", database)
	}
	if err := db.AutoMigrate(accountModels...); err != nil {
		t.Fatal(err)
	}
	h := NewMailboxHandler(db, nil, nil)
	requests, responses := patchExamples(t, "/api/v1/admin/mailboxes/{id}", "updateMailbox")

	t.Run("updated", func(t *testing.T) {
		mb := seedMailbox(t, db, "example-updated")
		rr := mailboxUpdate(t, h, mb.ID, string(mustJSON(requests["updated"])))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
		}
		var stored models.Mailbox
		if err := db.First(&stored, mb.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.DisplayName != "Contract Test" || stored.QuotaBytes != 1073741824 || !stored.Active {
			t.Errorf("updated not applied: %+v", stored)
		}
	})

	t.Run("password_short", func(t *testing.T) {
		mb := seedMailbox(t, db, "example-password-short")
		// Compare persisted timestamps: PostgreSQL rounds Go's nanoseconds.
		if err := db.First(&mb, mb.ID).Error; err != nil {
			t.Fatal(err)
		}
		rr := mailboxUpdate(t, h, mb.ID, string(mustJSON(requests["password_short"])))
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
		}
		var got interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if want := responses["422"]["password_short"]; !reflect.DeepEqual(got, want) {
			t.Errorf("body = %s, want the documented %s", rr.Body, mustJSON(want))
		}
		var stored models.Mailbox
		if err := db.First(&stored, mb.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.DisplayName != mb.DisplayName || stored.CredentialEpoch != mb.CredentialEpoch || !stored.UpdatedAt.Equal(mb.UpdatedAt) {
			t.Errorf("refused update changed the account: %+v", stored)
		}
		if credential, err := repositories.NewCredentialRepository(db).MatchPassword(mb.ID, "password123", time.Now()); err != nil || credential == nil {
			t.Fatalf("refused update changed the password: %v", err)
		}
	})
}
