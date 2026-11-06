package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// importSessionPaths are the two operations that open and close an import (#1498).
var importSessionPaths = map[string]struct{ method, path string }{
	"createImportSession": {http.MethodPost, "/api/v1/accounts/{id}/messages/import-sessions"},
	"closeImportSession":  {http.MethodDelete, "/api/v1/accounts/{id}/messages/import-sessions/{session}"},
}

// importSessionModes are the import fixture's modes that set up each pair needing
// setup. The pairs with no requirement are the live contract run's to prove.
var importSessionModes = map[string]string{
	"unauthenticated":   "",
	"api_key_ceiling":   "key_ceiling",
	"missing":           "disappeared",
	"request_too_large": "body_limit",
	"rate_limited":      "",
	"store_failure":     "",
	"unavailable":       "scope_failure",
	"malformed_json":    "",
}

// The import-session pairs that need a principal, a body, a limit, a state or a
// fault are sent through the real router over the import fixture's storage. The
// fixture answers only the account lookups an import makes, so a session's own
// row is the database fault store_failure names: nothing is written.
func TestImportSessionExamples(t *testing.T) {
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, route := range importSessionPaths {
		if len(pairs[op]) < 10 {
			t.Fatalf("incomplete %s: %d pairs", op, len(pairs[op]))
		}
		for name, pair := range pairs[op] {
			if len(pair.Requires) == 0 {
				continue
			}
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				mode, ok := importSessionModes[name]
				if !ok {
					t.Fatalf("no scenario for %s", name)
				}
				f := newImportFixture(t, mode)
				uri := messageCollectionReadURI(route.path, pair)
				var body []byte
				switch {
				case name == "malformed_json":
					body = []byte("{")
				case len(pair.Request.Body) > 0:
					body = append([]byte(nil), pair.Request.Body...)
				}
				if name == "request_too_large" && len(body) <= 8 {
					body = []byte(`{"x":"over"}`)
				}
				req := httptest.NewRequest(route.method, uri, bytes.NewReader(body))
				if body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				handler := f.router
				switch name {
				case "unauthenticated":
				case "rate_limited":
					handler = apiKeyRefusalLimited(t)
				case "api_key_ceiling":
					req.Header.Set("Authorization", "Bearer "+f.key)
				default:
					req.Header.Set("Authorization", "Bearer "+f.token)
				}
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d; queries=%v", rr.Code, rr.Body, pair.Response.Status, f.c.queries)
				}
				if !strings.HasPrefix(rr.Header().Get("Content-Type"), pair.Response.MediaType) {
					t.Fatalf("media type %q, want %q", rr.Header().Get("Content-Type"), pair.Response.MediaType)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				if strings.Contains(rr.Body.String(), "private") || strings.Contains(rr.Body.String(), "unexpected") {
					t.Fatal("storage detail leaked")
				}
				if name == "store_failure" {
					reached := false
					for _, q := range f.c.queries {
						if strings.Contains(q, "import_sessions") {
							reached = true
						}
					}
					if !reached {
						t.Fatalf("store fault not reached at the session row: %v", f.c.queries)
					}
				}
			})
		}
	}
}
