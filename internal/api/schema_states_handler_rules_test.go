package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

// describedDocument is the description the routers generate, decoded for what these tests
// read of it: each operation's parameters and the relay body's anyOf.
type describedDocument struct {
	Paths map[string]map[string]struct {
		Parameters []struct {
			Name   string `json:"name"`
			In     string `json:"in"`
			Schema struct {
				Pattern string `json:"pattern"`
			} `json:"schema"`
		} `json:"parameters"`
		RequestBody struct {
			Content map[string]struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
	} `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

func describedDocumentOf(t *testing.T) describedDocument {
	t.Helper()
	doc, warnings := describedRouters(t).Description("schema states handler rules")
	if len(warnings) != 0 {
		t.Fatalf("description warnings: %v", warnings)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var described describedDocument
	if err := json.Unmarshal(encoded, &described); err != nil {
		t.Fatal(err)
	}
	return described
}

// validationFields is a 422's refused fields, or fails the test when the answer is not one.
func validationFields(t *testing.T, rr *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Fields map[string]string `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error.Code != "validation_failed" {
		t.Fatalf("not a validation refusal: %s (%v)", rr.Body.String(), err)
	}
	return body.Error.Details.Fields
}

// The nightly fuzz run sent emailaddress=a, which the description permitted and the handler
// refused (#1165). The description now states the @ and the dotted domain the handler
// needs, and binding refuses an address without them, before the handler runs.
func TestLookupAddressIsRefusedByItsDescription(t *testing.T) {
	described := describedDocumentOf(t)
	for path, method := range map[string]string{
		"/mail/config-v1.1.xml":                        "get",
		"/.well-known/autoconfig/mail/config-v1.1.xml": "get",
		"/autodiscover/autodiscover.xml":               "post",
	} {
		var pattern string
		for _, p := range described.Paths[path][method].Parameters {
			if p.In == "query" && p.Name == "emailaddress" {
				pattern = p.Schema.Pattern
			}
		}
		if pattern == "" {
			t.Fatalf("%s %s: emailaddress states no pattern", method, path)
		}
		re := regexp.MustCompile(pattern)
		for _, value := range []string{"", "user@example.com", "user@x.y.", "user@bücher.de", "a@b@example.com", "user@example。com"} {
			if !re.MatchString(value) {
				t.Errorf("%s %s: the pattern refuses %q, which the handler looks up", method, path, value)
			}
		}
		for _, value := range []string{"a", "user@", "user@example", "user@example.", "user@example. ", "user@x.com@"} {
			if re.MatchString(value) {
				t.Errorf("%s %s: the pattern permits %q, which the handler refuses", method, path, value)
			}
		}
	}

	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	routers := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test", GatewayHostname: "restmail.test"}, nil)
	t.Cleanup(routers.Close)
	for _, path := range []string{"/mail/config-v1.1.xml", "/.well-known/autoconfig/mail/config-v1.1.xml"} {
		rr := httptest.NewRecorder()
		routers.Public.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path+"?emailaddress=a", nil))
		if got := validationFields(t, rr)["emailaddress"]; got != "no email domain in the address or the host name" {
			t.Errorf("%s: emailaddress refused as %q", path, got)
		}
	}

	// The handler reads the XML's address and ignores the query, so it would look the
	// domain up in the failing database and answer 500. The 422 is binding's.
	req := httptest.NewRequest(http.MethodPost, "/autodiscover/autodiscover.xml?emailaddress=a",
		strings.NewReader(`<Autodiscover><Request><EMailAddress>user@example.com</EMailAddress></Request></Autodiscover>`))
	req.Header.Set("Content-Type", "application/xml")
	rr := httptest.NewRecorder()
	routers.Public.ServeHTTP(rr, req)
	if got := validationFields(t, rr)["emailaddress"]; got != "invalid email address" {
		t.Errorf("autodiscover: emailaddress refused as %q", got)
	}
}

// The nightly fuzz run sent a relay with host "", which the description permitted and the
// handler refused (#1165). The relay alternative now states models.ValidRelayHost as a
// pattern, so binding refuses the host before the handler looks the domain up: a domain
// that does not exist answers 422 for the host, where the handler would answer 404.
func TestRelayHostIsRefusedByItsDescription(t *testing.T) {
	described := describedDocumentOf(t)
	schema := described.Paths["/api/v1/admin/domains/{id}/relay"]["put"].RequestBody.Content["application/json"].Schema
	var ref struct {
		Ref string `json:"$ref"`
	}
	if json.Unmarshal(schema, &ref) == nil && strings.HasPrefix(ref.Ref, "#/components/schemas/") {
		schema = described.Components.Schemas[strings.TrimPrefix(ref.Ref, "#/components/schemas/")]
	}
	var body struct {
		AnyOf []struct {
			Properties map[string]struct {
				MaxLength float64 `json:"maxLength"`
				Pattern   string  `json:"pattern"`
			} `json:"properties"`
		} `json:"anyOf"`
	}
	if err := json.Unmarshal(schema, &body); err != nil {
		t.Fatal(err)
	}
	var stated bool
	for _, alternative := range body.AnyOf {
		if host, ok := alternative.Properties["host"]; ok {
			stated = host.MaxLength == 253 && host.Pattern != "" && !regexp.MustCompile(host.Pattern).MatchString("")
		}
	}
	if !stated {
		t.Fatalf("the relay alternative states no host rule: %s", schema)
	}

	f := newGrantsFixture(t)
	owner := f.token(t, "owner")
	missing := "/api/v1/admin/domains/" + strconv.FormatUint(uint64(f.b.ID)+1000, 10) + "/relay"
	rr := doRequest(f.router, http.MethodPut, missing, owner, `{"transport":"relay","host":"","port":587,"tls_mode":"starttls"}`)
	if got := validationFields(t, rr)["host"]; got != "Must be a bare host name or IP address, with no scheme, port or path" {
		t.Errorf("host refused as %q", got)
	}
	// Direct ignores the host, as the handler does.
	if rr := doRequest(f.router, http.MethodPut, missing, owner, `{"transport":"direct","host":""}`); rr.Code != http.StatusNotFound {
		t.Errorf("direct with an empty host = %d %s, want the missing domain's 404", rr.Code, rr.Body.String())
	}
}

// The nightly fuzz run sent domain=a, which the description permitted and the handler
// refused (#1748). The description now states the dot validateDomainName needs, and binding
// refuses a name without one with the handler's own words, still no-store.
func TestDiscoveryDomainIsRefusedByItsDescription(t *testing.T) {
	var pattern string
	for _, p := range describedDocumentOf(t).Paths["/.well-known/restmail"]["get"].Parameters {
		if p.In == "query" && p.Name == "domain" {
			pattern = p.Schema.Pattern
		}
	}
	if pattern == "" {
		t.Fatal("domain states no pattern")
	}
	re := regexp.MustCompile(pattern)
	if re.MatchString("a") {
		t.Errorf("the pattern %q permits a, which the handler refuses", pattern)
	}

	handler := publicDiscoveryRouters(t)["configured"]
	get := func(query string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/restmail?"+query, nil))
		if got := rr.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", query, got)
		}
		return rr
	}

	for query, want := range map[string]string{
		"domain=a":                       "Invalid domain query parameter",
		"domain=a.":                      "Invalid domain query parameter",
		"domain=":                        "Invalid domain query parameter",
		"domain=alice%40example.test":    "Invalid domain query parameter",
		"domain=example.test&domain=b.c": "Exactly one domain query parameter required",
		// Binding reads the first value before the handler counts them.
		"domain=a&domain=b": "Invalid domain query parameter",
	} {
		if got := validationFields(t, get(query))["domain"]; got != want {
			t.Errorf("%s: domain refused as %q, want %q", query, got, want)
		}
	}

	// The fixtures host example.test and münchen.test, whichever way they are written.
	for query, status := range map[string]int{
		"domain=example.test":              http.StatusOK,
		"domain=EXAMPLE.TEST.":             http.StatusOK,
		"domain=m%C3%BCnchen.test":         http.StatusOK,
		"domain=M%C3%9CNCHEN.TEST.":        http.StatusOK,
		"domain=xn--mnchen-3ya.test":       http.StatusOK,
		"domain=example%E3%80%82test":      http.StatusOK,
		"domain=m%C3%BCnchen%EF%BC%8Etest": http.StatusOK,
		"domain=not-hosted.test":           http.StatusNotFound,
	} {
		if got := get(query); got.Code != status {
			t.Errorf("%s: got %d, want %d: %s", query, got.Code, status, got.Body.String())
		}
	}
}
