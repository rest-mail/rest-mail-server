//go:build faultinject && fault_health_status_created

package faultinject

import (
	"net/http"
	"net/http/httptest"
)

func init() {
	register(Fault{
		Name:   "health_status_created",
		Method: "GET",
		Path:   "/api/health",
		Break: func(rec *httptest.ResponseRecorder) {
			if rec.Code == http.StatusOK {
				rec.Code = http.StatusCreated
			}
		},
		Example: `{"data":{"status":"ok"}}`,
	})
}
