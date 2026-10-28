package handlers

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/restmail/restmail"
	yaml "go.yaml.in/yaml/v2"
)

const autodiscoverPath = "/autodiscover/autodiscover.xml"

// autodiscoverSpec is microsoftAutodiscover as the API serves it: whether it
// is public, whether its body is optional, and each response's media types.
type autodiscoverSpec struct {
	OperationID string        `yaml:"operationId"`
	Security    []interface{} `yaml:"security"`
	RequestBody struct {
		Required bool `yaml:"required"`
		Content  map[string]struct {
			Example  interface{} `yaml:"example"`
			Examples map[string]struct {
				Value string `yaml:"value"`
			} `yaml:"examples"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
	Responses map[string]struct {
		Content map[string]struct {
			Schema struct {
				Type string   `yaml:"type"`
				Enum []string `yaml:"enum"`
			} `yaml:"schema"`
			Example  interface{} `yaml:"example"`
			Examples map[string]struct {
				Value interface{} `yaml:"value"`
			} `yaml:"examples"`
		} `yaml:"content"`
	} `yaml:"responses"`
}

func readAutodiscoverSpec(t *testing.T) autodiscoverSpec {
	t.Helper()
	served, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read the spec: %v", err)
	}
	var doc struct {
		Paths struct {
			Autodiscover struct {
				Post *autodiscoverSpec `yaml:"post"`
			} `yaml:"/autodiscover/autodiscover.xml"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(served, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	op := doc.Paths.Autodiscover.Post
	if op == nil || op.OperationID != "microsoftAutodiscover" {
		t.Fatalf("the spec has no microsoftAutodiscover POST on %s", autodiscoverPath)
	}
	return *op
}

// The 400 is http.Error's: plain text, one of the handler's three messages
// with its newline. The 200 stays the XML document, the operation stays
// public, and its body stays optional, since the query parameter stands in
// for it.
func TestMicrosoftAutodiscover_Documents400(t *testing.T) {
	op := readAutodiscoverSpec(t)
	if op.Security == nil || len(op.Security) != 0 || op.RequestBody.Required {
		t.Fatal("Autodiscover must remain public with an optional body")
	}
	if _, ok := op.Responses["200"].Content["application/xml"]; !ok {
		t.Fatal("missing XML success")
	}
	if _, ok := op.Responses["400"].Content["application/json"]; !ok {
		t.Fatal("missing JSON bad_request")
	}
	if _, ok := op.Responses["422"].Content["application/json"]; !ok {
		t.Fatal("missing JSON validation_failed")
	}
}

// Each refusal comes from the real handler, with exactly a documented body,
// before anything is looked up: the handler has no database, so a lookup
// would panic.
func TestMicrosoftAutodiscover_RefusesBeforeLookup(t *testing.T) {
	for _, tc := range []struct {
		name, query, body, field, message string
		status                            int
	}{
		{"no_body", "", "", "", "invalid request", 400},
		{"malformed", "", "<Autodiscover><Request>", "", "invalid request", 400},
		{"email_missing", "", "<Autodiscover><Request/></Autodiscover>", "EMailAddress", "email address required", 422},
		{"email_invalid", "", "<Autodiscover><Request><EMailAddress>invalid</EMailAddress></Request></Autodiscover>", "EMailAddress", "invalid email address", 422},
		{"query_email_invalid", "?emailaddress=invalid", "", "emailaddress", "invalid email address", 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			autoconfigRoutes(NewAutoconfigHandler(nil, "")).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, autodiscoverPath+tc.query, strings.NewReader(tc.body)))
			if rr.Code != tc.status {
				t.Fatalf("got %d: %s", rr.Code, rr.Body)
			}
			if rr.Header().Get("Content-Type") != "application/json" {
				t.Fatal(rr.Header())
			}
			want := map[string]interface{}{"error": map[string]interface{}{"code": "bad_request", "message": tc.message}}
			if tc.status == 422 {
				want = parseShapeJSON(t, pipelineValidation(map[string]string{tc.field: tc.message})).(map[string]interface{})
			}
			if !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
				t.Fatalf("got %s, want %s", rr.Body, mustJSON(want))
			}
		})
	}
}

// Named requests and responses must remain paired even though the request is
// XML and the refusal is plain text. Send the spec's malformed XML unchanged
// to the real handler, with no database: it must be refused before lookup.
func TestMicrosoftAutodiscover_ExamplePairs(t *testing.T) {
	op := readAutodiscoverSpec(t)
	request := op.RequestBody.Content["application/xml"]
	if request.Example != nil || len(request.Examples) != 2 {
		t.Fatal(request)
	}
	malformed := request.Examples["malformed"].Value
	want := jsonValue(t, op.Responses["400"].Content["application/json"].Examples["malformed"].Value)
	rr := httptest.NewRecorder()
	autoconfigRoutes(NewAutoconfigHandler(nil, "")).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, autodiscoverPath, strings.NewReader(malformed)))
	if rr.Code != 400 || !reflect.DeepEqual(decodeShapeBody(t, rr), want) {
		t.Fatalf("got %d %s, want %s", rr.Code, rr.Body, mustJSON(want))
	}
}
