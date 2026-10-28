package middleware

import (
	"context"
	"net/http"

	"github.com/restmail/restmail/internal/db/models"
)

type auditTargetKey struct{}

func withAuditTarget(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), auditTargetKey{}, models.AuditTarget{}))
}

func requestAuditTarget(r *http.Request) models.AuditTarget {
	target, _ := r.Context().Value(auditTargetKey{}).(models.AuditTarget)
	return target
}

// SetAuditTarget adds a server-resolved target to the edge audit entry. This
// avoids deriving identity from an untrusted body or a truncated response.
// Actor and credential always come from the original authenticated claims.
func SetAuditTarget(r *http.Request, key, value string) {
	if target := requestAuditTarget(r); target != nil {
		target[key] = value
	}
}
