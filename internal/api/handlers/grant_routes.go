package handlers

import (
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
)

// GrantRouteChecks preserves the route checks and their order, before binding.
type GrantRouteChecks struct {
	At           func(string) endpoint.Middleware
	SessionOnly  endpoint.Middleware
	AuthThrottle endpoint.Middleware
}

type grantPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}
type activateGrantRequest struct {
	ID   uint32          `path:"id" minimum:"1" example:"1"`
	Body activateRequest `body:""`
}
type eligibleGrantRequest struct {
	ID   uint32              `path:"id" minimum:"1" example:"1"`
	Body secondFactorRequest `body:""`
}

// Register serves the permission API with the same capability, session and throttle
// checks as before. Grant scope and no-escalation checks remain inside the transactions.
func (h *GrantHandler) Register(rs endpoint.Routes, checks GrantRouteChecks) {
	tags := []string{"Admin: Roles & Grants"}
	read := []endpoint.Middleware{checks.At(middleware.CapUsersRead)}
	write := []endpoint.Middleware{checks.SessionOnly, checks.At(middleware.CapUsersWrite)}
	twoFactor := endpoint.Middleware{
		Wrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requireTwoFactor(w, middleware.GetClaims(r)) {
					next.ServeHTTP(w, r)
				}
			})
		},
		Describes: oapifly.Middleware{Responses: []oapifly.EndpointResponse{{Status: http.StatusForbidden, Description: "two_factor_required: changing grants requires a session signed in with two-factor authentication", Type: oapifly.TypeOf[respond.ErrorResponse]()}}},
	}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/roles",
		OperationID: "listRoles", Summary: "List roles", Tags: tags,
		Middleware: read}, h.listRoles)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/capabilities",
		OperationID: "listCapabilities", Summary: "List capabilities", Tags: tags,
		Middleware: read}, h.listCapabilities)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/grants",
		OperationID: "listGrants", Summary: "List grants, oldest first", Tags: tags,
		Middleware: read}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/grants",
		OperationID: "createGrant", Summary: "Grant a role at a scope", Tags: tags,
		Status: http.StatusCreated, Errors: []int{http.StatusForbidden, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.SessionOnly, checks.At(middleware.CapUsersWrite), twoFactor}}, h.create)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/grants/{id}",
		OperationID: "deleteGrant", Summary: "Remove a grant", Tags: tags,
		Status: http.StatusNoContent, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		Middleware: write}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/grants/mine",
		OperationID: "listMyGrants", Summary: "The caller's own grants", Tags: tags,
		Middleware: []endpoint.Middleware{checks.SessionOnly}}, h.mine)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/grants/{id}/activate",
		OperationID: "activateGrant", Summary: "Switch an eligible grant on", Tags: tags,
		Status: http.StatusCreated, Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.SessionOnly, checks.AuthThrottle}}, h.activate)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/grants/{id}/deactivate",
		OperationID: "deactivateGrant", Summary: "Switch a grant off", Tags: tags,
		Errors:     []int{http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.SessionOnly}}, h.deactivate)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/grants/{id}/make-eligible",
		OperationID: "makeGrantEligible", Summary: "Make an active grant eligible", Tags: tags,
		Errors:     []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.SessionOnly, checks.AuthThrottle}}, h.makeEligible)
}
