package handlers

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const blocklistPath = "/api/v1/admin/domains/{id}/blocklist"

// addToBlocklist's pattern_missing request (#386) is refused by the real
// handler with exactly its documented 422, before anything is looked up or
// stored: the handler has no database. The route gives it a valid domain ID,
// as the router would, so the body is the only reason it is refused.
func TestBlocklistAdd_DocumentedRefusals(t *testing.T) {
	add := policyRoutes(NewSenderRuleHandler(nil))
	documentedRefusals(t, blocklistPath, "addToBlocklist", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.ReplaceAll(blocklistPath, "{id}", "1")
		r.URL.RawPath = ""
		add.ServeHTTP(w, r)
	})
}

// The pairs vertrag compiles for addToBlocklist: created with the 201 and
// pattern_missing with the 422, nothing else. pattern_missing is created
// without its pattern, so the pattern is the only reason it is refused.
func TestBlocklistAdd_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, blocklistPath, "addToBlocklist")

	want := map[string][]string{
		"request": {"created", "pattern_missing"},
		"201":     {"created"},
		"422":     {"pattern_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addToBlocklist's named examples are %v, want %v", got, want)
	}

	created := map[string]interface{}{
		"pattern": "spam@spammer.example.com",
		"reason":  "Sends spam",
	}
	if !reflect.DeepEqual(requests["created"], created) {
		t.Errorf("created is %s, want the valid blocklist request %s",
			mustJSON(requests["created"]), mustJSON(created))
	}
	missing := map[string]interface{}{}
	for k, v := range created {
		if k != "pattern" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["pattern_missing"], missing) {
		t.Errorf("pattern_missing is %s, want created without its pattern: %s",
			mustJSON(requests["pattern_missing"]), mustJSON(missing))
	}
}
