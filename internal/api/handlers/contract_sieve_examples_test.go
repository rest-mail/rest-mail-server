package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	yaml "go.yaml.in/yaml/v2"
)

// Keep the reader local to Sieve: other contract slices also add PUT readers.
func sieveExamples(t *testing.T, method, path, operationID string) (map[string]interface{}, map[string]map[string]interface{}) {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type operationExamples struct {
		OperationID string `yaml:"operationId"`
		RequestBody struct {
			Content map[string]namedExamples `yaml:"content"`
		} `yaml:"requestBody"`
		Responses map[string]struct {
			Content map[string]namedExamples `yaml:"content"`
		} `yaml:"responses"`
	}
	var doc struct {
		Paths map[string]struct {
			Put  operationExamples `yaml:"put"`
			Post operationExamples `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatal(err)
	}
	op := doc.Paths[path].Put
	if method == "post" {
		op = doc.Paths[path].Post
	}
	if op.OperationID != operationID {
		t.Fatalf("%s %s is %q, want %s", method, path, op.OperationID, operationID)
	}
	requests := map[string]interface{}{}
	for name, example := range op.RequestBody.Content["application/json"].Examples {
		requests[name] = jsonValue(t, example.Value)
	}
	responses := map[string]map[string]interface{}{}
	for status, response := range op.Responses {
		for name, example := range response.Content["application/json"].Examples {
			if responses[status] == nil {
				responses[status] = map[string]interface{}{}
			}
			responses[status][name] = jsonValue(t, example.Value)
		}
	}
	return requests, responses
}

func TestSieve_ExamplePairs(t *testing.T) {
	for _, method := range []string{"put", "post"} {
		t.Run(method, func(t *testing.T) {
			path, operation := "/api/v1/accounts/{id}/sieve", "putSieveScript"
			if method == "post" {
				path, operation = path+"/validate", "validateSieveScript"
			}
			requests, responses := sieveExamples(t, method, path, operation)
			want := map[string][]string{
				"request": {"script_missing", "script_null", "valid"},
				"200":     {"valid"},
				"422":     {"script_missing", "script_null"},
			}
			if method == "put" {
				want["201"] = []string{"valid"}
			}
			got := map[string][]string{"request": names(requests)}
			for status, examples := range responses {
				got[status] = names(examples)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s examples = %v, want %v", operation, got, want)
			}
			missing := map[string]interface{}{}
			null := map[string]interface{}{"script": nil}
			if method == "put" {
				missing["active"], null["active"] = true, true
			}
			if !reflect.DeepEqual(requests["script_missing"], missing) ||
				!reflect.DeepEqual(requests["script_null"], null) {
				t.Fatalf("refused requests = %s, %s", mustJSON(requests["script_missing"]), mustJSON(requests["script_null"]))
			}
		})
	}
}

func TestSieve_DocumentedExamples(t *testing.T) {
	db := openAuthzTestDB(t)
	for _, method := range []string{"put", "post"} {
		t.Run(method, func(t *testing.T) {
			tx := db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() { tx.Rollback() })
			fx := seedAuthzFixture(t, tx)
			h := NewSieveHandler(tx)
			handle := func(w http.ResponseWriter, r *http.Request) { serveSieve(h, w, r) }
			path, operation, verb := "/api/v1/accounts/{id}/sieve", "putSieveScript", http.MethodPut
			if method == "post" {
				path, operation, verb = path+"/validate", "validateSieveScript", http.MethodPost
			}
			requests, responses := sieveExamples(t, method, path, operation)
			send := func(name string) *httptest.ResponseRecorder {
				t.Helper()
				body, err := json.Marshal(requests[name])
				if err != nil {
					t.Fatal(err)
				}
				rr := httptest.NewRecorder()
				handle(rr, authzRequest(verb, strings.TrimPrefix(path, "/api/v1/accounts/{id}"), fx.ownerMailbox.ID,
					map[string]string{"id": uintToStr(fx.ownerMailbox.ID)}, string(body)))
				return rr
			}
			// Refusals must neither create a script nor alter an existing one.
			var baseline models.SieveScript
			for _, stored := range []bool{false, true} {
				var before models.SieveScript
				if stored {
					var rr *httptest.ResponseRecorder
					if method == "put" {
						rr = send("valid")
					} else {
						rr = putSieve(t, h, fx.ownerMailbox.ID, `{"script":"keep;","active":true}`)
					}
					if rr.Code != http.StatusCreated {
						t.Fatalf("create: %d %s", rr.Code, rr.Body)
					}
					if rr := putSieve(t, h, fx.ownerMailbox.ID, `{"script":"keep;","active":false}`); rr.Code != http.StatusOK {
						t.Fatalf("seed inactive: %d %s", rr.Code, rr.Body)
					}
					if err := tx.Where("mailbox_id = ?", fx.ownerMailbox.ID).First(&before).Error; err != nil {
						t.Fatal(err)
					}
					if before.Active {
						t.Fatal("seed script must be inactive")
					}
					baseline = before
				}
				for _, name := range []string{"script_missing", "script_null"} {
					rr := send(name)
					if rr.Code != http.StatusUnprocessableEntity {
						t.Fatalf("%s: %d %s", name, rr.Code, rr.Body)
					}
					var got interface{}
					if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, responses["422"][name]) {
						t.Fatalf("%s: got %s, want %s", name, rr.Body, mustJSON(responses["422"][name]))
					}
					if !stored {
						if count := sieveRowCount(t, h, fx.ownerMailbox.ID); count != 0 {
							t.Fatalf("%s created %d scripts", name, count)
						}
					} else {
						var after models.SieveScript
						if err := tx.First(&after, before.ID).Error; err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(after, before) {
							t.Fatalf("%s changed stored script: got %+v, want %+v", name, after, before)
						}
					}
				}
			}
			rr := send("valid")
			if rr.Code != http.StatusOK {
				t.Fatalf("valid: %d %s", rr.Code, rr.Body)
			}
			var after models.SieveScript
			if err := tx.Where("mailbox_id = ?", fx.ownerMailbox.ID).First(&after).Error; err != nil {
				t.Fatal(err)
			}
			if method == "put" {
				want := requests["valid"].(map[string]interface{})["script"].(string)
				if after.Script != want || after.ScriptHash != filters.HashSieveScript(want) || after.ValidatedAt == nil || !after.Active {
					t.Fatalf("valid PUT did not store the validated script: %+v", after)
				}
			} else {
				var got interface{}
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, responses["200"]["valid"]) || !reflect.DeepEqual(after, baseline) {
					t.Fatalf("valid POST: response %s, stored %+v", rr.Body, after)
				}
			}
		})
	}
}
