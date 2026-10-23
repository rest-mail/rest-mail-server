package handlers

import (
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/version"
)

// APIRoot is the body of GET /api: what this API is, and where a client starts. Its fields
// are in the order the map it replaced was written in, so the bytes are unchanged.
type APIRoot struct {
	BuildDate string       `json:"build_date"`
	Commit    string       `json:"commit"`
	Links     APIRootLinks `json:"links"`
	Name      string       `json:"name"`
	Version   string       `json:"version"`
}

// APIRootLinks are where a client goes next: liveness, the description, and signing in.
type APIRootLinks struct {
	Docs   string `json:"docs"`
	Health string `json:"health"`
	Login  string `json:"login"`
}

func apiRoot(*http.Request, struct{}) (APIRoot, error) {
	return APIRoot{
		BuildDate: version.BuildDate,
		Commit:    version.Commit,
		Links: APIRootLinks{
			Docs:   "/api/docs",
			Health: "/api/health",
			Login:  "/api/v1/auth/login",
		},
		Name:    "restmail",
		Version: version.Version,
	}, nil
}

// RegisterSystem mounts what a client reads first: the API root, and what this server is.
// throttle is the owner endpoints' per-IP limit, whose bucket GET /api/v1/server shares so
// that a script polling it cannot use up the budget anyone needs to sign in.
func RegisterSystem(rs endpoint.Routes, throttle endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api",
		OperationID: "apiRoot", Summary: "API discovery — name, version and key links", Tags: []string{"System"},
		Security: oapifly.Public}, apiRoot)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/server",
		OperationID: "describeServer", Summary: "What this server is", Tags: []string{"Auth"},
		Security: oapifly.Public, Middleware: []endpoint.Middleware{throttle}}, describeServer)
}

// Register mounts the liveness probe. It needs no credential; a bearer token for a session
// holding domains:read at "*" adds the certificates that are about to expire.
func (h *HealthHandler) Register(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/health",
		OperationID: "getHealth", Summary: "Health check", Tags: []string{"Health"},
		Security: oapifly.Public, Errors: []int{http.StatusServiceUnavailable}}, h.health)
}
