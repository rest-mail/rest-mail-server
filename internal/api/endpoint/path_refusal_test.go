package endpoint

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type parsedPathText string

func (p *parsedPathText) UnmarshalText(raw []byte) error {
	if string(raw) != "ok" {
		return errors.New("not ok")
	}
	*p = parsedPathText(raw)
	return nil
}

func pathRefusalCase[R any](t *testing.T, valid, invalid string, want422 bool) {
	t.Helper()
	router := chi.NewRouter()
	registry := NewRegistry()
	calls := 0
	Register(Routes{Router: router, Registry: registry}, Operation{Method: http.MethodGet, Path: "/x/{value}", Status: http.StatusNoContent}, func(*http.Request, R) (Empty, error) { calls++; return Empty{}, nil })
	declared := false
	for _, response := range registry.Endpoints()[0].Responses {
		if response.Status == 422 {
			declared = true
		}
	}
	if declared != want422 {
		t.Fatalf("declared422=%v, want %v", declared, want422)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x/"+valid, nil))
	if rr.Code != 204 || calls != 1 {
		t.Fatalf("valid segment: %d, calls=%d", rr.Code, calls)
	}
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x/"+invalid, nil))
	want := 404
	if want422 {
		want = 422
	}
	if rr.Code != want || calls != 1 {
		t.Fatalf("bad/missing segment: %d, calls=%d, want %d", rr.Code, calls, want)
	}
}

func TestPathSegmentsOnlyDeclareReachableBindingRefusals(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		pathRefusalCase[struct {
			Value string `path:"value"`
		}](t, "ok", "", false)
	})
	t.Run("nonempty", func(t *testing.T) {
		pathRefusalCase[struct {
			Value string `path:"value" minLength:"1"`
		}](t, "ok", "", false)
	})
	t.Run("longer", func(t *testing.T) {
		pathRefusalCase[struct {
			Value string `path:"value" minLength:"2"`
		}](t, "ok", "x", true)
	})
	t.Run("enum", func(t *testing.T) {
		pathRefusalCase[struct {
			Value string `path:"value" enums:"ok,no"`
		}](t, "ok", "bad", true)
	})
	t.Run("parsed", func(t *testing.T) {
		pathRefusalCase[struct {
			Value parsedPathText `path:"value"`
		}](t, "ok", "bad", true)
	})
	t.Run("integer", func(t *testing.T) {
		pathRefusalCase[struct {
			Value uint32 `path:"value"`
		}](t, "1", "bad", true)
	})
}

func TestExplicitPathRefusalsRemainDeclared(t *testing.T) {
	router := chi.NewRouter()
	registry := NewRegistry()
	Register(Routes{Router: router, Registry: registry}, Operation{Method: http.MethodGet, Path: "/x/{value}", Status: http.StatusNoContent, Errors: []int{422}}, func(_ *http.Request, in struct {
		Value string `path:"value"`
	}) (Empty, error) {
		return Empty{}, Invalid(map[string]string{"value": "refused"})
	})
	declared := false
	for _, r := range registry.Endpoints()[0].Responses {
		declared = declared || r.Status == 422
	}
	if !declared {
		t.Fatal("explicit refusal omitted")
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x/text", nil))
	if rr.Code != 422 {
		t.Fatalf("explicit refusal: %d", rr.Code)
	}
}
