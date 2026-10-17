package handlers

// #160: the admin routes that test and validate a custom filter call the JS
// filter sidecar with the deployment's token, and a sidecar refusing it is the
// sidecar's problem, not the admin's: a 503, never a 401 that reads as the
// admin's own session having ended.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline/filters"
)

const testJSSidecarToken = "test-token-0123456789abcdef"

func jsSidecarStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/validate" {
			t.Errorf("sidecar path = %q, want /validate", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+testJSSidecarToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_, _ = w.Write([]byte(`{"valid":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func validateCustomFilter(h *PipelineHandler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/custom-filters/validate",
		strings.NewReader(`{"script":"function filter(e){return e}"}`))
	rec := httptest.NewRecorder()
	h.ValidateCustomFilter(rec, req)
	return rec
}

func TestValidateCustomFilter_SendsTheSidecarToken(t *testing.T) {
	srv := jsSidecarStub(t)
	rec := validateCustomFilter(NewPipelineHandler(nil, nil, filters.JSSidecar{URL: srv.URL, Token: testJSSidecarToken}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("got %d %s, want 200 with the sidecar's verdict", rec.Code, rec.Body)
	}
}

func TestValidateCustomFilter_RefusedTokenIs503(t *testing.T) {
	srv := jsSidecarStub(t)
	rec := validateCustomFilter(NewPipelineHandler(nil, nil, filters.JSSidecar{URL: srv.URL, Token: "not-the-token"}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s, want 503", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "JS_FILTER_TOKEN") {
		t.Errorf("body %s does not name JS_FILTER_TOKEN", rec.Body)
	}
}
