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

// The bulk retry and bounce operations pair their refused requests with their
// 400s the way bulkDeleteQueue does (#386): each refusal is a named request
// example named on the 400 and nowhere else, and the original request is
// by_ids, named on the 200.
var bulkQueuePairs = []struct {
	operationID, path string
	// The status a seeded entry is in, so the documented request affects it.
	seedStatus, wantStatus string
}{
	{"bulkRetryQueue", "/api/v1/admin/queue/bulk-retry", "deferred", "pending"},
	{"bulkBounceQueue", "/api/v1/admin/queue/bulk-bounce", "deferred", "bounced"},
}

// readBulkPostSpec is the POST on path as the spec the API serves documents it.
func readBulkPostSpec(t *testing.T, path string) bulkDeleteSpec {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths map[string]struct {
			Post *bulkDeleteSpec `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	op := doc.Paths[path].Post
	if op == nil {
		t.Fatalf("the spec has no POST %s", path)
	}
	return *op
}

// bulkPostPairProblems says what is wrong with how operationID pairs its
// refused requests with its 400, and keeps the successful contract as it was.
func bulkPostPairProblems(t *testing.T, operationID string, s bulkDeleteSpec) []string {
	t.Helper()
	var problems []string
	add := func(format string, args ...interface{}) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if s.OperationID != operationID {
		add("the operation is %q, want %s", s.OperationID, operationID)
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
	if got := names(requests); !reflect.DeepEqual(got, []string{"by_ids", "invalid_body", "invalid_ids", "selection_missing"}) {
		add("the request examples are %v, want by_ids, invalid_body, invalid_ids and selection_missing", got)
	}
	if got, want := requests["by_ids"], map[string]interface{}{"ids": []interface{}{float64(987654)}}; !reflect.DeepEqual(got, want) {
		add("by_ids is %s, want the original request %s", mustJSON(got), mustJSON(want))
	}
	if got, want := requests["invalid_body"], []interface{}{}; !reflect.DeepEqual(got, want) {
		add("invalid_body is %s, want an empty array", mustJSON(got))
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
	ok := s.Responses["200"].Content["application/json"]
	if ok.Schema.Type != "object" || !reflect.DeepEqual(ok.Schema.Required, []string{"data"}) ||
		ok.Schema.Properties["data"].Ref != "#/components/schemas/BulkQueueResponse" {
		add("the 200's schema is no longer an object whose data is a BulkQueueResponse")
	}
	if got := names(responses["200"]); !reflect.DeepEqual(got, []string{"by_ids"}) {
		add("the 200 names %v, want only by_ids", got)
	}
	if ref := s.Responses["400"].Content["application/json"].Schema.Ref; ref != "#/components/schemas/ErrorResponse" {
		add("the 400's schema is %q, want ErrorResponse", ref)
	}
	if got := names(responses["400"]); !reflect.DeepEqual(got, []string{"invalid_body", "selection_missing"}) {
		add("the 400 names %v, want only invalid_body and selection_missing", got)
	}
	return problems
}

func TestBulkQueue_RefusalPairs(t *testing.T) {
	for _, op := range bulkQueuePairs {
		t.Run(op.operationID, func(t *testing.T) {
			for _, problem := range bulkPostPairProblems(t, op.operationID, readBulkPostSpec(t, op.path)) {
				t.Error(problem)
			}
		})
	}
}

// Each named refusal is answered by the real handler with exactly the
// documented 400. The handler has no database: a query built would panic, so
// each 400 is proved to come before any entry is selected.
func TestBulkQueue_RefusalPairsRefused(t *testing.T) {
	for _, op := range bulkQueuePairs {
		spec := readBulkPostSpec(t, op.path)
		requests, refusals := spec.requests(t), spec.responses(t)["400"]
		for _, name := range []string{"invalid_body", "selection_missing"} {
			t.Run(op.operationID+"/"+name, func(t *testing.T) {
				request, ok := requests[name]
				if !ok {
					t.Fatalf("%s names no %s request example", op.operationID, name)
				}
				want, ok := refusals[name]
				if !ok {
					t.Fatalf("%s's 400 names no %s example", op.operationID, name)
				}
				body, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				rr := httptest.NewRecorder()
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s reached the database before it was refused: %v", body, r)
						}
					}()
					queueRoutes(NewQueueHandler(nil)).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, op.path, bytes.NewReader(body)))
				}()
				if rr.Code != http.StatusBadRequest {
					t.Fatalf("%s: status %d, want 400; body %s", body, rr.Code, rr.Body)
				}
				if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("%s: Content-Type %q, want application/json", body, ct)
				}
				assertDocumentedBody(t, request, rr, want)
			})
		}
	}
}

// The 200's by_ids example is what the handler answers: the documented request
// affects the entry it names, exactly one, and no other. The entries are
// seeded inside a transaction that is rolled back.
func TestBulkQueue_ByIDsExampleAnswered(t *testing.T) {
	gdb := openAuthzTestDB(t)
	if err := gdb.AutoMigrate(&models.OutboundQueue{}); err != nil {
		t.Skipf("bulk queue test skipped: migrate failed (%v)", err)
	}
	for _, op := range bulkQueuePairs {
		t.Run(op.operationID, func(t *testing.T) {
			spec := readBulkPostSpec(t, op.path)
			request, ok := spec.requests(t)["by_ids"]
			if !ok {
				t.Fatalf("%s names no by_ids request example", op.operationID)
			}
			want, ok := spec.responses(t)["200"]["by_ids"]
			if !ok {
				t.Fatalf("%s's 200 names no by_ids example", op.operationID)
			}
			ids, _ := request.(map[string]interface{})["ids"].([]interface{})
			if len(ids) != 1 {
				t.Fatalf("by_ids names %v, want one id", ids)
			}
			documented := uint(ids[0].(float64))

			tx := gdb.Begin()
			t.Cleanup(func() { tx.Rollback() })
			for _, id := range []uint{documented, documented + 1} {
				if err := tx.Delete(&models.OutboundQueue{}, id).Error; err != nil {
					t.Fatalf("clear queue entry %d: %v", id, err)
				}
				entry := models.OutboundQueue{ID: id, Sender: "s@x.test", Recipient: "r@y.test", Domain: "y.test", Status: op.seedStatus}
				if err := tx.Create(&entry).Error; err != nil {
					t.Fatalf("seed queue entry %d: %v", id, err)
				}
			}

			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			queueRoutes(NewQueueHandler(tx)).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, op.path, bytes.NewReader(body)))
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status %d, want 200; body %s", body, rr.Code, rr.Body)
			}
			assertDocumentedBody(t, request, rr, want)

			statuses := map[uint]string{}
			var rows []models.OutboundQueue
			if err := tx.Where("id IN ?", []uint{documented, documented + 1}).Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				statuses[row.ID] = row.Status
			}
			wantStatuses := map[uint]string{documented: op.wantStatus, documented + 1: op.seedStatus}
			if !reflect.DeepEqual(statuses, wantStatuses) {
				t.Errorf("entries after the documented request: %v, want %v", statuses, wantStatuses)
			}
		})
	}
}

// The pair checks reject a spec that drops a refused request, pairs it with
// success, or keeps an unnamed example beside the named ones.
func TestBulkQueue_RefusalPairMutationsRejected(t *testing.T) {
	for _, op := range bulkQueuePairs {
		if problems := bulkPostPairProblems(t, op.operationID, readBulkPostSpec(t, op.path)); len(problems) != 0 {
			t.Fatalf("%s: the served spec is rejected before any mutation: %v", op.operationID, problems)
		}
		for _, c := range []struct {
			name   string
			mutate func(*bulkDeleteSpec)
		}{
			{"request_missing", func(s *bulkDeleteSpec) {
				c := s.RequestBody.Content["application/json"]
				c.Examples = map[string]struct {
					Value interface{} `yaml:"value"`
				}{"by_ids": c.Examples["by_ids"], "selection_missing": c.Examples["selection_missing"]}
				s.RequestBody.Content = map[string]bulkContent{"application/json": c}
			}},
			{"unnamed_example_returns", func(s *bulkDeleteSpec) {
				c := s.RequestBody.Content["application/json"]
				c.Example = map[interface{}]interface{}{"ids": []interface{}{987654}}
				s.RequestBody.Content = map[string]bulkContent{"application/json": c}
			}},
			{"refused_request_paired_with_success", func(s *bulkDeleteSpec) {
				responses := map[string]struct {
					Content map[string]bulkContent `yaml:"content"`
				}{}
				for k, v := range s.Responses {
					responses[k] = v
				}
				r := responses["200"]
				c := r.Content["application/json"]
				examples := map[string]struct {
					Value interface{} `yaml:"value"`
				}{}
				for k, v := range c.Examples {
					examples[k] = v
				}
				examples["invalid_body"] = struct {
					Value interface{} `yaml:"value"`
				}{map[interface{}]interface{}{"data": map[interface{}]interface{}{"affected": 0}}}
				c.Examples = examples
				r.Content = map[string]bulkContent{"application/json": c}
				responses["200"] = r
				s.Responses = responses
			}},
			{"body_not_required", func(s *bulkDeleteSpec) { s.RequestBody.Required = false }},
		} {
			t.Run(op.operationID+"/"+c.name, func(t *testing.T) {
				s := readBulkPostSpec(t, op.path)
				c.mutate(&s)
				if problems := bulkPostPairProblems(t, op.operationID, s); len(problems) == 0 {
					t.Errorf("a spec with %s is accepted", c.name)
				}
			})
		}
	}
}
