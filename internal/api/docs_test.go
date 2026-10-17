package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// /api/docs/openapi.yaml serves the root openapi.yaml, byte for byte (#360).
// The served spec used to be a hand-kept copy in this package, and it fell
// about 1,200 lines behind; if a copy ever comes back, this fails as soon as
// the two differ.
func TestDocs_ServesTheRootSpec(t *testing.T) {
	want, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatalf("read the root spec: %v", err)
	}

	router, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/docs/openapi.yaml", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/docs/openapi.yaml = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("the served spec is not openapi.yaml: served %d bytes, the file has %d", rec.Body.Len(), len(want))
	}
}
