//go:build faultinject && fault_change_password_refusal_echoes_password

package faultinject

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

// A refused password change's message repeats the current password it was sent,
// as an error built from the request would. The message is a free string, so the
// answer still fits its schema: only something looking for the secret can notice.
// This route is the witness, not login, because the only password login's
// examples record is the fixture admin's, which Vertrag redacts from the recording
// (tests/contract/README.md).
func init() {
	register(Fault{
		Name:   "change_password_refusal_echoes_password",
		Method: "POST",
		Path:   "/api/v1/auth/password",
		BreakSeeing: func(sent []byte, rec *httptest.ResponseRecorder) {
			var request struct {
				CurrentPassword string `json:"current_password"`
			}
			if rec.Code != http.StatusForbidden || json.Unmarshal(sent, &request) != nil || request.CurrentPassword == "" {
				return
			}
			editJSON(func(body map[string]any) {
				if problem, ok := body["error"].(map[string]any); ok && problem["code"] == "invalid_password" {
					problem["message"] = "Current password is incorrect: " + request.CurrentPassword
				}
			})(rec)
		},
		Example:        `{"error":{"code":"invalid_password","message":"Current password is incorrect"}}`,
		ExampleStatus:  http.StatusForbidden,
		ExampleRequest: `{"current_password":"not-the-password","new_password":"a new password of my own"}`,
	})
}
