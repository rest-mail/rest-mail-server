package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

func TestImportContacts_ContractSchema(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					MinItems int `yaml:"minItems"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
		Paths map[string]struct {
			Post struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema struct {
							Ref string `yaml:"$ref"`
						} `yaml:"schema"`
					} `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Components.Schemas["ImportContactsRequest"].Properties["contacts"].MinItems; got != 1 {
		t.Errorf("contacts minItems = %d, want 1", got)
	}
	if got := doc.Paths["/api/v1/accounts/{id}/contacts/import"].Post.Responses["422"].Content["application/json"].Schema.Ref; got != "#/components/schemas/ValidationErrorResponse" {
		t.Errorf("422 response = %q, want ValidationErrorResponse", got)
	}
}

func TestImportContacts_ContractBoundaries(t *testing.T) {
	// A private database lets this exercise the production unique index and
	// duplicate handling without modifying contacts shared by other tests.
	db := openAuditTestDB(t)
	if err := db.AutoMigrate(append(accountModels, &models.Contact{})...); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_contacts_mailbox_email ON contacts(mailbox_id, email)`).Error; err != nil {
		t.Fatal(err)
	}
	ensureDefaultServerCeiling(t, db)
	mailbox := seedMailbox(t, db, "contact-import")
	existing := models.Contact{MailboxID: mailbox.ID, Email: "existing@example.com", Name: "Keep me", TrustLevel: "blocked", Source: "manual"}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&existing, existing.ID).Error; err != nil {
		t.Fatal(err)
	}
	routes := contactRoutes(NewContactHandler(db))
	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, authzRequest(http.MethodPost, contactsPathOf(mailbox.ID, "/import"), mailbox.ID,
			map[string]string{"id": uintToStr(mailbox.ID)}, body))
		return rr
	}
	for _, tc := range []struct {
		name, body, field string
	}{
		{"missing_contacts", `{}`, "contacts"},
		{"null_contacts", `{"contacts":null}`, "contacts"},
		{"empty_contacts", `{"contacts":[]}`, "contacts"},
		{"missing_email", `{"contacts":[{}]}`, "contacts[0].email"},
		{"null_email", `{"contacts":[{"email":null}]}`, "contacts[0].email"},
		{"empty_email", `{"contacts":[{"email":""}]}`, "contacts[0].email"},
		{"invalid_email", `{"contacts":[{"email":"not-a-email"}]}`, "contacts[0].email"},
		{"display_address", `{"contacts":[{"email":"Alice <alice@example.com>"}]}`, "contacts[0].email"},
		{"mixed_batch", `{"contacts":[{"email":"valid@example.com"},{"email":"invalid"}]}`, "contacts[1].email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := post(tc.body)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
			}
			var response struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Code != "validation_failed" || response.Error.Details.Fields[tc.field] == "" {
				t.Errorf("missing %s field error: %s", tc.field, rr.Body)
			}
			var contacts []models.Contact
			if err := db.Where("mailbox_id = ?", mailbox.ID).Find(&contacts).Error; err != nil {
				t.Fatal(err)
			}
			if len(contacts) != 1 || !reflect.DeepEqual(contacts[0], existing) {
				t.Errorf("rejected batch changed contacts: %+v", contacts)
			}
		})
	}
	for _, body := range []string{
		`{"contacts":[{"email":"alice@example.com"}]}`,
		`{"contacts":[{"email":"existing@example.com","name":"Overwrite"},{"email":"bob@example.com","name":"Bob"}]}`,
	} {
		rr := post(body)
		if rr.Code != http.StatusOK || rr.Body.String() != "{\"data\":{\"imported\":1}}\n" {
			t.Fatalf("valid batch: status %d, body %s", rr.Code, rr.Body)
		}
	}
	var contacts []models.Contact
	if err := db.Where("mailbox_id = ?", mailbox.ID).Order("id").Find(&contacts).Error; err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 3 || !reflect.DeepEqual(contacts[0], existing) {
		t.Fatalf("import count or existing contact changed: %+v", contacts)
	}
	for i, want := range []struct{ email, name string }{{"alice@example.com", ""}, {"bob@example.com", "Bob"}} {
		got := contacts[i+1]
		if got.Email != want.email || got.Name != want.name || got.TrustLevel != "auto" || got.Source != "import" {
			t.Errorf("imported contact = %+v, want %+v with auto trust and import source", got, want)
		}
	}
}
