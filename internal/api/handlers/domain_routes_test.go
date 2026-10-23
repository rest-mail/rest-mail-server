package handlers

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
)

// domainRoutes serves h's domain routes as the router registers them, behind capability
// checks that let every request through, for tests of the handlers themselves.
func domainRoutes(h *DomainHandler) http.Handler {
	r := chi.NewRouter()
	pass := func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	}
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()},
		DomainRouteChecks{At: pass, OnList: pass, OnDomain: pass})
	return r
}

// validSendingPolicy reports whether both domain request types allow p, as the enums tag that
// binding enforces says.
func validSendingPolicy(p string) bool {
	for _, t := range []reflect.Type{reflect.TypeOf(createDomainRequest{}), reflect.TypeOf(updateDomainBody{})} {
		f, _ := t.FieldByName("SendingPolicy")
		allowed := false
		for _, value := range strings.Split(f.Tag.Get("enums"), ",") {
			if value == p {
				allowed = true
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}
