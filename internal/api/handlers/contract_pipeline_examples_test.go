package handlers

import (
	"reflect"
	"sort"
	"testing"

	"github.com/restmail/restmail/internal/pipeline/filters"
)

// createPipeline's direction_missing request (#386) is refused by the real
// handler with exactly its documented 422, before anything is stored: the
// handler has no database.
func TestPipelineCreate_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/admin/pipelines", "createPipeline",
		NewPipelineHandler(nil, nil, filters.JSSidecar{}).CreatePipeline)
}

// The pairs vertrag compiles for createPipeline: created with the 201 and
// direction_missing with the 422, nothing else. direction_missing is created
// without its direction, so the direction is the only reason it is refused.
func TestPipelineCreate_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")

	want := map[string][]string{
		"request": {"created", "direction_missing"},
		"201":     {"created"},
		"422":     {"direction_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("createPipeline's named examples are %v, want %v", got, want)
	}

	created, _ := requests["created"].(map[string]interface{})
	if d := created["direction"]; d != "inbound" && d != "outbound" {
		t.Errorf("created's direction is %v, want inbound or outbound", d)
	}
	missing := map[string]interface{}{}
	for k, v := range created {
		if k != "direction" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["direction_missing"], missing) {
		t.Errorf("direction_missing is %s, want created without its direction: %s",
			mustJSON(requests["direction_missing"]), mustJSON(missing))
	}
}

func names(examples map[string]interface{}) []string {
	var out []string
	for name := range examples {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
