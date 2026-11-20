package handlers

import (
	"context"
	"net/http"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

// PublicStatus is the entire public availability view.
type PublicStatus struct {
	Status string `json:"status" enums:"operational,degraded"`
}

// RegisterPublicStatus mounts the public availability view independently of
// the operator health probe, which can return certificate details.
func (h *HealthHandler) RegisterPublicStatus(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/status",
		OperationID: "getPublicStatus", Summary: "Public service availability", Tags: []string{"Health"},
		Description: "Reports whether the API can reach its database. Every answer has Cache-Control: no-store.",
		Security:    oapifly.Public, Headers: noStoreHeader, Middleware: []endpoint.Middleware{noStore},
		SuccessStatuses: []int{http.StatusServiceUnavailable}}, h.publicStatus)
}

func (h *HealthHandler) publicStatus(r *http.Request, _ struct{}) (endpoint.Status[PublicStatus], error) {
	// Bound public probes even if the peer keeps its connection open while
	// the database pool or network is stalled.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if h.db != nil {
		pool, err := h.db.DB()
		if err == nil && pool.PingContext(ctx) == nil {
			return endpoint.Status[PublicStatus]{Code: http.StatusOK, Value: PublicStatus{Status: "operational"}}, nil
		}
	}
	return endpoint.Status[PublicStatus]{Code: http.StatusServiceUnavailable, Value: PublicStatus{Status: "degraded"}}, nil
}
