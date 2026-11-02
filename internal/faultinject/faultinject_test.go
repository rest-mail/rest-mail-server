//go:build faultinject

package faultinject

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// answering serves body as JSON on every route.
func answering(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Kept", "yes")
		_, _ = w.Write([]byte(body))
	})
}

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// Built with every fault's tag (the CI step does), each fault changes the
// answer on its own route and nowhere else.
func TestEachCompiledFaultBreaksItsRoute(t *testing.T) {
	if len(registry) == 0 {
		t.Skip("no fault tag in this build; CI adds every fault tag from tests/contract/faults.yml")
	}
	for _, name := range Active() {
		f := registry[name]
		t.Run(name, func(t *testing.T) {
			h := wrap(answering(f.Example), []Fault{f})
			good := serve(answering(f.Example), f.Method, f.Path)
			broken := serve(h, f.Method, f.Path)
			if broken.Body.String() == good.Body.String() &&
				broken.Header().Get("Content-Type") == good.Header().Get("Content-Type") {
				t.Fatalf("%s left %s %s answering as before: %s", name, f.Method, f.Path, broken.Body)
			}
			if broken.Header().Get("X-Kept") != "yes" {
				t.Fatalf("%s lost the route's other headers", name)
			}
			other := serve(h, http.MethodGet, "/api/elsewhere")
			if other.Body.String() != f.Example {
				t.Fatalf("%s changed another route: %s", name, other.Body)
			}
		})
	}
}

func TestTheShapeBreakers(t *testing.T) {
	cases := []struct {
		name    string
		breaker func(*httptest.ResponseRecorder)
		body    string
		want    string
		header  string
	}{
		{"rename", renameField("endpoint", "url"), `{"endpoint":"x"}`, `{"url":"x"}`, "application/json"},
		{"rename leaves an answer without the field", renameField("endpoint", "url"), `{"error":"e"}`, `{"error":"e"}`, "application/json"},
		{"replace", replaceField("data", []any{}), `{"data":{"status":"ok"}}`, `{"data":[]}`, "application/json"},
		{"not JSON is left alone", replaceField("data", []any{}), `plain`, `plain`, "application/json"},
		{"media type", setMediaType("text/plain"), `{"a":1}`, `{"a":1}`, "text/plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Fault{Name: c.name, Method: http.MethodGet, Path: "/x", Break: c.breaker}
			got := serve(wrap(answering(c.body), []Fault{f}), http.MethodGet, "/x")
			if got.Body.String() != c.want {
				t.Fatalf("body %s, want %s", got.Body, c.want)
			}
			if got.Header().Get("Content-Type") != c.header {
				t.Fatalf("Content-Type %q, want %q", got.Header().Get("Content-Type"), c.header)
			}
		})
	}
}

func TestNoFaultLeavesTheHandler(t *testing.T) {
	next := http.NewServeMux()
	if got := wrap(next, nil); got != http.Handler(next) {
		t.Fatal("wrap with no fault replaced the handler")
	}
}
