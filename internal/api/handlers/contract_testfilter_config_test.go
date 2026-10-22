package handlers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// TestFilter's config is a required JSON object, regardless of whether the
// selected factory checks its config. header_validate ignores decoding errors,
// so it used to accept missing config and every JSON type below.
func TestTestFilter_ConfigObjectRequired(t *testing.T) {
	for name, config := range map[string]string{
		"missing": "", "null": "null", "array": "[]", "array with object": "[{}]",
		"string": `"config"`, "empty string": `""`, "number": "1", "boolean": "false",
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"filter_name":"header_validate","email":{}`
			if config != "" {
				body += `,"config":` + config
			}
			body += `}`
			var request interface{}
			if err := json.Unmarshal([]byte(body), &request); err != nil {
				t.Fatal(err)
			}
			h, made, _ := testFilterHandler(t)
			rr := postTestFilter(t, h, request)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d, want 422; body %s", rr.Code, rr.Body)
			}
			want := parseShapeJSON(t, `{"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"config":"must be an object"}}}}`)
			if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
				t.Errorf("body %s, want %s", rr.Body, mustJSON(want))
			}
			if *made != 0 {
				t.Errorf("made %d filters before rejecting config", *made)
			}
		})
	}
}

// Empty and populated objects reach the factory unchanged. Filter-specific
// validation remains the factory's job; a reject action is still a successful
// test execution, represented by a 200 response.
func TestTestFilter_ConfigObjectsReachFactory(t *testing.T) {
	for name, config := range map[string]map[string]interface{}{
		"empty":   {},
		"options": {"require_from": false, "require_date": false},
	} {
		t.Run(name, func(t *testing.T) {
			h, made, given := testFilterHandler(t)
			rr := postTestFilter(t, h, map[string]interface{}{
				"filter_name": "header_validate", "email": map[string]interface{}{}, "config": config,
			})
			if rr.Code != http.StatusOK || *made != 1 {
				t.Fatalf("status %d, filters made %d; body %s", rr.Code, *made, rr.Body)
			}
			var got map[string]interface{}
			if err := json.Unmarshal(*given, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, config) {
				t.Errorf("factory config %s, want %s", *given, mustJSON(config))
			}
		})
	}
}
