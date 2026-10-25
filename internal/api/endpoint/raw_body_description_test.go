package endpoint

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func rawBodyDescriptionCase[Req any](t *testing.T, contentType, body, query string, status int, validation bool, handler func(*http.Request, Req) (string, error), explicit ...int) {
	t.Helper()
	router, registry := chi.NewRouter(), NewRegistry()
	Register(Routes{Router: router, Registry: registry}, Operation{Method: http.MethodPost, Path: "/body", OperationID: "body", Errors: explicit}, handler)
	declared := false
	for _, response := range registry.Endpoints()[0].Responses {
		declared = declared || response.Status == 422
	}
	if declared != validation {
		t.Fatalf("422 declared=%v, want %v", declared, validation)
	}
	req := httptest.NewRequest(http.MethodPost, "/body"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != status {
		t.Fatalf("got %d, want %d: %s", rr.Code, status, rr.Body.String())
	}
}

func TestRawBodyValidationDescriptionsMatchRealBinding(t *testing.T) {
	t.Run("optional XML is copied, not parsed", func(t *testing.T) {
		type request struct {
			Body *string `body:"application/xml"`
		}
		for _, body := range []string{"", "not XML", "\x00"} {
			rawBodyDescriptionCase(t, "application/xml", body, "", 200, false, func(_ *http.Request, in request) (string, error) {
				if body == "" {
					if in.Body != nil {
						t.Fatal("absent optional body is not nil")
					}
				} else if in.Body == nil || *in.Body != body {
					t.Fatal("raw body was not bound exactly")
				}
				return "copied", nil
			})
		}
	})
	t.Run("raw bytes are copied", func(t *testing.T) {
		type request struct {
			Body []byte `body:"application/octet-stream"`
		}
		rawBodyDescriptionCase(t, "application/octet-stream", "\x00raw", "", 200, false, func(_ *http.Request, in request) (string, error) {
			if string(in.Body) != "\x00raw" {
				t.Fatal("binary bytes changed")
			}
			return "copied", nil
		})
	})
	t.Run("raw media alternatives retain 415", func(t *testing.T) {
		type request struct {
			XML  *string `body:"application/xml"`
			Text *string `body:"text/plain"`
		}
		rawBodyDescriptionCase(t, "application/json", "{}", "", 415, false, func(*http.Request, request) (string, error) {
			t.Fatal("unsupported media reached handler")
			return "", nil
		})
	})
	t.Run("mixed JSON form retains validation", func(t *testing.T) {
		rawBodyDescriptionCase(t, "application/json", `{"name":"too long a widget name"}`, "", 422, true, func(*http.Request, uploadWidget) (string, error) {
			t.Fatal("invalid JSON reached handler")
			return "", nil
		})
	})
	t.Run("JSON suffix retains validation", func(t *testing.T) {
		type request struct {
			Body createWidget `body:"application/widget+json"`
		}
		rawBodyDescriptionCase(t, "application/widget+json", `{"name":"too long a widget name"}`, "", 422, true, func(*http.Request, request) (string, error) { t.Fatal("invalid JSON reached handler"); return "", nil })
	})
	t.Run("default JSON retains validation", func(t *testing.T) {
		type request struct {
			Body createWidget `body:""`
		}
		rawBodyDescriptionCase(t, "application/json", `{"name":"too long a widget name"}`, "", 422, true, func(*http.Request, request) (string, error) { t.Fatal("invalid JSON reached handler"); return "", nil })
	})
	t.Run("query rules still refuse raw requests", func(t *testing.T) {
		type request struct {
			Body  *string `body:"application/xml"`
			Count int     `query:"count"`
		}
		rawBodyDescriptionCase(t, "application/xml", "text", "?count=invalid", 422, true, func(*http.Request, request) (string, error) { t.Fatal("invalid query reached handler"); return "", nil })
	})
	t.Run("explicit handler rules remain", func(t *testing.T) {
		type request struct {
			Body *string `body:"application/xml"`
		}
		rawBodyDescriptionCase(t, "application/xml", "text", "", 422, true, func(*http.Request, request) (string, error) {
			return "", Invalid(map[string]string{"body": "handler refusal"})
		}, 422)
	})
}
