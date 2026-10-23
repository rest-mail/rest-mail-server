package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

// Every route the router serves is registered with endpoint.Register, which describes it by
// the types it executes, or it is on unregisteredRoutes. That list only shrinks: as each
// area moves over (#630 to #652) its routes leave it, until it is empty (#669).
func TestEveryRouteIsRegistered(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{CORSAllowedOrigins: []string{"http://localhost:3000"}, Environment: "test"}
	routers := NewRouters(newFailingGormDB(t), jwtSvc, cfg, nil)
	t.Cleanup(routers.Close)

	routes, ok := routers.Public.(chi.Routes)
	if !ok {
		t.Fatal("the public router no longer exposes its registration table")
	}
	served := map[string]bool{}
	if err := chi.Walk(routes, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[method+" "+path] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	allowed := map[string]bool{}
	for _, route := range unregisteredRoutes {
		if allowed[route] {
			t.Errorf("%s is on unregisteredRoutes twice", route)
		}
		allowed[route] = true
	}
	registered := map[string]bool{}
	for _, route := range routers.Registry.Routes() {
		registered[route] = true
	}

	var problems []string
	for route := range served {
		switch {
		case registered[route] && allowed[route]:
			problems = append(problems, route+" is registered now: take it off unregisteredRoutes")
		case !registered[route] && !allowed[route]:
			problems = append(problems, route+" is served without endpoint.Register")
		}
	}
	for route := range allowed {
		if !served[route] {
			problems = append(problems, route+" is on unregisteredRoutes but no longer served: take it off")
		}
	}
	for route := range registered {
		if !served[route] {
			problems = append(problems, route+" is registered but the router does not serve it")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Errorf("the router and the registry disagree:\n%s", strings.Join(problems, "\n"))
	}
}
