package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
)

func TestOwnMailboxLegacySendRequests(t *testing.T) {
	// Existing clients omit on_behalf, including clients sending a draft with
	// no body. They must not need either new delegated-sending capability.
	for _, tc := range []struct {
		name, path, body string
		access           string
	}{
		{"compose", "/api/v1/messages/send", `{"from":"owner@example.test","to":["far@remote.example"],"subject":"hello"}`, ""},
		{"forward", "/api/v1/messages/5/forward", `{"from":"owner@example.test","to":["far@remote.example"]}`, "mail:read"},
		{"draft_without_body", "/api/v1/messages/draft/5/send", "", "mail:write"},
		{"draft_empty_object", "/api/v1/messages/draft/5/send", `{}`, "mail:write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &draftSendDB{}
			h := draftSendHandler(t, d)
			permissions := []auth.ScopedPermission{{Capability: "mail:send", Scope: "mailbox:owner@example.test"}}
			if tc.access != "" {
				permissions = append(permissions, auth.ScopedPermission{Capability: tc.access, Scope: "mailbox:owner@example.test"})
			}
			claims := &auth.Claims{AccountID: 7, Address: "owner@example.test", CredentialKind: auth.CredentialKindAPIKey, CredentialID: 9, APIKeyPermissions: permissions}
			r := withClaims(httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)), claims)
			w := httptest.NewRecorder()
			composeRoutes(h).ServeHTTP(w, r)
			if w.Code != http.StatusCreated || d.stored.sent != 1 || d.stored.queued != 1 {
				t.Fatalf("legacy request: %d %s, sent=%d queued=%d", w.Code, w.Body, d.stored.sent, d.stored.queued)
			}
			if strings.HasPrefix(tc.name, "draft_") && d.stored.draft {
				t.Fatal("sending the draft left the draft behind")
			}
		})
	}
}
