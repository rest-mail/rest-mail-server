package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The older nightly's two searchMessages findings sent after=a and before=a as valid values
// (#1163). The description says both are date-times, and the route refuses anything else at
// binding with 422 before it reads mail, so route and description agree: it was Vertrag's
// generator that drew a non-date-time for a date-time query parameter.
func TestSearchRefusesADateThatIsNotADateTime(t *testing.T) {
	s := messageCollectionReadScenarios(t)["found"]
	for _, name := range []string{"after", "before"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/1/search?q=Subject&"+name+"=a&has_attachment=", nil)
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			want := "{\"error\":{\"code\":\"validation_failed\",\"message\":\"Query parameters failed validation\",\"details\":{\"fields\":{\"" + name + "\":\"must be a date-time\"}}}}\n"
			if rr.Code != http.StatusUnprocessableEntity || rr.Body.String() != want {
				t.Fatalf("got %d %s, want 422 %s", rr.Code, rr.Body.String(), want)
			}
		})
	}
}
