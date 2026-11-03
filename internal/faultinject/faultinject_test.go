//go:build faultinject

package faultinject

import (
	"encoding/json"
	"io"
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
	return serveBody(h, method, path, "")
}

func serveBody(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
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
			good := serveBody(next, f.Method, f.Path, f.ExampleRequest)
			broken := serveBody(h, f.Method, f.Path, f.ExampleRequest)
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
		{"discover_missing_status_ok", http.StatusNotFound, http.StatusOK},
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

// A BreakSeeing fault is given what the client sent, and the route still gets it.
func TestBreakSeeingGetsTheRequestBodyAndLeavesItForTheRoute(t *testing.T) {
	var routeGot, faultGot string
	route := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		routeGot = string(body)
	})
	f := Fault{Method: http.MethodPost, Path: "/x", BreakSeeing: func(sent []byte, _ *httptest.ResponseRecorder) {
		faultGot = string(sent)
	}}
	serveBody(wrap(route, []Fault{f}), http.MethodPost, "/x", `{"a":1}`)
	if routeGot != `{"a":1}` || faultGot != `{"a":1}` {
		t.Fatalf("the route got %q and the fault %q", routeGot, faultGot)
	}
}

// Only a refused password change that sent the wrong current password answers
// differently, and nothing but the message changes, so it still fits its schema.
func TestRefusalEchoOnlyChangesAWrongPasswordsMessage(t *testing.T) {
	fault, active := registry["change_password_refusal_echoes_password"]
	if !active {
		t.Skip("built without fault_change_password_refusal_echoes_password")
	}
	answer := func(status int, body, request string) *httptest.ResponseRecorder {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		})
		return serveBody(wrap(next, []Fault{fault}), fault.Method, fault.Path, request)
	}
	refused := answer(http.StatusForbidden, fault.Example, fault.ExampleRequest)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", refused.Code)
	}
	var got struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(refused.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != "invalid_password" || !strings.Contains(got.Error.Message, "not-the-password") {
		t.Fatalf("the refusal did not carry the password: %s", refused.Body)
	}
	for _, unchanged := range []struct {
		status        int
		body, request string
	}{
		{http.StatusOK, `{"data":{"changed":true}}`, fault.ExampleRequest},
		{http.StatusForbidden, `{"error":{"code":"two_factor_required","message":"m"}}`, fault.ExampleRequest},
		{http.StatusUnprocessableEntity, `{"error":{"code":"validation_failed","message":"m"}}`, fault.ExampleRequest},
		{http.StatusForbidden, fault.Example, `{"new_password":"a new password of my own"}`},
		{http.StatusForbidden, fault.Example, `not JSON`},
	} {
		rec := answer(unchanged.status, unchanged.body, unchanged.request)
		if rec.Code != unchanged.status || rec.Body.String() != unchanged.body {
			t.Fatalf("%d %s for %s became %d %s", unchanged.status, unchanged.body, unchanged.request, rec.Code, rec.Body)
		}
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
		{"replace with null", replaceField("data", nil), `{"data":[{"id":1}]}`, `{"data":null}`, "application/json"},
		{"null leaves a refusal without the field", replaceField("data", nil), `{"error":{"code":"e"}}`, `{"error":{"code":"e"}}`, "application/json"},
		{"add to data", addDataField("storage_ref", "x"), `{"data":{"db":"up"}}`, `{"data":{"db":"up","storage_ref":"x"}}`, "application/json"},
		{"add leaves a list under data", addDataField("storage_ref", "x"), `{"data":[]}`, `{"data":[]}`, "application/json"},
		{"add leaves a refusal without data", addDataField("storage_ref", "x"), `{"error":{"code":"e"}}`, `{"error":{"code":"e"}}`, "application/json"},
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
