package handlers

import (
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/pipeline/filters"
)

func TestCustomFilterCreate_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/admin/custom-filters", "createCustomFilter",
		NewPipelineHandler(nil, nil, filters.JSSidecar{}).CreateCustomFilter)
}

func TestCustomFilterCreate_ExamplePair(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/custom-filters", "createCustomFilter")
	if len(requests) != 2 || len(responses["201"]) != 1 || len(responses["422"]) != 1 {
		t.Fatalf("want two requests paired with one 201 and one 422, got %v / %v", requests, responses)
	}
	created, ok := requests["created"].(map[string]interface{})
	if !ok || created["filter_type"] != "action" {
		t.Fatal("created must keep the valid action filter")
	}
	refused, ok := requests["filter_type_invalid"].(map[string]interface{})
	if !ok || refused["filter_type"] != "invalid" {
		t.Fatal("filter_type_invalid must have an invalid filter_type")
	}
	if _, ok := responses["201"]["created"]; !ok {
		t.Fatal("created must pair with 201")
	}
	if _, ok := responses["422"]["filter_type_invalid"]; !ok {
		t.Fatal("filter_type_invalid must pair with 422")
	}

	// The refusal changes just one constraint, preserving the script, config
	// and disabled state used by the successful contract transaction.
	refused["filter_type"] = created["filter_type"]
	if !reflect.DeepEqual(created, refused) {
		t.Fatalf("refused request differs from created beyond filter_type: %v / %v", created, refused)
	}
}
