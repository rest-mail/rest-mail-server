package handlers

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// createContact's email_missing, email_invalid and trust_level_invalid
// requests (#386) are each refused by the real handler, for the account's own
// session, with exactly their documented 422, and nothing is stored. The
// account check reads the database, so this test needs one.
func TestCreateContact_DocumentedRefusals(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.Contact{}); err != nil {
		t.Fatal(err)
	}
	fx := seedMsgDeleteFixture(t, db, 0)
	t.Cleanup(func() {
		db.Where("mailbox_id = ?", fx.mailbox.ID).Delete(&models.Contact{})
		db.Delete(&fx.mailbox)
		db.Delete(&models.Domain{}, fx.mailbox.DomainID)
	})

	routes := contactRoutes(NewContactHandler(db))
	forAccount := func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path, r.URL.RawPath = contactsPathOf(fx.mailbox.ID, ""), ""
		ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox.ID})
		routes.ServeHTTP(w, r.WithContext(ctx))
	}
	documentedRefusals(t, "/api/v1/accounts/{id}/contacts", "createContact", forAccount)

	var stored int64
	if err := db.Model(&models.Contact{}).Where("mailbox_id = ?", fx.mailbox.ID).Count(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Errorf("the refused requests stored %d contacts, want none", stored)
	}
}

// The pairs vertrag compiles for createContact: created with the 201, each
// refusal with the 422. Each refused request is a valid contact at another
// address with one field missing or changed, so it is refused for that field
// alone. The address differs from created's because the
// createContact hook removes a contact made earlier at the same address.
func TestCreateContact_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/accounts/{id}/contacts", "createContact")

	want := map[string][]string{
		"request": {"created", "email_invalid", "email_missing", "trust_level_invalid"},
		"201":     {"created"},
		"422":     {"email_invalid", "email_missing", "trust_level_invalid"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("createContact's named examples are %v, want %v", got, want)
	}

	created := map[string]interface{}{
		"email":       "someone@example.com",
		"name":        "Someone",
		"trust_level": "trusted",
	}
	if !reflect.DeepEqual(requests["created"], created) {
		t.Errorf("created is %s, want the valid contact %s", mustJSON(requests["created"]), mustJSON(created))
	}
	data := responses["201"]["created"].(map[string]interface{})["data"].(map[string]interface{})
	for field, value := range created {
		if data[field] != value {
			t.Errorf("the 201's %s is %v, want the created request's %v", field, data[field], value)
		}
	}

	refused := map[string]interface{}{
		"email":       "refused@example.com",
		"name":        "Someone",
		"trust_level": "trusted",
	}
	for name, change := range map[string]struct {
		field string
		value interface{} // nil: the field is left out
	}{
		"email_missing":       {"email", nil},
		"email_invalid":       {"email", "Refused <refused@example.com>"},
		"trust_level_invalid": {"trust_level", "friend"},
	} {
		changed := map[string]interface{}{}
		for k, v := range refused {
			changed[k] = v
		}
		if change.value == nil {
			delete(changed, change.field)
		} else {
			changed[change.field] = change.value
		}
		if !reflect.DeepEqual(requests[name], changed) {
			t.Errorf("%s is %s, want refused with %s changed: %s",
				name, mustJSON(requests[name]), change.field, mustJSON(changed))
		}
	}
}
