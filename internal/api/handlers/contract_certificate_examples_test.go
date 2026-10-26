package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

// Binding rejects malformed bodies with 400 and broken field rules with 422,
// before a database lookup. Exercise the named examples through registration.
func TestCertificateCreate_DocumentedBadRequests(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/certificates", "createCertificate")
	wantPairs := map[string][]string{
		"request": {"fields_missing", "invalid_body", "valid"},
		"201":     {"valid"}, "400": {"invalid_body"}, "422": {"fields_missing"},
	}
	gotPairs := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		gotPairs[status] = names(examples)
	}
	if !reflect.DeepEqual(gotPairs, wantPairs) {
		t.Fatalf("example pairs %v, want %v", gotPairs, wantPairs)
	}
	for _, status := range []string{"400", "422"} {
		for name, want := range responses[status] {
			rr, reachedDB := createCertificateWithoutDB(string(mustJSON(requests[name])))
			if reachedDB || fmt.Sprint(rr.Code) != status {
				t.Fatalf("%s reached DB %v, status %d: %s", name, reachedDB, rr.Code, rr.Body)
			}
			var got interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s answered %s, want %s", name, rr.Body, mustJSON(want))
			}
		}
	}
	for _, tc := range []struct {
		body   string
		status int
		field  string
	}{
		{"not json", 400, ""}, {`[]`, 400, ""}, {`null`, 400, ""}, {`"cert"`, 400, ""}, {`1`, 400, ""}, {`false`, 400, ""},
		{`{"domain_id":"1","cert_pem":"cert","key_pem":"key"}`, 422, "domain_id"},
		{`{"domain_id":-1,"cert_pem":"cert","key_pem":"key"}`, 422, "domain_id"},
		{`{"domain_id":0,"cert_pem":"cert","key_pem":"key"}`, 422, "domain_id"},
		{`{"domain_id":null,"cert_pem":"cert","key_pem":"key"}`, 422, "domain_id"},
		{`{"domain_id":1,"cert_pem":42,"key_pem":"key"}`, 422, "cert_pem"},
		{`{"domain_id":1,"cert_pem":"","key_pem":"key"}`, 422, "cert_pem"},
		{`{"domain_id":1,"cert_pem":"cert","key_pem":false}`, 422, "key_pem"},
		{`{"domain_id":1,"cert_pem":"cert","key_pem":""}`, 422, "key_pem"},
	} {
		rr, reachedDB := createCertificateWithoutDB(tc.body)
		if reachedDB || rr.Code != tc.status {
			t.Errorf("%s reached DB %v, status %d, want %d: %s", tc.body, reachedDB, rr.Code, tc.status, rr.Body)
			continue
		}
		if tc.field != "" {
			var got struct {
				Error struct {
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error.Details.Fields[tc.field] == "" {
				t.Errorf("%s: field %s absent from %s", tc.body, tc.field, rr.Body)
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
// The 422 names the broken field rules.
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
	if want := []string{"201", "400", "422", "429"}; !reflect.DeepEqual(statuses, want) {
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
	certificateRoutes(NewCertificateHandler(nil, "")).ServeHTTP(rr, req)
	return rr, false
}
