package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A draft's to and cc entries must be addresses, as DraftRequest's
// format: email says (#388). The refusal comes before any lookup, so the
// handler needs no database to answer it.
func TestSaveDraft_RefusesARecipientThatIsNotAnAddress(t *testing.T) {
	h := NewMessageHandler(nil, nil, nil, "", nil)
	for _, tc := range []struct{ name, body, field string }{
		{"to", `{"to":["not-an-email"]}`, "to"},
		{"cc", `{"cc":["not-an-email"]}`, "cc"},
		{"to after a good one", `{"to":["a@restmail.test","b@restmail.test\r\nBcc: c@evil.test"]}`, "to"},
		{"empty cc entry", `{"cc":[""]}`, "cc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			composeRoutes(h).ServeHTTP(rr, authzRequest(http.MethodPost, "/api/v1/messages/draft", 1, nil, tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", rr.Code, rr.Body.String())
			}
			if want := "invalid recipient address in " + tc.field; !strings.Contains(rr.Body.String(), want) {
				t.Errorf("body %s does not say %q", rr.Body.String(), want)
			}
		})
	}
}
