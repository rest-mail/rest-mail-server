package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

const importContactsPath = "/api/v1/accounts/{id}/contacts/import"

func TestImportContacts_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, importContactsPath, "importContacts")
	want := map[string][]string{
		"200": {"imported"},
		"422": {"body_array", "contacts_empty", "contacts_missing", "email_invalid", "email_missing", "mixed_batch"},
	}
	if len(requests) != 7 || len(responses) != len(want) {
		t.Fatalf("unexpected pairs: requests %v, responses %v", names(requests), responses)
	}
	for status, expected := range want {
		if got := names(responses[status]); !reflect.DeepEqual(got, expected) {
			t.Errorf("%s examples = %v, want %v", status, got, expected)
		}
		for _, name := range expected {
			if _, ok := requests[name]; !ok {
				t.Errorf("%s has no request", name)
			}
		}
	}
	if !reflect.DeepEqual(requests["body_array"], []interface{}{}) {
		t.Errorf("body_array = %v, want []", requests["body_array"])
	}
}

// Run every pair against the real handler. Rejecting a later entry must not
// import the valid first entry, nor change any contact already in the account.
func TestImportContacts_DocumentedExamples(t *testing.T) {
	db := openAuditTestDB(t)
	if err := db.AutoMigrate(append(accountModels, &models.Contact{})...); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_contacts_mailbox_email ON contacts(mailbox_id, email)`).Error; err != nil {
		t.Fatal(err)
	}
	ensureDefaultServerCeiling(t, db)
	mailbox := seedMailbox(t, db, "import-examples")
	existing := models.Contact{MailboxID: mailbox.ID, Email: "existing@example.org", Name: "Keep me", TrustLevel: "blocked", Source: "manual"}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	readContacts := func(t *testing.T) []models.Contact {
		t.Helper()
		var contacts []models.Contact
		if err := db.Where("mailbox_id = ?", mailbox.ID).Order("id").Find(&contacts).Error; err != nil {
			t.Fatal(err)
		}
		return contacts
	}
	requests, responses := postExamples(t, importContactsPath, "importContacts")
	routes := contactRoutes(NewContactHandler(db))
	send := func(t *testing.T, name, status string) {
		t.Helper()
		body, err := json.Marshal(requests[name])
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, authzRequest(http.MethodPost, contactsPathOf(mailbox.ID, "/import"), mailbox.ID,
			map[string]string{"id": uintToStr(mailbox.ID)}, string(body)))
		wantStatus, err := strconv.Atoi(status)
		if err != nil {
			t.Fatal(err)
		}
		if rr.Code != wantStatus {
			t.Fatalf("%s: status %d, want %s: %s", name, rr.Code, status, rr.Body)
		}
		var got interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, responses[status][name]) {
			t.Errorf("%s: response %s, want %s", name, rr.Body, mustJSON(responses[status][name]))
		}
	}
	for _, phase := range []string{"before_import", "after_import"} {
		if phase == "after_import" {
			send(t, "imported", "200")
			contacts := readContacts(t)
			if len(contacts) != 2 || contacts[1].Email != "carol@example.org" || contacts[1].Name != "Carol" || contacts[1].TrustLevel != "auto" || contacts[1].Source != "import" {
				t.Fatalf("successful import stored unexpected contacts: %+v", contacts)
			}
		}
		before := readContacts(t)
		for _, status := range []string{"400", "422"} {
			for _, name := range names(responses[status]) {
				t.Run(phase+"/"+name, func(t *testing.T) {
					send(t, name, status)
					if got := readContacts(t); !reflect.DeepEqual(got, before) {
						t.Errorf("refusal changed contacts: got %+v, want %+v", got, before)
					}
				})
			}
		}
	}
}
