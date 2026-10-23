package endpoint

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
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
