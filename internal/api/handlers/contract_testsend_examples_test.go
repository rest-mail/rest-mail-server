package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/config"
)

// sendTestEmail's from_missing request (#386) is refused by the real handler,
// outside production, with exactly its documented 422, before any mailbox is
// looked up: the handler has no database.
func TestSendTestEmail_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/admin/test/send", "sendTestEmail",
		NewTestHandler(nil, &config.Config{Environment: "test"}).SendTestEmail)
}

// The pairs vertrag compiles for sendTestEmail: created with the 201 and
// from_missing with the 422, nothing else. created is the request the spec
// always documented, and from_missing is it without its sender, so the sender
// is the only reason it is refused.
func TestSendTestEmail_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/test/send", "sendTestEmail")

	want := map[string][]string{
		"request": {"created", "from_missing"},
		"201":     {"created"},
		"422":     {"from_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sendTestEmail's named examples are %v, want %v", got, want)
	}

	created := map[string]interface{}{
		"from":    "postmaster@restmail.test",
		"to":      "admin@restmail.test",
		"subject": "Test",
		"body":    "A test message.",
	}
	if !reflect.DeepEqual(requests["created"], created) {
		t.Errorf("created is %s, want the documented request %s",
			mustJSON(requests["created"]), mustJSON(created))
	}
	missing := map[string]interface{}{}
	for k, v := range created {
		if k != "from" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["from_missing"], missing) {
		t.Errorf("from_missing is %s, want created without its sender: %s",
			mustJSON(requests["from_missing"]), mustJSON(missing))
	}
}

// In production the test endpoints stay shut: every documented request,
// the refused one included, is answered 403 before it is read, and nothing is
// looked up.
func TestSendTestEmail_ProductionRefusesExamples(t *testing.T) {
	requests, _ := postExamples(t, "/api/v1/admin/test/send", "sendTestEmail")
	if len(requests) == 0 {
		t.Fatal("sendTestEmail names no request example")
	}
	send := NewTestHandler(nil, &config.Config{Environment: "production"}).SendTestEmail

	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/send", bytes.NewReader(body))
			rr := httptest.NewRecorder()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s reached the database in production: %v", body, r)
					}
				}()
				send(rr, req)
			}()

			if rr.Code != http.StatusForbidden {
				t.Fatalf("%s: status %d in production, want 403; body %s", body, rr.Code, rr.Body)
			}
		})
	}
}
