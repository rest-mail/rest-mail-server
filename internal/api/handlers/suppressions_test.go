package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSuppressions_RejectDomainIDsBelowOne: the spec's domain ID starts at 1,
// so 0 is refused as a negative or non-numeric ID is, before the database is
// asked (the handler has none here).
func TestSuppressions_RejectDomainIDsBelowOne(t *testing.T) {
	h := NewSuppressionHandler(nil)
	for _, id := range []string{"0", "-1", "vertrag-not-a-number", "1.5"} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			path := "/api/v1/admin/domains/" + id + "/suppressions"
			if method == http.MethodDelete {
				path += "/1"
			}
			req := httptest.NewRequest(method, path, nil)
			rr := httptest.NewRecorder()
			policyRoutes(h).ServeHTTP(rr, req)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s with domain ID %q: status %d, want 422", method, id, rr.Code)
			}
		}
	}
}
