package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"testing"

	"github.com/restmail/restmail/internal/pipeline/filters"
)

// createPipeline's direction_missing and filters_missing requests (#386) are
// refused by the real handler with exactly their documented 422s, before
// anything is stored: the handler has no database.
func TestPipelineCreate_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/admin/pipelines", "createPipeline",
		pipelineRoutes(NewPipelineHandler(nil, nil, filters.JSSidecar{})).ServeHTTP)
}

// pipelineCreateRefused names each request example createPipeline refuses
// with the field it is created without.
var pipelineCreateRefused = map[string]string{
	"direction_missing": "direction",
	"filters_missing":   "filters",
}

// The pairs vertrag compiles for createPipeline: created with the 201,
// body_array with the 400, and direction_missing and filters_missing each with
// the 422 of its name. Each 422 request is created without one field, so that
// field is the only reason it is refused, and its 422 names that field alone.
func TestPipelineCreate_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	for _, problem := range pipelineCreatePairProblems(requests, responses) {
		t.Error(problem)
	}
}

func pipelineCreatePairProblems(requests map[string]interface{}, responses map[string]map[string]interface{}) []string {
	var problems []string
	want := map[string][]string{
		"request": {"active_wrong_type", "body_array", "created", "direction_missing", "filters_missing", "null_body"},
		"201":     {"created"},
		"400":     {"body_array", "null_body"},
		"422":     {"active_wrong_type", "direction_missing", "filters_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		problems = append(problems, fmt.Sprintf("createPipeline's named examples are %v, want %v", got, want))
	}

	created, _ := requests["created"].(map[string]interface{})
	if d := created["direction"]; d != "inbound" && d != "outbound" {
		problems = append(problems, fmt.Sprintf("created's direction is %v, want inbound or outbound", d))
	}
	if f, ok := created["filters"].([]interface{}); !ok || len(f) == 0 || !allObjects(f) {
		problems = append(problems, fmt.Sprintf("created's filters are %s, want an array of filter objects", mustJSON(created["filters"])))
	}

	for name, field := range pipelineCreateRefused {
		if _, ok := created[field]; !ok {
			problems = append(problems, fmt.Sprintf("created has no %s for %s to be without", field, name))
		}
		without := map[string]interface{}{}
		for k, v := range created {
			if k != field {
				without[k] = v
			}
		}
		if !reflect.DeepEqual(requests[name], without) {
			problems = append(problems, fmt.Sprintf("%s is %s, want created without its %s: %s",
				name, mustJSON(requests[name]), field, mustJSON(without)))
		}
		if fields := refusedFields(responses["422"][name]); !reflect.DeepEqual(names(fields), []string{field}) {
			problems = append(problems, fmt.Sprintf("the 422 %s is %s, want a validation_failed naming %s alone",
				name, mustJSON(responses["422"][name]), field))
		}
	}
	return problems
}

// refusedFields is the details.fields of a validation_failed error body, or
// nil if the body is not one.
func refusedFields(body interface{}) map[string]interface{} {
	b, _ := body.(map[string]interface{})
	e, _ := b["error"].(map[string]interface{})
	if e["code"] != "validation_failed" {
		return nil
	}
	details, _ := e["details"].(map[string]interface{})
	fields, _ := details["fields"].(map[string]interface{})
	return fields
}

func allObjects(items []interface{}) bool {
	for _, item := range items {
		if _, ok := item.(map[string]interface{}); !ok {
			return false
		}
	}
	return true
}

// The real handler, with no database, refuses filters_missing on its filters
// alone, with the body the spec documents for it, while created, which only
// adds the filters, gets as far as storing the pipeline.
func TestPipelineCreate_FiltersMissingRefused(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation",`+
		`"details":{"fields":{"filters":"must be an array of filter objects"}}}}`)
	if !reflect.DeepEqual(responses["422"]["filters_missing"], want) {
		t.Errorf("the spec documents filters_missing's 422 as %s, want %s",
			mustJSON(responses["422"]["filters_missing"]), mustJSON(want))
	}

	body := string(mustJSON(requests["filters_missing"]))
	rr, reached := createPipelineNoDB(t, body)
	if reached {
		t.Fatalf("%s reached the database before it was refused", body)
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%s: status %d, want 422; body %s", body, rr.Code, rr.Body)
	}
	got := decodeShapeBody(t, rr)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered %s, want %s", body, rr.Body, mustJSON(want))
	}

	// Controls: created is not refused; the answer is not direction_missing's
	// documented body; and a request without its direction too is refused
	// with a body other than filters_missing's.
	created := string(mustJSON(requests["created"]))
	if rr, reached := createPipelineNoDB(t, created); !reached {
		t.Errorf("created %s never reached the database: status %d, body %s", created, rr.Code, rr.Body)
	}
	if reflect.DeepEqual(got, responses["422"]["direction_missing"]) {
		t.Error("filters_missing is answered with direction_missing's 422")
	}
	missing, _ := requests["filters_missing"].(map[string]interface{})
	neither := map[string]interface{}{}
	for k, v := range missing {
		if k != "direction" {
			neither[k] = v
		}
	}
	rr, reached = createPipelineNoDB(t, string(mustJSON(neither)))
	if reached || rr.Code != http.StatusUnprocessableEntity || reflect.DeepEqual(decodeShapeBody(t, rr), want) {
		t.Errorf("%s answered %d %s, want a 422 other than filters_missing's", mustJSON(neither), rr.Code, rr.Body)
	}
}

// Each way the filters_missing pair can be wrong is caught: the request or
// the 422 missing or misnamed, a 422 naming another field, and a request
// without more, or less, than its filters. The mutations are made on copies,
// and the spec's examples are the same afterwards.
func TestPipelineCreate_ExamplePairMutations(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/pipelines", "createPipeline")
	if problems := pipelineCreatePairProblems(requests, responses); len(problems) != 0 {
		t.Fatalf("the spec's pairs are wrong before any mutation: %v", problems)
	}
	before := mustJSON([]interface{}{requests, responses})

	mutations := map[string]func(map[string]interface{}, map[string]map[string]interface{}){
		"request missing": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			delete(req, "filters_missing")
		},
		"request misnamed": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["filter_missing"] = req["filters_missing"]
			delete(req, "filters_missing")
		},
		"422 missing": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			delete(res["422"], "filters_missing")
		},
		"422 misnamed": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["422"]["filter_missing"] = res["422"]["filters_missing"]
			delete(res["422"], "filters_missing")
		},
		"422 also on 201": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["201"]["filters_missing"] = res["201"]["created"]
		},
		"422 names direction": func(_ map[string]interface{}, res map[string]map[string]interface{}) {
			res["422"]["filters_missing"] = res["422"]["direction_missing"]
		},
		"request keeps filters": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["filters_missing"] = req["created"]
		},
		"request without domain_id too": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			delete(req["filters_missing"].(map[string]interface{}), "domain_id")
		},
		"request with empty filters": func(req map[string]interface{}, _ map[string]map[string]interface{}) {
			req["filters_missing"].(map[string]interface{})["filters"] = []interface{}{}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var copied struct {
				Requests  map[string]interface{}
				Responses map[string]map[string]interface{}
			}
			if err := json.Unmarshal(mustJSON(map[string]interface{}{"Requests": requests, "Responses": responses}), &copied); err != nil {
				t.Fatal(err)
			}
			mutate(copied.Requests, copied.Responses)
			if len(pipelineCreatePairProblems(copied.Requests, copied.Responses)) == 0 {
				t.Errorf("the mutation %q is not caught", name)
			}
		})
	}

	if after := mustJSON([]interface{}{requests, responses}); !bytes.Equal(after, before) {
		t.Errorf("the mutations changed the spec's examples:\n  %s\nwas\n  %s", after, before)
	}
}

func names[V any](examples map[string]V) []string {
	var out []string
	for name := range examples {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
