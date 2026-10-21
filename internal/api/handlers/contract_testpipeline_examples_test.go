package handlers

import (
	"net/http"
	"reflect"
	"testing"
)

const testPipelinePath = "/api/v1/admin/pipelines/test"

// The pairs vertrag compiles for testPipeline (#386): valid with the 200,
// invalid_body with the 400, and email_missing with the 422. email_missing is
// valid without its email, so the email is the only reason it is refused.
func TestTestPipeline_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, testPipelinePath, "testPipeline")

	want := map[string][]string{
		"request": {"email_missing", "invalid_body", "valid"},
		"200":     {"valid"},
		"400":     {"invalid_body"},
		"422":     {"email_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("testPipeline's named examples are %v, want %v", got, want)
	}

	if want := []interface{}{}; !reflect.DeepEqual(requests["invalid_body"], want) {
		t.Errorf("invalid_body is %s, want []", mustJSON(requests["invalid_body"]))
	}
	valid, ok := requests["valid"].(map[string]interface{})
	if !ok || valid["email"] == nil {
		t.Fatalf("valid is %s, want a request with an email", mustJSON(requests["valid"]))
	}
	missing := map[string]interface{}{}
	for k, v := range valid {
		if k != "email" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["email_missing"], missing) {
		t.Errorf("email_missing is %s, want valid without its email: %s",
			mustJSON(requests["email_missing"]), mustJSON(missing))
	}
}

// invalid_body and email_missing are refused by the real handler with exactly
// their documented 400 and 422. The handler has no database, so each refusal
// is proved to come before the pipeline is looked up.
func TestTestPipeline_DocumentedRefusals(t *testing.T) {
	requests, responses := postExamples(t, testPipelinePath, "testPipeline")
	if len(responses["400"]) == 0 || len(responses["422"]) == 0 {
		t.Fatalf("testPipeline's 400 and 422 must each name an example, got %v", responses)
	}
	for status, code := range map[string]int{"400": http.StatusBadRequest, "422": http.StatusUnprocessableEntity} {
		for name, want := range responses[status] {
			t.Run(status+"/"+name, func(t *testing.T) {
				request, ok := requests[name]
				if !ok {
					t.Fatalf("the %s example %q has no request example of the same name", status, name)
				}
				checkTestPipelineRefusal(t, string(mustJSON(request)), code, string(mustJSON(want)))
			})
		}
	}
}
