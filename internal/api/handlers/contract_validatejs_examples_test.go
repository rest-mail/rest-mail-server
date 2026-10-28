package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/restmail/restmail/internal/pipeline/filters"
)

const validateJSPath = "/api/v1/admin/custom-filters/validate"

// validateJSSidecar stands in for the JS filter sidecar's /validate: it counts
// the requests it gets, keeps the last body, and answers as the sidecar does
// for a script that compiles when no sample email is sent.
func validateJSSidecar(t *testing.T) (*PipelineHandler, *int32, *[]byte) {
	t.Helper()
	var calls int32
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/validate" || r.Header.Get("Authorization") != "Bearer "+testJSSidecarToken {
			t.Errorf("sidecar got %s with Authorization %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		got, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"valid":true}`))
	}))
	t.Cleanup(srv.Close)
	return NewPipelineHandler(nil, nil, filters.JSSidecar{URL: srv.URL, Token: testJSSidecarToken}), &calls, &got
}

func postValidateJS(t *testing.T, h *PipelineHandler, request interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	pipelineRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, validateJSPath, bytes.NewReader(body)))
	return rr
}

// The pairs vertrag compiles for validateCustomFilter (#386): valid with the
// 200, body_array with the 400 and script_missing with the 422, nothing else.
// valid is the script the spec has always shown, and script_missing is valid
// without its script, so the script is the only reason it is refused.
func TestValidateCustomFilter_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, validateJSPath, "validateCustomFilter")

	want := map[string][]string{
		"request": {"body_array", "script_missing", "valid"},
		"200":     {"valid"},
		"422":     {"body_array", "script_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validateCustomFilter's named examples are %v, want %v", got, want)
	}

	valid := map[string]interface{}{
		"script": "function filter(email) {\n  return { action: \"continue\" };\n}\n",
	}
	if !reflect.DeepEqual(requests["valid"], valid) {
		t.Errorf("valid is %s, want the script the spec has always shown %s",
			mustJSON(requests["valid"]), mustJSON(valid))
	}
	missing := map[string]interface{}{}
	for k, v := range valid {
		if k != "script" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["script_missing"], missing) {
		t.Errorf("script_missing is %s, want valid without its script: %s",
			mustJSON(requests["script_missing"]), mustJSON(missing))
	}
}

// script_missing is refused by the real handler with exactly its documented
// 422 before the sidecar is asked, and valid reaches the sidecar unchanged and
// gets its answer back verbatim, as the documented 200. The handler has no
// database.
func TestValidateCustomFilter_DocumentedAnswers(t *testing.T) {
	requests, responses := postExamples(t, validateJSPath, "validateCustomFilter")
	if len(responses["422"]) == 0 || len(responses["200"]) == 0 {
		t.Fatalf("validateCustomFilter's 200 and 422 must each name an example, got %v", responses)
	}

	for name, want := range responses["422"] {
		t.Run(name, func(t *testing.T) {
			request, ok := requests[name]
			if !ok {
				t.Fatalf("the 422 example %q has no request example of the same name", name)
			}
			h, calls, _ := validateJSSidecar(t)
			rr := postValidateJS(t, h, request)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: status %d, want 422; body %s", mustJSON(request), rr.Code, rr.Body)
			}
			assertDocumentedBody(t, request, rr, want)
			if n := atomic.LoadInt32(calls); n != 0 {
				t.Errorf("%s was sent to the sidecar %d times before it was refused", mustJSON(request), n)
			}
		})
	}

	for name, want := range responses["200"] {
		t.Run(name, func(t *testing.T) {
			request, ok := requests[name]
			if !ok {
				t.Fatalf("the 200 example %q has no request example of the same name", name)
			}
			h, calls, sent := validateJSSidecar(t)
			rr := postValidateJS(t, h, request)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status %d, want 200; body %s", mustJSON(request), rr.Code, rr.Body)
			}
			assertDocumentedBody(t, request, rr, want)
			if n := atomic.LoadInt32(calls); n != 1 {
				t.Fatalf("%s reached the sidecar %d times, want once", mustJSON(request), n)
			}
			var forwarded interface{}
			if err := json.Unmarshal(*sent, &forwarded); err != nil {
				t.Fatalf("decode the sidecar's request %s: %v", *sent, err)
			}
			if !reflect.DeepEqual(forwarded, request) {
				t.Errorf("the sidecar got %s, want the request unchanged: %s", *sent, mustJSON(request))
			}
		})
	}
}

func assertDocumentedBody(t *testing.T, request interface{}, rr *httptest.ResponseRecorder, want interface{}) {
	t.Helper()
	var got interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rr.Body, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s answered\n  %s\nthe spec documents\n  %s", mustJSON(request), rr.Body, mustJSON(want))
	}
}
