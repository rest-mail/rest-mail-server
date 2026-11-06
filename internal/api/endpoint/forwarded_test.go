package endpoint

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
)

type upstreamFailure struct {
	Error string `json:"error"`
}

type upstreamAnswer struct {
	Result interface{} `json:"result"`
	Logs   []string    `json:"logs"`
}

func TestForwardedPreservesUpstreamStatusAndBytes(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusRequestTimeout} {
		r, reg := server(t)
		const body = " {\"result\":{\"custom\":true}, \"logs\":null}\n"
		Register(Routes{Router: r, Registry: reg}, Operation{Method: http.MethodGet, Path: "/upstream", OperationID: "upstream", Responses: []oapifly.EndpointResponse{{Status: http.StatusRequestTimeout, Type: oapifly.TypeOf[upstreamFailure]()}}},
			func(*http.Request, struct{}) (Forwarded[upstreamAnswer], error) {
				return Forwarded[upstreamAnswer]{Status: status, Body: []byte(body)}, nil
			})
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/upstream", nil))
		if rr.Code != status || rr.Body.String() != body || rr.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("forwarded answer = %d %q %q", rr.Code, rr.Header(), rr.Body.String())
		}
		// Its declared success is the upstream type, without a data envelope.
		endpoints := reg.Endpoints()
		ep := endpoints[len(endpoints)-1]
		if ep.Responses[0].Type != oapifly.TypeOf[upstreamAnswer]() {
			t.Fatalf("response type = %v", ep.Responses[0].Type)
		}
		foundTimeout := false
		for _, response := range ep.Responses {
			if response.Status == http.StatusRequestTimeout {
				foundTimeout = response.Type == oapifly.TypeOf[upstreamFailure]()
			}
		}
		if !foundTimeout {
			t.Fatal("missing typed upstream timeout response")
		}
		if strings.Contains(rr.Body.String(), `"data"`) {
			t.Fatal("unexpected data envelope")
		}
	}
}

// The writer's normalization reaches a list through a pointer, a status wrapper and a nested
// list, and a list left nil answers []. A pointer to a slice is the one shape it cannot help:
// nil is its null, so the description guard (TestNoSuccessListIsNullable) refuses that type.
func TestWriterAnswersListsAcrossPointersAndWrappers(t *testing.T) {
	type row struct {
		Tags []string `json:"tags"`
	}
	type answer struct {
		Rows []row    `json:"rows"`
		More []string `json:"more"`
	}
	for name, register := range map[string]func(Routes){
		"value": func(rs Routes) {
			Register(rs, Operation{Method: http.MethodGet, Path: "/lists"}, func(*http.Request, struct{}) (answer, error) {
				return answer{Rows: []row{{}}}, nil
			})
		},
		"pointer": func(rs Routes) {
			Register(rs, Operation{Method: http.MethodGet, Path: "/lists"}, func(*http.Request, struct{}) (*answer, error) {
				return &answer{Rows: []row{{}}}, nil
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := chi.NewRouter()
			register(Routes{Router: r, Registry: NewRegistry()})
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/lists", nil))
			if want := `{"data":{"rows":[{"tags":[]}],"more":[]}}`; strings.TrimSpace(rr.Body.String()) != want {
				t.Fatalf("got %s, want %s", rr.Body, want)
			}
		})
	}
}
