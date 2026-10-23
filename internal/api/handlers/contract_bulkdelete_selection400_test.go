package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	yaml "go.yaml.in/yaml/v2"
)

const bulkDeleteQueuePath = "/api/v1/admin/queue/bulk-delete"

// bulkDeleteSpec is the DELETE on bulkDeleteQueuePath as the spec the API
// serves documents it. The pairing helpers read a POST, so this reads the
// DELETE itself.
type bulkDeleteSpec struct {
	OperationID string                `yaml:"operationId"`
	Security    []map[string][]string `yaml:"security"`
	RequestBody struct {
		Required bool                   `yaml:"required"`
		Content  map[string]bulkContent `yaml:"content"`
	} `yaml:"requestBody"`
	Responses map[string]struct {
		Content map[string]bulkContent `yaml:"content"`
	} `yaml:"responses"`
}

type bulkContent struct {
	Schema struct {
		Ref        string   `yaml:"$ref"`
		Type       string   `yaml:"type"`
		Required   []string `yaml:"required"`
		Properties map[string]struct {
			Ref string `yaml:"$ref"`
		} `yaml:"properties"`
	} `yaml:"schema"`
	Example  interface{} `yaml:"example"`
	Examples map[string]struct {
		Value interface{} `yaml:"value"`
	} `yaml:"examples"`
}

func readBulkDeleteSpec(t *testing.T) bulkDeleteSpec {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths map[string]struct {
			Delete *bulkDeleteSpec `yaml:"delete"`
			Post   interface{}     `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	path, ok := doc.Paths[bulkDeleteQueuePath]
	if !ok || path.Delete == nil {
		t.Fatalf("the spec has no DELETE %s", bulkDeleteQueuePath)
	}
	if path.Post != nil {
		t.Errorf("%s documents a POST: bulkDeleteQueue is a DELETE", bulkDeleteQueuePath)
	}
	return *path.Delete
}

func (s bulkDeleteSpec) requests(t *testing.T) map[string]interface{} {
	t.Helper()
	out := map[string]interface{}{}
	for name, example := range s.RequestBody.Content["application/json"].Examples {
		out[name] = jsonValue(t, example.Value)
	}
	return out
}

func (s bulkDeleteSpec) responses(t *testing.T) map[string]map[string]interface{} {
	t.Helper()
	out := map[string]map[string]interface{}{}
	for status, response := range s.Responses {
		for name, example := range response.Content["application/json"].Examples {
			if out[status] == nil {
				out[status] = map[string]interface{}{}
			}
			out[status][name] = jsonValue(t, example.Value)
		}
	}
	return out
}

// bulkDeletePairProblems says what is wrong with how the operation pairs its
// refused request with its 400, and keeps the successful request/response
// contract as it was: ids selects entries and answers 200 with the affected
// count, and the refused request is named on the 400 and nowhere else.
func bulkDeletePairProblems(t *testing.T, s bulkDeleteSpec) []string {
	t.Helper()
	var problems []string
	add := func(format string, args ...interface{}) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if s.OperationID != "bulkDeleteQueue" {
		add("the operation is %q, want bulkDeleteQueue", s.OperationID)
	}
	wantSecurity := []map[string][]string{
		{"CookieAuth": {"queue:manage"}},
		{"BearerAuth": {"queue:manage"}},
		{"APIKeyAuth": {"queue:manage"}},
	}
	if !reflect.DeepEqual(s.Security, wantSecurity) {
		add("security is %v, want %v", s.Security, wantSecurity)
	}
	if !s.RequestBody.Required {
		add("the request body is not required")
	}
	body := s.RequestBody.Content["application/json"]
	if body.Schema.Ref != "#/components/schemas/BulkQueueRequest" {
		add("the request schema is %q, want BulkQueueRequest", body.Schema.Ref)
	}
	if body.Example != nil {
		add("the request keeps an unnamed example next to the named ones: it pairs with every response")
	}

	requests := s.requests(t)
	responses := s.responses(t)

	if got := names(requests); !reflect.DeepEqual(got, []string{"by_ids", "invalid_ids", "selection_missing"}) {
		add("the request examples are %v, want by_ids, invalid_ids and selection_missing", got)
	}
	if got, want := requests["by_ids"], jsonValue(t, map[interface{}]interface{}{"ids": []interface{}{987654}}); !reflect.DeepEqual(got, want) {
		add("by_ids is %s, want the original request %s", mustJSON(got), mustJSON(want))
	}
	if got, want := requests["selection_missing"], map[string]interface{}{}; !reflect.DeepEqual(got, want) {
		add("selection_missing is %s, want an empty object", mustJSON(got))
	}

	statuses := make([]string, 0, len(s.Responses))
	for status := range s.Responses {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	if !reflect.DeepEqual(statuses, []string{"200", "400", "422"}) {
		add("the responses are %v, want 200, 400 and 422", statuses)
	}

	// 200: unchanged contract, with its example named for the request that
	// selects entries so the refused request is not paired with success.
	ok := s.Responses["200"].Content["application/json"]
	if ok.Schema.Type != "object" || !reflect.DeepEqual(ok.Schema.Required, []string{"data"}) ||
		ok.Schema.Properties["data"].Ref != "#/components/schemas/BulkQueueResponse" {
		add("the 200's schema is no longer an object whose data is a BulkQueueResponse")
	}
	if got := names(responses["200"]); !reflect.DeepEqual(got, []string{"by_ids"}) {
		add("the 200 names %v, want only by_ids", got)
	}
	wantOK := map[string]interface{}{"data": map[string]interface{}{"affected": float64(1)}}
	if got := responses["200"]["by_ids"]; !reflect.DeepEqual(got, wantOK) {
		add("the 200's by_ids is %s, want %s", mustJSON(got), mustJSON(wantOK))
	}

	// 400: the handler's own refusal, an ErrorResponse naming only the request
	// that selects nothing.
	if ref := s.Responses["400"].Content["application/json"].Schema.Ref; ref != "#/components/schemas/ErrorResponse" {
		add("the 400's schema is %q, want ErrorResponse", ref)
	}
	if got := names(responses["400"]); !reflect.DeepEqual(got, []string{"selection_missing"}) {
		add("the 400 names %v, want only selection_missing", got)
	}
	wantRefusal := map[string]interface{}{
		"error": map[string]interface{}{"code": "bad_request", "message": "ids or filter required"},
	}
	if got := responses["400"]["selection_missing"]; !reflect.DeepEqual(got, wantRefusal) {
		add("the 400's selection_missing is %s, want %s", mustJSON(got), mustJSON(wantRefusal))
	}

	for name := range requests {
		var on []string
		for status, examples := range responses {
			if _, named := examples[name]; named {
				on = append(on, status)
			}
		}
		sort.Strings(on)
		want := map[string][]string{"by_ids": {"200"}, "selection_missing": {"400"}, "invalid_ids": {"422"}}[name]
		if !reflect.DeepEqual(on, want) {
			add("request example %q is named on responses %v, want %v", name, on, want)
		}
	}
	return problems
}

// bulkDeleteQueue's 400 pair (#386): selection_missing selects no entries,
// is named on the 400 and nowhere else, and the 400 names it alone, while the
// operation's successful request and 200 stay as they were.
func TestBulkDeleteQueue_SelectionMissingPair(t *testing.T) {
	for _, problem := range bulkDeletePairProblems(t, readBulkDeleteSpec(t)) {
		t.Error(problem)
	}
}

// selection_missing is refused by the real handler with exactly the documented
// 400. The handler has no database: a query built would panic, so the 400 is
// proved to come before any entry is selected or deleted. An empty ids is the
// same refusal, and the original request (by_ids) is never sent, because
// accepting it would touch the database.
func TestBulkDeleteQueue_SelectionMissingRefused(t *testing.T) {
	spec := readBulkDeleteSpec(t)
	request, ok := spec.requests(t)["selection_missing"]
	if !ok {
		t.Fatal("bulkDeleteQueue names no selection_missing request example")
	}
	want, ok := spec.responses(t)["400"]["selection_missing"]
	if !ok {
		t.Fatal("bulkDeleteQueue's 400 names no selection_missing example")
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, body string }{
		{"selection_missing", string(body)},
		{"empty_ids", `{"ids":[]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s reached the database before it was refused: %v", c.body, r)
					}
				}()
				queueRoutes(NewQueueHandler(nil)).ServeHTTP(rr,
					httptest.NewRequest(http.MethodDelete, bulkDeleteQueuePath, bytes.NewReader([]byte(c.body))))
			}()
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("%s: status %d, want 400; body %s", c.body, rr.Code, rr.Body)
			}
			if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s: Content-Type %q, want application/json", c.body, ct)
			}
			assertDocumentedBody(t, c.body, rr, want)
		})
	}
}

// The 200's by_ids example is what the handler answers: the documented request
// removes the entry it names, affecting exactly one, and no other. The entry
// is seeded under the documented id inside a transaction that is rolled back.
func TestBulkDeleteQueue_ByIDsExampleAnswered(t *testing.T) {
	spec := readBulkDeleteSpec(t)
	request, ok := spec.requests(t)["by_ids"]
	if !ok {
		t.Fatal("bulkDeleteQueue names no by_ids request example")
	}
	want, ok := spec.responses(t)["200"]["by_ids"]
	if !ok {
		t.Fatal("bulkDeleteQueue's 200 names no by_ids example")
	}
	ids, _ := request.(map[string]interface{})["ids"].([]interface{})
	if len(ids) != 1 {
		t.Fatalf("by_ids names %v, want one id", ids)
	}
	documented := uint(ids[0].(float64))

	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Skipf("bulk-delete test skipped: migrate failed (%v)", err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	for _, id := range []uint{documented, documented + 1} {
		if err := tx.Delete(&models.OutboundQueue{}, id).Error; err != nil {
			t.Fatalf("clear queue entry %d: %v", id, err)
		}
		if err := tx.Create(&models.OutboundQueue{ID: id, Sender: "s@x.test", Recipient: "r@y.test", Domain: "y.test"}).Error; err != nil {
			t.Fatalf("seed queue entry %d: %v", id, err)
		}
	}

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	queueRoutes(NewQueueHandler(tx)).ServeHTTP(rr,
		httptest.NewRequest(http.MethodDelete, bulkDeleteQueuePath, bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("%s: status %d, want 200; body %s", body, rr.Code, rr.Body)
	}
	assertDocumentedBody(t, request, rr, want)

	var left []uint
	if err := tx.Model(&models.OutboundQueue{}).Where("id IN ?", []uint{documented, documented + 1}).Order("id").Pluck("id", &left).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, []uint{documented + 1}) {
		t.Errorf("entries left after the documented request: %v, want only %d", left, documented+1)
	}
}

// The pair checks reject a spec that drops the request or the 400, names the
// pair differently, documents another body, or pairs the refused request with
// success.
func TestBulkDeleteQueue_SelectionMissingPairMutationsRejected(t *testing.T) {
	read := func() bulkDeleteSpec { return readBulkDeleteSpec(t) }
	setExamples := func(c bulkContent, name string, value interface{}) bulkContent {
		examples := map[string]struct {
			Value interface{} `yaml:"value"`
		}{}
		for k, v := range c.Examples {
			examples[k] = v
		}
		if value == nil {
			delete(examples, name)
		} else {
			examples[name] = struct {
				Value interface{} `yaml:"value"`
			}{value}
		}
		c.Examples = examples
		return c
	}
	renamed := func(c bulkContent, from, to string) bulkContent {
		examples := map[string]struct {
			Value interface{} `yaml:"value"`
		}{}
		for k, v := range c.Examples {
			if k == from {
				k = to
			}
			examples[k] = v
		}
		c.Examples = examples
		return c
	}
	request := func(s *bulkDeleteSpec, f func(bulkContent) bulkContent) {
		s.RequestBody.Content = map[string]bulkContent{"application/json": f(s.RequestBody.Content["application/json"])}
	}
	response := func(s *bulkDeleteSpec, status string, f func(bulkContent) bulkContent) {
		responses := map[string]struct {
			Content map[string]bulkContent `yaml:"content"`
		}{}
		for k, v := range s.Responses {
			responses[k] = v
		}
		r := responses[status]
		r.Content = map[string]bulkContent{"application/json": f(r.Content["application/json"])}
		responses[status] = r
		s.Responses = responses
	}
	errorBody := func(message string) interface{} {
		return map[interface{}]interface{}{"error": map[interface{}]interface{}{"code": "bad_request", "message": message}}
	}

	if problems := bulkDeletePairProblems(t, read()); len(problems) != 0 {
		t.Fatalf("the served spec is rejected before any mutation: %v", problems)
	}

	for _, c := range []struct {
		name   string
		mutate func(*bulkDeleteSpec)
	}{
		{"api_key_auth_missing", func(s *bulkDeleteSpec) {
			s.Security = s.Security[:2]
		}},
		{"api_key_capability_changed", func(s *bulkDeleteSpec) {
			s.Security[2] = map[string][]string{"APIKeyAuth": {"queue:read"}}
		}},
		{"request_missing", func(s *bulkDeleteSpec) {
			request(s, func(c bulkContent) bulkContent { return setExamples(c, "selection_missing", nil) })
		}},
		{"request_misnamed", func(s *bulkDeleteSpec) {
			request(s, func(c bulkContent) bulkContent { return renamed(c, "selection_missing", "missing") })
		}},
		{"request_not_empty", func(s *bulkDeleteSpec) {
			request(s, func(c bulkContent) bulkContent {
				return setExamples(c, "selection_missing", map[interface{}]interface{}{"ids": []interface{}{}})
			})
		}},
		{"original_request_changed", func(s *bulkDeleteSpec) {
			request(s, func(c bulkContent) bulkContent {
				return setExamples(c, "by_ids", map[interface{}]interface{}{"ids": []interface{}{1}})
			})
		}},
		{"unnamed_example_returns", func(s *bulkDeleteSpec) {
			request(s, func(c bulkContent) bulkContent {
				c.Example = map[interface{}]interface{}{"ids": []interface{}{987654}}
				return c
			})
		}},
		{"response_400_missing", func(s *bulkDeleteSpec) {
			responses := map[string]struct {
				Content map[string]bulkContent `yaml:"content"`
			}{}
			for k, v := range s.Responses {
				if k != "400" {
					responses[k] = v
				}
			}
			s.Responses = responses
		}},
		{"response_400_example_missing", func(s *bulkDeleteSpec) {
			response(s, "400", func(c bulkContent) bulkContent { return setExamples(c, "selection_missing", nil) })
		}},
		{"response_400_misnamed", func(s *bulkDeleteSpec) {
			response(s, "400", func(c bulkContent) bulkContent { return renamed(c, "selection_missing", "missing") })
		}},
		{"response_400_wrong_body", func(s *bulkDeleteSpec) {
			response(s, "400", func(c bulkContent) bulkContent {
				return setExamples(c, "selection_missing", errorBody("Invalid request body"))
			})
		}},
		{"response_400_wrong_schema", func(s *bulkDeleteSpec) {
			response(s, "400", func(c bulkContent) bulkContent {
				c.Schema.Ref = "#/components/schemas/ValidationErrorResponse"
				return c
			})
		}},
		{"refused_request_paired_with_success", func(s *bulkDeleteSpec) {
			response(s, "200", func(c bulkContent) bulkContent {
				return setExamples(c, "selection_missing", map[interface{}]interface{}{"data": map[interface{}]interface{}{"affected": 0}})
			})
		}},
		{"response_200_example_missing", func(s *bulkDeleteSpec) {
			response(s, "200", func(c bulkContent) bulkContent { return setExamples(c, "by_ids", nil) })
		}},
		{"response_200_schema_changed", func(s *bulkDeleteSpec) {
			response(s, "200", func(c bulkContent) bulkContent { c.Schema.Required = nil; return c })
		}},
		{"security_changed", func(s *bulkDeleteSpec) {
			s.Security = []map[string][]string{{"CookieAuth": {"queue:read"}}}
		}},
		{"body_not_required", func(s *bulkDeleteSpec) { s.RequestBody.Required = false }},
		{"operation_renamed", func(s *bulkDeleteSpec) { s.OperationID = "bulkRemoveQueue" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := read()
			c.mutate(&s)
			if problems := bulkDeletePairProblems(t, s); len(problems) == 0 {
				t.Errorf("a spec with %s is accepted", c.name)
			}
		})
	}
}
