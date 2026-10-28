package handlers

import (
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// Sending a draft can supply the unsubscribe details a bulk domain's mail needs (#144),
// in an optional body: a draft sent with no body still binds, as it always did.
func TestSendDraft_TakesAnOptionalListUnsubscribe(t *testing.T) {
	params := func(where, name string) (string, bool) { return "12", where == "path" && name == "id" }

	var bare sendDraftRequest
	if err := oapifly.Bind(&bare, params, nil); err != nil {
		t.Fatalf("a draft sent with no body did not bind: %v", err)
	}
	if bare.ID != 12 || bare.Body != nil {
		t.Fatalf("bare send = %+v", bare)
	}

	var supplied sendDraftRequest
	body := `{"list_unsubscribe":{"https":"https://example.test/unsubscribe"}}`
	if err := oapifly.Bind(&supplied, params, []byte(body)); err != nil {
		t.Fatalf("a draft sent with list_unsubscribe did not bind: %v", err)
	}
	if supplied.Body == nil || supplied.Body.ListUnsubscribe == nil ||
		supplied.Body.ListUnsubscribe.HTTPS != "https://example.test/unsubscribe" {
		t.Fatalf("list_unsubscribe = %+v", supplied.Body)
	}
}
