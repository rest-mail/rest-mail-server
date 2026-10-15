package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/version"
)

// A server says it is rest-mail, and which version, to anyone who asks — and
// nothing else: before 0.7 it also said whether it was claimed, and how to.
func TestServer_SaysWhatItIsAndNothingElse(t *testing.T) {
	rr := httptest.NewRecorder()
	DescribeServer(rr, httptest.NewRequest(http.MethodGet, "/api/v1/server", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/server = %d %s", rr.Code, rr.Body)
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", rr.Body, err)
	}
	want := map[string]any{"product": "rest-mail", "version": version.Version}
	if len(body.Data) != len(want) || body.Data["product"] != want["product"] || body.Data["version"] != want["version"] {
		t.Errorf("got %v, want %v", body.Data, want)
	}
}
