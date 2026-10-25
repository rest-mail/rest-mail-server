package endpoint

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type rejectingQueryText string

func (*rejectingQueryText) UnmarshalText([]byte) error { return errors.New("refused text") }

func queryDescriptionCase[Req any](t *testing.T, query string, want int, validation bool, handler func(*http.Request, Req) (string, error), errors ...int) string {
	t.Helper()
	router, registry := chi.NewRouter(), NewRegistry()
	Register(Routes{Router: router, Registry: registry}, Operation{Method: http.MethodGet, Path: "/query", OperationID: "query", Errors: errors}, handler)
	declared := false
	for _, response := range registry.Endpoints()[0].Responses {
		declared = declared || response.Status == http.StatusUnprocessableEntity
	}
	if declared != validation {
		t.Fatalf("validation response declared=%v, want %v", declared, validation)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/query"+query, nil))
	if rr.Code != want {
		t.Fatalf("got %d, want %d: %s", rr.Code, want, rr.Body.String())
	}
	return rr.Body.String()
}

func TestQueryValidationDescriptionsMatchRealBinding(t *testing.T) {
	t.Run("unconstrained strings are still bound", func(t *testing.T) {
		type request struct {
			Value string `query:"value" description:"Any text"`
		}
		for _, query := range []string{"", "?value=not-a-number", "?value=%00"} {
			queryDescriptionCase(t, query, http.StatusOK, false, func(r *http.Request, in request) (string, error) {
				if in.Value != r.URL.Query().Get("value") {
					t.Fatal("the query was described but not bound")
				}
				return in.Value, nil
			})
		}
	})
	t.Run("required strings retain refusal", func(t *testing.T) {
		type request struct {
			Value string `query:"value,required"`
		}
		queryDescriptionCase(t, "", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("required query reached handler")
			return "", nil
		})
	})
	t.Run("enums retain refusal", func(t *testing.T) {
		type request struct {
			Value string `query:"value" enums:"one,two"`
		}
		queryDescriptionCase(t, "?value=three", 422, true, func(*http.Request, request) (string, error) { t.Fatal("invalid enum reached handler"); return "", nil })
	})
	t.Run("length rules retain refusal", func(t *testing.T) {
		type request struct {
			Value string `query:"value" minLength:"2" maxLength:"4"`
		}
		queryDescriptionCase(t, "?value=longer", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("invalid length reached handler")
			return "", nil
		})
	})
	t.Run("patterns retain refusal", func(t *testing.T) {
		type request struct {
			Value string `query:"value" pattern:"^yes$"`
		}
		queryDescriptionCase(t, "?value=no", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("invalid pattern reached handler")
			return "", nil
		})
	})
	t.Run("formats retain refusal", func(t *testing.T) {
		type request struct {
			Value string `query:"value" format:"date-time"`
		}
		queryDescriptionCase(t, "?value=never", 422, true, func(*http.Request, request) (string, error) { t.Fatal("invalid time reached handler"); return "", nil })
	})
	t.Run("numbers retain refusal", func(t *testing.T) {
		type request struct {
			Value int `query:"value"`
		}
		queryDescriptionCase(t, "?value=text", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("invalid number reached handler")
			return "", nil
		})
	})
	t.Run("custom parsers retain refusal", func(t *testing.T) {
		type request struct {
			Value rejectingQueryText `query:"value"`
		}
		queryDescriptionCase(t, "?value=text", 422, true, func(*http.Request, request) (string, error) {
			t.Fatal("refused custom text reached handler")
			return "", nil
		})
	})
	t.Run("handler rules remain explicit", func(t *testing.T) {
		type request struct {
			Value string `query:"value"`
		}
		queryDescriptionCase(t, "?value=refused", 422, true, func(*http.Request, request) (string, error) {
			return "", InvalidQuery(map[string]string{"value": "refused by handler"})
		}, 422)
	})
}
