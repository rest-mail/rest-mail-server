package endpoint

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHostParameterUsesRequestAuthority(t *testing.T) {
	type request struct {
		Host string `header:"hOsT"`
	}
	router := chi.NewRouter()
	Register(Routes{Router: router, Registry: NewRegistry()}, Operation{Method: http.MethodGet, Path: "/host", OperationID: "host"}, func(r *http.Request, in request) (string, error) {
		if in.Host != r.Host {
			t.Errorf("bound Host=%q, request authority=%q", in.Host, r.Host)
		}
		return in.Host, nil
	})
	for _, host := range []string{"mta-sts.example.com", "MTA-STS.EXAMPLE.COM.:443", "mta-sts.xn--bcher-kva.example"} {
		req := httptest.NewRequest(http.MethodGet, "/host", nil)
		req.Host = host
		req.Header.Set("Host", "ignored.invalid")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
	}
}

func headerDescriptionCase[Req any](t *testing.T, value string, want int, validation bool, handler func(*http.Request, Req) (string, error), explicit ...int) {
	t.Helper()
	router, registry := chi.NewRouter(), NewRegistry()
	Register(Routes{Router: router, Registry: registry}, Operation{Method: http.MethodGet, Path: "/header", OperationID: "header", Errors: explicit}, handler)
	declared := false
	for _, response := range registry.Endpoints()[0].Responses {
		declared = declared || response.Status == 422
	}
	if declared != validation {
		t.Fatalf("422 declared=%v, want %v", declared, validation)
	}
	req := httptest.NewRequest(http.MethodGet, "/header", nil)
	if value != "" {
		req.Header.Set("X-Value", value)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("got %d, want %d: %s", rr.Code, want, rr.Body.String())
	}
}

func TestHeaderValidationDescriptionsMatchRealBinding(t *testing.T) {
	t.Run("plain optional headers remain bound", func(t *testing.T) {
		type request struct {
			Value string `header:"X-Value"`
		}
		for _, value := range []string{"", "any text"} {
			headerDescriptionCase(t, value, 200, false, func(r *http.Request, in request) (string, error) {
				if in.Value != r.Header.Get("X-Value") {
					t.Fatal("header was not bound")
				}
				return in.Value, nil
			})
		}
	})
	t.Run("required", func(t *testing.T) {
		type request struct {
			Value string `header:"X-Value,required"`
		}
		headerDescriptionCase(t, "", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("missing header reached handler")
			return "", nil
		})
	})
	t.Run("enum", func(t *testing.T) {
		type request struct {
			Value string `header:"X-Value" enums:"one,two"`
		}
		headerDescriptionCase(t, "three", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("invalid header reached handler")
			return "", nil
		})
	})
	t.Run("length", func(t *testing.T) {
		type request struct {
			Value string `header:"X-Value" minLength:"2" maxLength:"4"`
		}
		headerDescriptionCase(t, "longer", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("invalid header reached handler")
			return "", nil
		})
	})
	t.Run("number", func(t *testing.T) {
		type request struct {
			Value int `header:"X-Value"`
		}
		headerDescriptionCase(t, "text", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("invalid header reached handler")
			return "", nil
		})
	})
	t.Run("custom parser", func(t *testing.T) {
		type request struct {
			Value rejectingQueryText `header:"X-Value"`
		}
		headerDescriptionCase(t, "text", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("invalid header reached handler")
			return "", nil
		})
	})
	t.Run("handler refusal", func(t *testing.T) {
		type request struct {
			Value string `header:"X-Value"`
		}
		headerDescriptionCase(t, "text", 422, true, func(*http.Request, request) (string, error) {
			return "", Invalid(map[string]string{"value": "refused by handler"})
		}, 422)
	})
}
