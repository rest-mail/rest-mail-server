package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

const testFilterPath = "/api/v1/admin/pipelines/test-filter"

// testFilterValid is the request the spec has always shown for testFilter.
const testFilterValid = `{
  "filter_name": "header_validate",
  "config": {},
  "email": {
    "envelope": {"mail_from": "someone@example.com", "rcpt_to": ["eve@restmail.test"]},
    "headers": {
      "from": [{"name": "Someone", "address": "someone@example.com"}],
      "to": [{"address": "eve@restmail.test"}],
      "subject": "A filter test",
      "message-id": "<filter-test@example.com>",
      "date": "Sat, 17 Oct 2026 12:00:00 +0000"
    },
    "body": {"content_type": "text/plain", "content": "Hello."}
  }
}`

// testFilterHandler is the real handler on a real engine, whose registry holds
// header_validate's own factory wrapped to count the filters it makes and keep
// the config it is given. It has no database.
func testFilterHandler(t *testing.T) (*PipelineHandler, *int, *[]byte) {
	t.Helper()
	var made int
	var config []byte
	registry := pipeline.NewRegistry()
	registry.Register("header_validate", func(c []byte) (pipeline.Filter, error) {
		made++
		config = append([]byte(nil), c...)
		return filters.NewHeaderValidate(c)
	})
	return NewPipelineHandler(nil, pipeline.NewEngine(registry, nil), filters.JSSidecar{}), &made, &config
}

func postTestFilter(t *testing.T, h *PipelineHandler, request interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.TestFilter(rr, httptest.NewRequest(http.MethodPost, testFilterPath, bytes.NewReader(body)))
	return rr
}

// The pairs vertrag compiles for testFilter (#386): valid with the 200,
// invalid_body with the 400, and filter_name_missing with the 422. valid is
// the request the spec has always shown, and filter_name_missing is valid
// without its filter name, so the name is the only reason it is refused.
func TestTestFilter_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, testFilterPath, "testFilter")

	want := map[string][]string{
		"request": {"filter_name_missing", "valid"},
		"200":     {"valid"},
		"400":     {"invalid_body"},
		"422":     {"filter_name_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("testFilter's named examples are %v, want %v", got, want)
	}

	var valid map[string]interface{}
	if err := json.Unmarshal([]byte(testFilterValid), &valid); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(requests["valid"], valid) {
		t.Errorf("valid is %s, want the request the spec has always shown %s",
			mustJSON(requests["valid"]), mustJSON(valid))
	}
	missing := map[string]interface{}{}
	for k, v := range valid {
		if k != "filter_name" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["filter_name_missing"], missing) {
		t.Errorf("filter_name_missing is %s, want valid without its filter_name: %s",
			mustJSON(requests["filter_name_missing"]), mustJSON(missing))
	}
}

// filter_name_missing is refused by the real handler with exactly its
// documented 422 before the engine is asked, and valid runs the real
// header_validate, made once with the request's config, and gets the
// documented 200. The handler has no database.
func TestTestFilter_DocumentedAnswers(t *testing.T) {
	requests, responses := postExamples(t, testFilterPath, "testFilter")
	if len(responses["422"]) == 0 || len(responses["200"]) == 0 {
		t.Fatalf("testFilter's 200 and 422 must each name an example, got %v", responses)
	}

	for name, want := range responses["422"] {
		t.Run(name, func(t *testing.T) {
			request, ok := requests[name]
			if !ok {
				t.Fatalf("the 422 example %q has no request example of the same name", name)
			}
			// No engine: a request that reached it would panic, not be refused.
			h := NewPipelineHandler(nil, nil, filters.JSSidecar{})
			var rr *httptest.ResponseRecorder
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s reached the engine before it was refused: %v", mustJSON(request), r)
					}
				}()
				rr = postTestFilter(t, h, request)
			}()
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status %d, want 422; body %s", mustJSON(request), rr.Code, rr.Body)
			}
			assertDocumentedBody(t, request, rr, want)
		})
	}

	for name, want := range responses["200"] {
		t.Run(name, func(t *testing.T) {
			request, ok := requests[name]
			if !ok {
				t.Fatalf("the 200 example %q has no request example of the same name", name)
			}
			h, made, config := testFilterHandler(t)
			rr := postTestFilter(t, h, request)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status %d, want 200; body %s", mustJSON(request), rr.Code, rr.Body)
			}
			assertDocumentedBody(t, request, rr, want)
			if *made != 1 {
				t.Fatalf("%s made %d filters, want one", mustJSON(request), *made)
			}
			var given interface{}
			if err := json.Unmarshal(*config, &given); err != nil {
				t.Fatalf("decode the filter's config %s: %v", *config, err)
			}
			if sent := request.(map[string]interface{})["config"]; !reflect.DeepEqual(given, sent) {
				t.Errorf("the filter was made with %s, want the request's config %s", *config, mustJSON(sent))
			}
		})
	}
}
