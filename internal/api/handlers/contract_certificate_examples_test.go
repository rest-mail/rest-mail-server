package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

// createCertificate refuses a body it cannot use with a 400 before it looks
// the domain up (#388). Each body here is sent to the real handler, which has
// no database, and gets exactly the 400 example documented for its reason: a
// body that reached the lookup would panic on the nil database instead.
func TestCertificateCreate_DocumentedBadRequests(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/certificates", "createCertificate")
	wantPairs := map[string][]string{
		"request": {"fields_missing", "invalid_body", "valid"},
		"201":     {"valid"},
		"400":     {"fields_missing", "invalid_body"},
	}
	gotPairs := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		gotPairs[status] = names(examples)
	}
	if !reflect.DeepEqual(gotPairs, wantPairs) {
		t.Fatalf("createCertificate's named examples are %v, want %v", gotPairs, wantPairs)
	}
	for name, want := range responses["400"] {
		body := string(mustJSON(requests[name]))
		rr, reachedDB := createCertificateWithoutDB(body)
		if reachedDB || rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: reached database %v, status %d, want 400 before lookup", body, reachedDB, rr.Code)
		}
		var got interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s answered %s, want %s", body, rr.Body, mustJSON(want))
		}
	}

	documented := responses["400"]
	if got, want := names(documented), []string{"fields_missing", "invalid_body"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("createCertificate's 400 examples are %v, want %v", got, want)
	}
	// The two reasons must stay apart: one body is not usable at all, the
	// other is well-typed JSON that leaves a required field out.
	if reflect.DeepEqual(documented["invalid_body"], documented["fields_missing"]) {
		t.Fatalf("invalid_body and fields_missing document the same body %s", mustJSON(documented["invalid_body"]))
	}

	bodies := map[string][]string{
		"invalid_body": {
			``,
			`{`,
			`not json`,
			`[]`,
			`[{"domain_id":1,"cert_pem":"cert","key_pem":"key"}]`,
			`"domain_id"`,
			`42`,
			`true`,
			`{"domain_id":"1","cert_pem":"cert","key_pem":"key"}`,
			`{"domain_id":-1,"cert_pem":"cert","key_pem":"key"}`,
			`{"domain_id":1.5,"cert_pem":"cert","key_pem":"key"}`,
			`{"domain_id":1,"cert_pem":1,"key_pem":"key"}`,
			`{"domain_id":1,"cert_pem":"cert","key_pem":["key"]}`,
			`{"domain_id":1,"cert_pem":"cert","key_pem":"key","auto_renew":"yes"}`,
		},
		"fields_missing": {
			`null`,
			`{}`,
			`{"cert_pem":"cert","key_pem":"key"}`,
			`{"domain_id":0,"cert_pem":"cert","key_pem":"key"}`,
			`{"domain_id":null,"cert_pem":"cert","key_pem":"key"}`,
			`{"domain_id":1,"key_pem":"key"}`,
			`{"domain_id":1,"cert_pem":"","key_pem":"key"}`,
			`{"domain_id":1,"cert_pem":"cert"}`,
			`{"domain_id":1,"cert_pem":"cert","key_pem":"","auto_renew":true}`,
		},
	}
	for name, cases := range bodies {
		want, ok := documented[name]
		if !ok {
			t.Fatalf("the 400 documents no %q example", name)
		}
		for _, body := range cases {
			rr, reachedDB := createCertificateWithoutDB(body)
			if reachedDB {
				t.Errorf("%q reached the database before it was refused", body)
				continue
			}
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%q: status %d, want 400; body %s", body, rr.Code, rr.Body)
				continue
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("%q: Content-Type %q, want application/json", body, ct)
			}
			var got interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("%q: decode %s: %v", body, rr.Body, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%q answered\n  %s\nthe spec documents %s as\n  %s", body, rr.Body, name, mustJSON(want))
			}
		}
	}
}

// The control for the test above: a complete, well-typed body gets past both
// refusals to the domain lookup, which the nil database turns into a panic. If
// it did not, the refusals above would prove nothing about coming first.
func TestCertificateCreate_CompleteBodyReachesTheLookup(t *testing.T) {
	rr, reachedDB := createCertificateWithoutDB(`{"domain_id":1,"cert_pem":"cert","key_pem":"key"}`)
	if !reachedDB {
		t.Fatalf("a complete body was answered %d %s without the domain lookup", rr.Code, rr.Body)
	}
}

// The success keeps its link, and the 400 keeps the shared error envelope.
// No 422 is documented: the handler never sends one.
func TestCertificateCreate_DocumentedResponses(t *testing.T) {
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	type schemaRef struct {
		Ref string `yaml:"$ref"`
	}
	var doc struct {
		Paths map[string]struct {
			Post struct {
				OperationID string `yaml:"operationId"`
				Responses   map[string]struct {
					Ref     string                 `yaml:"$ref"`
					Links   map[string]interface{} `yaml:"links"`
					Content map[string]struct {
						Schema schemaRef `yaml:"schema"`
					} `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"post"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	op := doc.Paths["/api/v1/admin/certificates"].Post
	if op.OperationID != "createCertificate" {
		t.Fatalf("POST /api/v1/admin/certificates is %q, want createCertificate", op.OperationID)
	}

	var statuses []string
	for status := range op.Responses {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	if want := []string{"201", "400"}; !reflect.DeepEqual(statuses, want) {
		t.Errorf("createCertificate documents %v, want %v", statuses, want)
	}
	if _, ok := op.Responses["201"].Links["getCertificate"]; !ok {
		t.Errorf("the 201 lost its getCertificate link")
	}
	if got, want := op.Responses["400"].Content["application/json"].Schema.Ref, "#/components/schemas/ErrorResponse"; got != want {
		t.Errorf("the 400's schema is %q, want %q", got, want)
	}
}

// createCertificateWithoutDB sends body to CreateCertificate on a handler with
// no database and no master key, and says whether it reached the database.
func createCertificateWithoutDB(body string) (rr *httptest.ResponseRecorder, reachedDB bool) {
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/certificates", strings.NewReader(body))
	defer func() {
		if recover() != nil {
			reachedDB = true
		}
	}()
	NewCertificateHandler(nil, "").CreateCertificate(rr, req)
	return rr, false
}
