//go:build faultinject

package faultinject

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/config"
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

// Built with every fault's tag (the CI step does), each response fault changes
// only its route, and each configured limit changes its middleware's answers.
func TestEachCompiledFaultBreaksItsRoute(t *testing.T) {
	if len(registry) == 0 {
		t.Skip("no fault tag in this build; CI adds every fault tag from tests/contract/faults.yml")
	}
	for _, name := range Active() {
		f := registry[name]
		t.Run(name, func(t *testing.T) {
			if f.Configure != nil {
				testConfiguredLimit(t, f)
				return
			}
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if f.ExampleStatus != 0 {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Kept", "yes")
					w.WriteHeader(f.ExampleStatus)
				}
				answering(f.Example).ServeHTTP(w, r)
			})
			h := wrap(next, []Fault{f})
			good := serve(next, f.Method, f.Path)
			broken := serve(h, f.Method, f.Path)
			if broken.Body.String() == good.Body.String() &&
				broken.Header().Get("Content-Type") == good.Header().Get("Content-Type") &&
				broken.Code == good.Code {
				t.Fatalf("%s left %s %s answering as before: %s", name, f.Method, f.Path, broken.Body)
			}
			if broken.Header().Get("X-Kept") != "yes" {
				t.Fatalf("%s lost the route's other headers", name)
			}
			other := serve(h, http.MethodGet, "/api/elsewhere")
			if other.Body.String() != f.Example || other.Code != good.Code {
				t.Fatalf("%s changed another route: %s", name, other.Body)
			}
		})
	}
}

func TestStatusFaultsOnlyChangeTheirTargetStatus(t *testing.T) {
	cases := []struct {
		name string
		from int
		to   int
	}{
		{"health_status_created", http.StatusOK, http.StatusCreated},
		{"login_validation_removed", http.StatusUnprocessableEntity, http.StatusOK},
	}
	for _, test := range cases {
		fault, active := registry[test.name]
		if !active {
			continue
		}
		t.Run(test.name, func(t *testing.T) {
			for _, status := range []int{200, 201, 400, 401, 404, 413, 422, 429, 500} {
				rec := httptest.NewRecorder()
				rec.WriteHeader(status)
				rec.WriteString(fault.Example)
				fault.Break(rec)
				want := status
				if status == test.from {
					want = test.to
				}
				if rec.Code != want || rec.Body.String() != fault.Example {
					t.Fatalf("status %d became %d with body %s, want %d with unchanged body", status, rec.Code, rec.Body, want)
				}
			}
		})
	}
}

func testConfiguredLimit(t *testing.T, fault Fault) {
	t.Helper()
	baseline := config.Config{APIMaxBodyBytes: 32, AuthRateLimitEnabled: true, AuthRateLimitBurst: 1, AuthRateLimitRPS: 0.0001}
	broken := baseline
	fault.Configure(&broken)
	probe := func(cfg config.Config) []int {
		handler := answering(`{"data":{}}`)
		if cfg.AuthRateLimitEnabled {
			handler = middleware.RateLimit(middleware.RateLimitConfig{RPS: cfg.AuthRateLimitRPS, Burst: cfg.AuthRateLimitBurst})(handler)
		}
		handler = middleware.MaxBodyBytes(cfg.APIMaxBodyBytes)(handler)
		var statuses []int
		for _, body := range []string{`{}`, `{}`, strings.Repeat("x", 64)} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(fault.Method, fault.Path, strings.NewReader(body)))
			statuses = append(statuses, rec.Code)
		}
		return statuses
	}
	good := probe(baseline)
	if !reflect.DeepEqual(good, []int{200, 429, 413}) {
		t.Fatalf("baseline limits: %v", good)
	}
	if got := probe(broken); reflect.DeepEqual(got, good) {
		t.Fatalf("%s did not change its route's limit answers: %v", fault.Name, got)
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
