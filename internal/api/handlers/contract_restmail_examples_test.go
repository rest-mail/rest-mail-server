package handlers

import (
	"reflect"
	"testing"
)

const restmailMessagesPath = "/restmail/messages"

// restmailDeliver's recipients_missing request (#386) is refused by the real
// handler with exactly its documented 422, before the sender's domain is
// looked up, a signature is verified or anything is stored: the handler has no
// database, and nothing is configured to trust or check.
func TestRestmailDeliver_DocumentedRefusals(t *testing.T) {
	deliver := restmailRoutes(NewRestmailHandler(nil, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})).ServeHTTP
	documentedRefusals(t, restmailMessagesPath, "restmailDeliver", deliver)
}

// The pairs vertrag compiles for restmailDeliver: delivered with the 201 and
// recipients_missing with the 422, nothing else. recipients_missing is
// delivered without its to, and neither carries a recipients alias or a raw
// message, so the missing to is the only reason it is refused.
func TestRestmailDeliver_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, restmailMessagesPath, "restmailDeliver")

	want := map[string][]string{
		"request": {"delivered", "recipients_missing"},
		"201":     {"delivered"},
		"422":     {"recipients_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restmailDeliver's named examples are %v, want %v", got, want)
	}

	delivered := map[string]interface{}{
		"from":       "someone@ext-contract.test",
		"to":         []interface{}{"eve@restmail.test"},
		"subject":    "Delivered over RESTMAIL",
		"body_text":  "Hello, Eve.",
		"message_id": "<restmail-delivery@ext-contract.test>",
		"date":       "Sat, 17 Oct 2026 12:00:00 +0000",
	}
	if !reflect.DeepEqual(requests["delivered"], delivered) {
		t.Errorf("delivered is %s, want the valid delivery request %s",
			mustJSON(requests["delivered"]), mustJSON(delivered))
	}
	missing := map[string]interface{}{}
	for k, v := range delivered {
		if k != "to" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["recipients_missing"], missing) {
		t.Errorf("recipients_missing is %s, want delivered without its to: %s",
			mustJSON(requests["recipients_missing"]), mustJSON(missing))
	}

	// The handler answers a delivery with the recipients it stored the message
	// for and those it could not: delivered's one recipient is the fixture's
	// seeded eve, so the documented answer delivers to her and fails no one.
	result := map[string]interface{}{
		"data": map[string]interface{}{
			"delivered": []interface{}{"eve@restmail.test"},
			"failed":    []interface{}{},
		},
	}
	if !reflect.DeepEqual(responses["201"]["delivered"], result) {
		t.Errorf("the 201's delivered is %s, want %s",
			mustJSON(responses["201"]["delivered"]), mustJSON(result))
	}
}
