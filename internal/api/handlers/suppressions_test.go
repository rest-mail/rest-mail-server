package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestSuppressions_RejectDomainIDsBelowOne: the spec's domain ID starts at 1,
// so 0 is refused as a negative or non-numeric ID is, before the database is
// asked (the handler has none here).
func TestSuppressions_RejectDomainIDsBelowOne(t *testing.T) {
	h := NewSuppressionHandler(nil)
	for _, id := range []string{"0", "-1", "vertrag-not-a-number", "1.5"} {
		for name, serve := range map[string]http.HandlerFunc{"List": h.List, "Delete": h.Delete} {
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", id)
			rctx.URLParams.Add("sid", "1")
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			rr := httptest.NewRecorder()
			serve(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s with domain ID %q: status %d, want 400", name, id, rr.Code)
			}
		}
	}
}
