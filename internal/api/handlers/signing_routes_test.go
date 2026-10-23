package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
)

func signingPass(string) endpoint.Middleware {
	return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
}
func dkimRoutes(h *DKIMHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, DKIMRouteChecks{At: signingPass, OnDomain: signingPass})
	return r
}
func certificateRoutes(h *CertificateHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, CertificateRouteChecks{At: signingPass})
	return r
}

// These failures must happen in typed binding before a database is touched.
// Pin the actual response bodies to the examples even without PostgreSQL.
func TestSigningBindingExamples(t *testing.T) {
	for _, tc := range []struct {
		path, operation string
		handler         http.Handler
	}{
		{"/api/v1/admin/dkim/{id}", "setDKIMKey", dkimRoutes(NewDKIMHandler(nil, ""))},
		{rotationContractPath, "setDKIMRotation", dkimRoutes(NewDKIMHandler(nil, ""))},
	} {
		requests, responses := operationExamples(t, tc.path, "put", tc.operation)
		for _, status := range []string{"400", "422"} {
			for name, want := range responses[status] {
				// Cryptographic validation needs a real domain.
				if name == "private_key_invalid" {
					continue
				}
				t.Run(tc.operation+"/"+name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodPut, strings.ReplaceAll(tc.path, "{id}", "1"), bytes.NewReader(mustJSON(requests[name])))
					rr := httptest.NewRecorder()
					tc.handler.ServeHTTP(rr, req)
					if fmt.Sprint(rr.Code) != status {
						t.Fatalf("status %d, want %s: %s", rr.Code, status, rr.Body)
					}
					if got := decodeShapeBody(t, rr); !reflect.DeepEqual(got, want) {
						t.Fatalf("got %s, want %s", rr.Body, mustJSON(want))
					}
				})
			}
		}
	}
}
