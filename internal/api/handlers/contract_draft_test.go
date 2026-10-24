package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

func TestSaveDraft_OptionalSenderSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required []string `yaml:"required"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	schema, ok := doc.Components.Schemas["DraftRequest"]
	if !ok {
		t.Fatal("DraftRequest schema missing")
	}
	if len(schema.Required) != 0 {
		t.Errorf("an empty draft must be valid, but required = %v", schema.Required)
	}
	requests, _ := postExamples(t, "/api/v1/messages/draft", "saveDraft")
	empty, ok := requests["empty"].(map[string]interface{})
	if !ok || len(empty) != 0 {
		t.Fatalf("empty draft example = %v, want {}", requests["empty"])
	}
}

func TestSaveDraft_OptionalSenderExamples(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewMessageHandler(tx, nil, nil, "", nil)
	requests, _ := postExamples(t, "/api/v1/messages/draft", "saveDraft")
	for _, name := range []string{"empty", "with_sender"} {
		t.Run(name, func(t *testing.T) {
			body, ok := requests[name].(map[string]interface{})
			if !ok {
				t.Fatalf("missing object example %s", name)
			}
			// Use this test's isolated mailbox in place of the E2E admin.
			wantSender := ""
			if _, supplied := body["from"]; supplied {
				wantSender = fx.ownerMailbox.Address
				body["from"] = wantSender
			}
			rr := httptest.NewRecorder()
			composeRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/messages/draft",
				fx.ownerMailbox.ID, nil, string(mustJSON(body))))
			if rr.Code != http.StatusCreated {
				t.Fatalf("want 201, got %d: %s", rr.Code, rr.Body)
			}
			var response struct {
				Data models.Message `json:"data"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var stored models.Message
			if err := tx.First(&stored, response.Data.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.MailboxID != fx.ownerMailbox.ID || stored.Folder != "Drafts" || !stored.IsDraft || stored.Sender != wantSender {
				t.Errorf("draft stored in wrong mailbox or with wrong sender: %+v", stored)
			}
			if name == "empty" && (stored.Subject != "" || stored.BodyText != "" || string(stored.RecipientsTo) != "[]" || string(stored.RecipientsCc) != "[]") {
				t.Errorf("empty draft acquired content: %+v", stored)
			}
		})
	}

	// Omitting from does not let a caller select somebody else's mailbox.
	// An explicit address still goes through the mail:write permission check.
	rr := httptest.NewRecorder()
	composeRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/messages/draft",
		fx.attackerMailbox.ID, nil, string(mustJSON(map[string]string{"from": fx.ownerMailbox.Address}))))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("foreign sender: want 403, got %d: %s", rr.Code, rr.Body)
	}
	var count int64
	if err := tx.Model(&models.Message{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("refused draft changed storage: got %d messages, want 2", count)
	}
}
