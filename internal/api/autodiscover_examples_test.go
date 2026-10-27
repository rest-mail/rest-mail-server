package api

import (
	"encoding/json"
	"github.com/restmail/restmail/internal/examplepairs"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/api/handlers"
)

const autodiscoverExamplePath = "/autodiscover/autodiscover.xml"

var autodiscoverExampleCases = map[string]struct {
	router string
	status int
}{
	"valid": {"available", 200}, "query_fallback": {"available", 200},
	"malformed": {"available", 400}, "address_missing": {"available", 400}, "address_invalid": {"available", 400},
	"domain_not_found": {"domain_not_found", 404}, "oversized": {"available", 413},
	"internal_error": {"internal_error", 500}, "mail_host_unconfigured": {"mail_host_unconfigured", 503},
}

func TestAutodiscoverExamplesMatchRegisteredResponses(t *testing.T) {
	routers := autoconfigExampleRouters(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range autodiscoverExampleCases {
		t.Run(name, func(t *testing.T) {
			pair, ok := pairs["microsoftAutodiscover"][name]
			if !ok {
				t.Fatal("named Autodiscover pair is missing")
			}
			var body string
			if err := json.Unmarshal(pair.Request.Body, &body); err != nil {
				t.Fatal(err)
			}
			query := url.Values{}
			for key, value := range pair.Request.Query {
				query.Set(key, value.(string))
			}
			for _, unknownLength := range []bool{false, true} {
				req := httptest.NewRequest(http.MethodPost, autodiscoverExamplePath+"?"+query.Encode(), strings.NewReader(body))
				req.Header.Set("Content-Type", pair.Request.MediaType)
				if unknownLength {
					req.ContentLength = -1
				}
				rr := httptest.NewRecorder()
				routers[tc.router].ServeHTTP(rr, req)
				if rr.Code != tc.status {
					t.Fatalf("unknown length=%v: got %d, want %d: %s", unknownLength, rr.Code, tc.status, rr.Body.String())
				}
				media, params, err := mime.ParseMediaType(rr.Header().Get("Content-Type"))
				if err != nil {
					t.Fatal(err)
				}
				if media != "application/json" && params["charset"] != "utf-8" {
					t.Fatal("text/XML charset is not UTF-8")
				}
				if media != pair.Response.MediaType || rr.Code != pair.Response.Status {
					t.Fatal("pair status or media type differs")
				}
				if media == "application/json" {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				} else {
					var expected string
					if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
						t.Fatal(err)
					}
					if expected != rr.Body.String() {
						t.Fatalf("complete body differs: got %q, want %q", rr.Body.String(), expected)
					}
				}
			}
			if tc.status == 413 && len(body) != handlers.AutodiscoverMaxBody+1 {
				t.Fatal("oversized example is not exactly one byte past the real cap")
			}
		})
	}
	for _, warning := range describedRouters(t).generator("Autodiscover examples").Report() {
		if warning.OperationID == "microsoftAutodiscover" {
			t.Errorf("Autodiscover still has a warning: %v", warning)
		}
	}
}

func TestAutodiscoverBodyAtTheLimitStillWorks(t *testing.T) {
	body := `<Autodiscover><Request><EMailAddress>user@example.com</EMailAddress></Request></Autodiscover>`
	body += strings.Repeat(" ", handlers.AutodiscoverMaxBody-len(body))
	router := autoconfigExampleRouters(t)["available"]
	for _, length := range []int64{-1, handlers.AutodiscoverMaxBody} {
		req := httptest.NewRequest(http.MethodPost, autodiscoverExamplePath, strings.NewReader(body))
		req.ContentLength = length
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("exact-cap body (length %d): %d %s", length, rr.Code, rr.Body.String())
		}
	}
}

func TestAutodiscoverExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for the real Autodiscover boundary check")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers := autoconfigExampleRouters(t)
	for name, tc := range autodiscoverExampleCases {
		t.Run(name, func(t *testing.T) {
			checkRegisteredExamplesWithVertrag(t, binary, routers[tc.router], "", http.MethodPost, strconv.Itoa(tc.status), []string{autodiscoverExamplePath}, 1, false, name)
		})
	}
}
