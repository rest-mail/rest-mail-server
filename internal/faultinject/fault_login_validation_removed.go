//go:build faultinject && fault_login_validation_removed

package faultinject

import (
	"net/http"
	"net/http/httptest"
)

func init() {
	register(Fault{
		Name:   "login_validation_removed",
		Method: "POST",
		Path:   "/api/v1/auth/login",
		Break: func(rec *httptest.ResponseRecorder) {
			if rec.Code == http.StatusUnprocessableEntity {
				rec.Code = http.StatusOK
			}
		},
		Example:       `{"error":{"code":"validation_error","message":"email is required"}}`,
		ExampleStatus: http.StatusUnprocessableEntity,
	})
}
