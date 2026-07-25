package filters

// Item 5 (issue #201): the JavaScript filter treated EVERY sidecar failure
// (connection error, timeout, non-200, unparseable body, invalid result) as
// ActionContinue — a wedged or compromised sidecar silently disabled all JS
// filtering (fail-open). The fix adds a configurable failure action defaulting
// to `defer` (fail-closed) and validates the sidecar's returned action against
// the known set before trusting it.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

func jsConfig(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return b
}

// sidecarStub returns an httptest server that always responds with the given
// status code and raw body.
func sidecarStub(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// TestJSFilter_SidecarFailureDefersByDefault: a non-2xx sidecar response must
// fail closed to ActionDefer, not silently continue.
func TestJSFilter_SidecarFailureDefersByDefault(t *testing.T) {
	srv := sidecarStub(http.StatusInternalServerError, `{"error":"boom"}`)
	defer srv.Close()

	f, err := NewJavaScript(jsConfig(t, map[string]any{"script": "function filter(e){return e}", "url": srv.URL}))
	if err != nil {
		t.Fatalf("NewJavaScript: %v", err)
	}
	res, err := f.Execute(context.Background(), &pipeline.EmailJSON{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionDefer {
		t.Errorf("sidecar 500 → Action = %q, want defer (fail-closed default)", res.Action)
	}
}

// TestJSFilter_FailureActionConfigurable: an operator may opt back into
// fail-open by setting failure_action=continue.
func TestJSFilter_FailureActionConfigurable(t *testing.T) {
	srv := sidecarStub(http.StatusInternalServerError, `{"error":"boom"}`)
	defer srv.Close()

	f, err := NewJavaScript(jsConfig(t, map[string]any{
		"script":         "function filter(e){return e}",
		"url":            srv.URL,
		"failure_action": "continue",
	}))
	if err != nil {
		t.Fatalf("NewJavaScript: %v", err)
	}
	res, err := f.Execute(context.Background(), &pipeline.EmailJSON{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionContinue {
		t.Errorf("failure_action=continue → Action = %q, want continue", res.Action)
	}
}

// TestJSFilter_InvalidReturnedActionTreatedAsFailure: a 200 response whose
// result carries an action outside the known set must not be cast blindly — it
// falls back to the failure action (defer by default).
func TestJSFilter_InvalidReturnedActionTreatedAsFailure(t *testing.T) {
	srv := sidecarStub(http.StatusOK, `{"result":{"type":"action","action":"pwned","log":{"detail":"x"}}}`)
	defer srv.Close()

	f, err := NewJavaScript(jsConfig(t, map[string]any{"script": "function filter(e){return e}", "url": srv.URL}))
	if err != nil {
		t.Fatalf("NewJavaScript: %v", err)
	}
	res, err := f.Execute(context.Background(), &pipeline.EmailJSON{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action == "pwned" {
		t.Fatal("invalid action from sidecar was cast blindly")
	}
	if res.Action != pipeline.ActionDefer {
		t.Errorf("invalid returned action → Action = %q, want defer", res.Action)
	}
}

// TestJSFilter_ValidActionPassesThrough: a well-formed sidecar result with a
// known action is honoured.
func TestJSFilter_ValidActionPassesThrough(t *testing.T) {
	srv := sidecarStub(http.StatusOK, `{"result":{"type":"action","action":"reject","log":{"detail":"spam"}}}`)
	defer srv.Close()

	f, err := NewJavaScript(jsConfig(t, map[string]any{"script": "function filter(e){return e}", "url": srv.URL}))
	if err != nil {
		t.Fatalf("NewJavaScript: %v", err)
	}
	res, err := f.Execute(context.Background(), &pipeline.EmailJSON{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionReject {
		t.Errorf("valid action=reject → Action = %q, want reject", res.Action)
	}
}

// TestJSFilter_UnknownFailureActionRejectedAtConstruction: a bogus
// failure_action is a configuration error, caught at construction.
func TestJSFilter_UnknownFailureActionRejectedAtConstruction(t *testing.T) {
	_, err := NewJavaScript(jsConfig(t, map[string]any{
		"script":         "function filter(e){return e}",
		"failure_action": "banana",
	}))
	if err == nil {
		t.Fatal("expected NewJavaScript to reject an unknown failure_action")
	}
}
