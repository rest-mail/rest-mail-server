package handlers

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

const sendMessagePath = "/api/v1/messages/send"

// The pairs vertrag compiles for sendMessage (#386): valid with the 201,
// from_missing with the 400, and unsubscribe_not_allowed with the 422. Each
// refused request is valid but for the one field it is named for.
func TestSendMessage_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, sendMessagePath, "sendMessage")

	want := map[string][]string{
		"request": {"from_missing", "unsubscribe_not_allowed", "valid"},
		"201":     {"valid"},
		"400":     {"from_missing"},
		"422":     {"unsubscribe_not_allowed"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sendMessage's named examples are %v, want %v", got, want)
	}

	valid, ok := requests["valid"].(map[string]interface{})
	if !ok || valid["from"] == nil || valid["list_unsubscribe"] != nil {
		t.Fatalf("valid is %s, want a request with a from and no list_unsubscribe", mustJSON(requests["valid"]))
	}
	missing := map[string]interface{}{}
	unsubscribe := map[string]interface{}{"list_unsubscribe": map[string]interface{}{"mailto": "unsubscribe@restmail.test"}}
	for k, v := range valid {
		unsubscribe[k] = v
		if k != "from" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["from_missing"], missing) {
		t.Errorf("from_missing is %s, want valid without its from: %s",
			mustJSON(requests["from_missing"]), mustJSON(missing))
	}
	if !reflect.DeepEqual(requests["unsubscribe_not_allowed"], unsubscribe) {
		t.Errorf("unsubscribe_not_allowed is %s, want valid with a list_unsubscribe: %s",
			mustJSON(requests["unsubscribe_not_allowed"]), mustJSON(unsubscribe))
	}
}

// from_missing is refused by the real handler with exactly its documented
// 400. The handler has no database, so the refusal is proved to come before
// the sender is looked up.
func TestSendMessage_DocumentedBadRequest(t *testing.T) {
	requests, responses := postExamples(t, sendMessagePath, "sendMessage")
	claims := &auth.Claims{AccountID: 1, CredentialKind: "app_password"}

	rr := sendAs(&MessageHandler{}, claims, string(mustJSON(requests["from_missing"])))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body %s", rr.Code, rr.Body)
	}
	if got, want := decodeShapeBody(t, rr), parseShapeJSON(t, string(mustJSON(responses["400"]["from_missing"]))); !reflect.DeepEqual(got, want) {
		t.Errorf("from_missing answered %s, want %v", rr.Body, want)
	}
}

// unsubscribe_not_allowed, sent from a transactional domain as the contract
// run's hook makes restmail.test, is refused with exactly its documented 422.
// Needs the unit-test Postgres; skips when none is reachable.
func TestSendMessage_DocumentedPolicyRefusal(t *testing.T) {
	requests, responses := postExamples(t, sendMessagePath, "sendMessage")
	h, _, _ := seedSigningDomain(t, "contract-send")

	domain := fmt.Sprintf("contract-send-%d.test", time.Now().UnixNano())
	if err := h.db.Create(&models.Domain{Name: domain, SendingPolicy: models.SendingPolicyTransactional}).Error; err != nil {
		t.Fatalf("seed the domain: %v", err)
	}
	sender := seedSendingMailbox(t, h.db, domain, "admin")
	request, ok := requests["unsubscribe_not_allowed"].(map[string]interface{})
	if !ok {
		t.Fatalf("unsubscribe_not_allowed is %s, want an object", mustJSON(requests["unsubscribe_not_allowed"]))
	}
	request["from"] = sender.Address

	rr := sendAs(h, &auth.Claims{AccountID: sender.ID, CredentialKind: "app_password"}, string(mustJSON(request)))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422; body %s", rr.Code, rr.Body)
	}
	if got, want := decodeShapeBody(t, rr), parseShapeJSON(t, string(mustJSON(responses["422"]["unsubscribe_not_allowed"]))); !reflect.DeepEqual(got, want) {
		t.Errorf("unsubscribe_not_allowed answered %s, want %v", rr.Body, want)
	}
	var queued int64
	h.db.Model(&models.OutboundQueue{}).Where("sender = ?", sender.Address).Count(&queued)
	if queued != 0 {
		t.Errorf("the refused send queued %d messages", queued)
	}
}
