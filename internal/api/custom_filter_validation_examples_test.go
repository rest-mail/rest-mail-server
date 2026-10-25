package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

const customFilterValidationPath = "/api/v1/admin/custom-filters/validate"
const customFilterValidationToken = "public-local-validation-fixture-token"

// This HTTP fixture proves the API/sidecar boundary, not JavaScript execution.
// The running sidecar's own suite is responsible for the compiler/runtime.
type customFilterValidationCalls struct {
	mu       sync.Mutex
	requests []map[string]interface{}
}

func (c *customFilterValidationCalls) snapshot() []map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]interface{}(nil), c.requests...)
}
func newCustomFilterValidationScenarios(t *testing.T) (map[string]bulkQueueExampleScenario, *customFilterValidationCalls) {
	t.Helper()
	calls := &customFilterValidationCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/validate" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer "+customFilterValidationToken {
			t.Errorf("wrong validation sidecar method/path/content type/token")
			w.WriteHeader(401)
			return
		}
		var request map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("invalid sidecar request:%v", err)
			w.WriteHeader(400)
			return
		}
		calls.mu.Lock()
		calls.requests = append(calls.requests, request)
		calls.mu.Unlock()
		script, _ := request["script"].(string)
		w.Header().Set("Content-Type", "application/json")
		switch script {
		case "invalid-json":
			io.WriteString(w, "{")
		case "truncated-response":
			body := `{"valid":true}`
			w.Header().Set("Content-Length", strconv.Itoa(len(body)+11))
			io.WriteString(w, body)
		case "transport-unavailable":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack:%v", err)
				return
			}
			conn.Close()
		case "token-refused":
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized"}`)
		case "syntax-error":
			io.WriteString(w, `{"valid":false,"error":"Unexpected token","phase":"syntax"}`)
		case "dry-run":
			io.WriteString(w, `{"valid":true,"dry_run":{"result":{"action":"continue","score":1},"logs":["checked"]}}`)
		case "execution-error":
			io.WriteString(w, `{"valid":true,"error":"Script threw","phase":"execution","dry_run":{"error":"Script threw","logs":[]}}`)
		case "worker-error":
			w.WriteHeader(500)
			io.WriteString(w, `{"error":"Worker unavailable"}`)
		case "spaced-response":
			io.WriteString(w, " {\"valid\":true, \"dry_run\":{\"result\":{\"custom\":42},\"logs\":[]}}\n")
		default:
			io.WriteString(w, `{"valid":true}`)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("JS_FILTER_URL", server.URL)
	t.Setenv("JS_FILTER_TOKEN", customFilterValidationToken)
	out := newCustomFilterMutationScenarios(t)
	normal := out["created"]
	for _, name := range []string{"valid", "script_missing", "script_empty", "script_null", "script_wrong_type", "unknown_field", "syntax_error", "dry_run", "execution_error", "worker_error", "spaced_response", "sidecar_invalid_json", "sidecar_truncated", "sidecar_unavailable", "sidecar_token_refused", "email_null", "email_nested", "unicode_script"} {
		out[name] = normal
	}
	return out, calls
}
func customFilterValidationRequest(t *testing.T, s bulkQueueExampleScenario, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, customFilterValidationPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		r.Header.Set("Authorization", "Bearer "+s.token)
	}
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, r)
	return rr
}
func TestCustomFilterValidationTruncatedResponseIsNotSuccess(t *testing.T) {
	f, _ := newCustomFilterValidationScenarios(t)
	rr := customFilterValidationRequest(t, f["valid"], []byte(`{"script":"truncated-response"}`))
	if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Invalid sidecar response\"}}\n" {
		t.Fatalf("truncated sidecar response got%d %s want sanitized500", rr.Code, rr.Body)
	}
}
func TestCustomFilterValidationForwardedBytes(t *testing.T) {
	f, _ := newCustomFilterValidationScenarios(t)
	rr := customFilterValidationRequest(t, f["valid"], []byte(`{"script":"spaced-response"}`))
	want := " {\"valid\":true, \"dry_run\":{\"result\":{\"custom\":42},\"logs\":[]}}\n"
	if rr.Code != 200 || rr.Body.String() != want {
		t.Fatalf("forwarded%d %q want200 %q", rr.Code, rr.Body.String(), want)
	}
}
func TestCustomFilterValidationExamples(t *testing.T) {
	f, calls := newCustomFilterValidationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["validateCustomFilter"]) < 25 {
		t.Fatal("validation examples incomplete")
	}
	for name, pair := range pairs["validateCustomFilter"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			if name == "body_boundary" {
				if len(body) > 512 {
					t.Fatal("oversized boundary")
				}
				body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
			}
			before := len(calls.snapshot())
			rr := customFilterValidationRequest(t, s, body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			after := calls.snapshot()[before:]
			want := rr.Code == 200 || strings.HasPrefix(name, "sidecar_")
			if (len(after) == 1) != want || len(after) > 1 {
				t.Fatalf("sidecar calls%d want%t", len(after), want)
			}
			if len(after) == 1 {
				var request map[string]interface{}
				if err := json.Unmarshal(pair.Request.Body, &request); err != nil {
					t.Fatal(err)
				}
				if request["email"] == nil {
					delete(request, "email")
				}
				a, _ := json.Marshal(after[0])
				b, _ := json.Marshal(request)
				if !bytes.Equal(a, b) {
					t.Fatalf("sidecar payload%s want%s", a, b)
				}
			}
			if s.store != nil && len(s.store.snapshot()) != 0 {
				t.Fatal("validation accessed custom-filter storage")
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "validateCustomFilter" {
				t.Errorf("validation warning:%v", w)
			}
		}
	}
}
func TestCustomFilterValidationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for validation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newCustomFilterValidationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["validateCustomFilter"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, s, http.MethodPost, customFilterValidationPath, name, pair, 513)
		})
	}
}
