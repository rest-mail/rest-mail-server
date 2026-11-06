package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// unsubscribeOperations are the unsubscribe routes (#1575) and their methods.
var unsubscribeOperations = map[string]struct{ method, path string }{
	"getUnsubscribeOptions": {http.MethodGet, "/api/v1/messages/{id}/unsubscribe"},
	"unsubscribeOneClick":   {http.MethodPost, "/api/v1/messages/{id}/unsubscribe/one-click"},
	"unsubscribeMailto":     {http.MethodPost, "/api/v1/messages/{id}/unsubscribe/mailto"},
}

// Every example pair of the unsubscribe routes is answered as written by the router, set up
// as its requires names, on the message routes' in-process fixtures.
func TestUnsubscribeExamples(t *testing.T) {
	scenarios := messageSourceReadScenarios(t)
	scenarios["read_only"] = scenarios["key_read"]
	scenarios["malformed_json"] = scenarios["found"]
	scenarios["no_targets"] = scenarios["found"]
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range unsubscribeOperations {
		if len(pairs[operation]) < 8 {
			t.Fatalf("incomplete examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			if unsubscribeTargetExample(operation, name) {
				continue
			}
			t.Run(operation+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, operation, name)
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				var body []byte
				if len(pair.Request.Body) > 0 {
					body = pair.Request.Body
				}
				if name == "malformed_json" {
					// The pair's body is the JSON string "{": the wire carries that one byte.
					body = []byte("{")
				}
				if name == "request_too_large" && len(body) < 9 {
					body = []byte("123456789")
				}
				req := httptest.NewRequest(route.method, messageCollectionReadURI(route.path, pair), bytes.NewReader(body))
				if body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				if name == "rate_limited" {
					retry, err := strconv.Atoi(rr.Header().Get("Retry-After"))
					if err != nil || retry < 1 {
						t.Fatal("refusal lost Retry-After")
					}
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
}
