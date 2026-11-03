//go:build faultinject && fault_discover_missing_status_ok

package faultinject

import (
	"net/http"
	"net/http/httptest"
)

// RESTMAIL discovery answers an unhosted domain with 200, keeping the refusal's
// body, so only the status tells a client the domain is not hosted.
func init() {
	register(Fault{
		Name:   "discover_missing_status_ok",
		Method: "GET",
		Path:   "/.well-known/restmail",
		Break: func(rec *httptest.ResponseRecorder) {
			if rec.Code == http.StatusNotFound {
				rec.Code = http.StatusOK
			}
		},
		Example:       `{"error":{"code":"not_found","message":"Domain not hosted"}}`,
		ExampleStatus: http.StatusNotFound,
	})
}
