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
				Value string `yaml:"value"`
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

	if op.Security == nil || len(op.Security) != 0 {
		t.Errorf("security is %v, want [] (Outlook asks before it has signed in)", op.Security)
	}
	if op.RequestBody.Required {
		t.Error("the request body is required, want optional (emailaddress is the fallback)")
	}

	ok, found := op.Responses["200"].Content["application/xml"]
	if !found || ok.Schema.Type != "string" || len(op.Responses["200"].Content) != 1 {
		t.Errorf("the 200 is %+v, want only an application/xml string", op.Responses["200"].Content)
	}

	bad := op.Responses["400"].Content
	if len(bad) != 1 {
		t.Fatalf("the 400 documents %d media types, want only text/plain", len(bad))
	}
	plain, found := bad["text/plain"]
	if !found {
		t.Fatalf("the 400 documents %v, want text/plain", bad)
	}
	want := []string{"invalid request\n", "email address required\n", "invalid email address\n"}
	if plain.Schema.Type != "string" || !reflect.DeepEqual(plain.Schema.Enum, want) {
		t.Errorf("the 400's schema is %q %q, want a string, one of %q", plain.Schema.Type, plain.Schema.Enum, want)
	}
	if plain.Example != nil || len(plain.Examples) != 1 || plain.Examples["malformed"].Value != "invalid request\n" {
		t.Errorf("the 400's examples are %+v, want only malformed: invalid request\n", plain.Examples)
	}
}

// Each refusal comes from the real handler, with exactly a documented body,
// before anything is looked up: the handler has no database, so a lookup
// would panic.
func TestMicrosoftAutodiscover_RefusesBeforeLookup(t *testing.T) {
	documented := map[string]bool{}
	for _, body := range readAutodiscoverSpec(t).Responses["400"].Content["text/plain"].Schema.Enum {
		documented[body] = true
	}

	for _, c := range []struct {
		name  string
		query string
		body  string
		want  string
	}{
		{"no_body", "", "", "invalid request\n"},
		{"malformed", "", "<Autodiscover><Request>", "invalid request\n"},
		{"not_autodiscover", "", "<Other/>", "invalid request\n"},
		{"email_missing", "", "<Autodiscover><Request></Request></Autodiscover>", "email address required\n"},
		{"email_empty", "", "<Autodiscover><Request><EMailAddress></EMailAddress></Request></Autodiscover>", "email address required\n"},
		{"email_invalid", "", "<Autodiscover><Request><EMailAddress>eve.restmail.test</EMailAddress></Request></Autodiscover>", "invalid email address\n"},
		{"query_email_invalid", "?emailaddress=eve.restmail.test", "", "invalid email address\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s reached the database before it was refused: %v", c.name, r)
					}
				}()
				NewAutoconfigHandler(nil, "").MicrosoftAutodiscover(rr,
					httptest.NewRequest(http.MethodPost, autodiscoverPath+c.query, strings.NewReader(c.body)))
			}()
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body %q", rr.Code, rr.Body)
			}
			if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type %q, want text/plain; charset=utf-8", ct)
			}
			if got := rr.Body.String(); got != c.want {
				t.Errorf("body %q, want %q", got, c.want)
			}
			if !documented[rr.Body.String()] {
				t.Errorf("body %q is not one the spec's 400 documents", rr.Body)
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
		t.Fatalf("request examples are %+v, want only named valid and malformed", request)
	}
	valid, hasValid := request.Examples["valid"]
	malformed, hasMalformed := request.Examples["malformed"]
	if !hasValid || !hasMalformed || !strings.Contains(valid.Value, "<EMailAddress>eve@restmail.test</EMailAddress>") {
		t.Fatalf("request examples are %+v, want valid fixture XML and malformed XML", request.Examples)
	}
	ok := op.Responses["200"].Content["application/xml"]
	if _, found := ok.Examples["valid"]; !found || ok.Example != nil || len(ok.Examples) != 1 {
		t.Fatalf("200 examples are %+v, want only named valid", ok)
	}
	want := op.Responses["400"].Content["text/plain"].Examples["malformed"].Value
	if malformed.Value == "" || want == "" {
		t.Fatal("malformed request and response need nonempty examples")
	}
	rr := httptest.NewRecorder()
	NewAutoconfigHandler(nil, "").MicrosoftAutodiscover(rr,
		httptest.NewRequest(http.MethodPost, autodiscoverPath, strings.NewReader(malformed.Value)))
	if rr.Code != http.StatusBadRequest || rr.Body.String() != want {
		t.Fatalf("malformed XML answered %d %q, want 400 %q", rr.Code, rr.Body.String(), want)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type %q, want text/plain; charset=utf-8", ct)
	}
}
