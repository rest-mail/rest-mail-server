package filters

// Item 5 (issue #201): the JavaScript filter treated EVERY sidecar failure
// (connection error, timeout, non-200, unparseable body, invalid result) as
// ActionContinue — a wedged or compromised sidecar silently disabled all JS
// filtering (fail-open). The fix adds a configurable failure action defaulting
// to `defer` (fail-closed) and validates the sidecar's returned action against
// the known set before trusting it.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

const testSidecarToken = "test-token-0123456789abcdef"

// sidecarStub returns an httptest server that, like the sidecar, refuses a
// request without the token, and otherwise always responds with the given
// status code and raw body.
func sidecarStub(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSidecarToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// TestJSFilter_SidecarFailureDefersByDefault: a non-2xx sidecar response must
// fail closed to ActionDefer, not silently continue.
func TestJSFilter_SidecarFailureDefersByDefault(t *testing.T) {
	srv := sidecarStub(http.StatusInternalServerError, `{"error":"boom"}`)
	defer srv.Close()

	f, err := NewJavaScriptWithSidecar(JSSidecar{URL: srv.URL, Token: testSidecarToken})(jsConfig(t, map[string]any{"script": "function filter(e){return e}"}))
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

	f, err := NewJavaScriptWithSidecar(JSSidecar{URL: srv.URL, Token: testSidecarToken})(jsConfig(t, map[string]any{
		"script":         "function filter(e){return e}",
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

	f, err := NewJavaScriptWithSidecar(JSSidecar{URL: srv.URL, Token: testSidecarToken})(jsConfig(t, map[string]any{"script": "function filter(e){return e}"}))
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

	f, err := NewJavaScriptWithSidecar(JSSidecar{URL: srv.URL, Token: testSidecarToken})(jsConfig(t, map[string]any{"script": "function filter(e){return e}"}))
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

// TestJSFilter_WrongTokenFailsClosed (#160): a sidecar that refuses the API's
// token yields no verdict, so the failure action applies.
func TestJSFilter_WrongTokenFailsClosed(t *testing.T) {
	srv := sidecarStub(http.StatusOK, `{"result":{"type":"action","action":"continue","log":{"detail":"ok"}}}`)
	defer srv.Close()

	f, err := NewJavaScriptWithSidecar(JSSidecar{URL: srv.URL, Token: "not-the-token"})(jsConfig(t, map[string]any{"script": "function filter(e){return e}"}))
	if err != nil {
		t.Fatalf("NewJavaScript: %v", err)
	}
	res, err := f.Execute(context.Background(), &pipeline.EmailJSON{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionDefer {
		t.Errorf("sidecar 401 → Action = %q, want defer", res.Action)
	}
}

// TestJSFilter_ConfigCannotRedirectTheSidecar (#160): the sidecar is the
// operator's (JS_FILTER_URL). A "url" in a pipeline's filter config is not
// read, so it cannot send the script, or the token, to another host.
func TestJSFilter_ConfigCannotRedirectTheSidecar(t *testing.T) {
	sidecar := sidecarStub(http.StatusOK, `{"result":{"type":"action","action":"reject","log":{"detail":"spam"}}}`)
	defer sidecar.Close()
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
	}))
	defer other.Close()

	f, err := NewJavaScriptWithSidecar(JSSidecar{URL: sidecar.URL, Token: testSidecarToken})(jsConfig(t, map[string]any{
		"script": "function filter(e){return e}",
		"url":    other.URL,
	}))
	if err != nil {
		t.Fatalf("NewJavaScript: %v", err)
	}
	res, err := f.Execute(context.Background(), &pipeline.EmailJSON{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Action != pipeline.ActionReject {
		t.Errorf("Action = %q, want the sidecar's reject", res.Action)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the config's url received %d requests, want 0", n)
	}
}

// TestJSFilter_RealSidecar runs the filter against the real Node sidecar
// (projects/js-filter-sidecar), end to end: the token the API sends is the one
// the sidecar was started with, a script's verdict comes back, and a script
// trying the old vm escape gets no verdict. It needs node and the sidecar's
// node_modules (npm ci), and is skipped without them; CI's JS filter job has
// both.
func TestJSFilter_RealSidecar(t *testing.T) {
	sidecar := startRealSidecar(t)
	email := &pipeline.EmailJSON{Headers: pipeline.Headers{Subject: "Win a prize"}}

	run := func(t *testing.T, token, script string) *pipeline.FilterResult {
		t.Helper()
		f, err := NewJavaScriptWithSidecar(JSSidecar{URL: sidecar, Token: token})(jsConfig(t, map[string]any{
			"script":     script,
			"timeout_ms": 1000,
		}))
		if err != nil {
			t.Fatalf("NewJavaScript: %v", err)
		}
		res, err := f.Execute(context.Background(), email)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		return res
	}

	verdict := `function filter(email) {
		if (email.headers.subject.includes("prize")) {
			return { type: "action", action: "reject", log: { detail: "prize spam" } };
		}
		return { type: "action", action: "continue" };
	}`

	t.Run("verdict", func(t *testing.T) {
		res := run(t, testSidecarToken, verdict)
		if res.Action != pipeline.ActionReject || res.Log.Detail != "prize spam" {
			t.Errorf("got %q (%s), want reject (prize spam)", res.Action, res.Log.Detail)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		res := run(t, "not-the-token-0123456789", verdict)
		if res.Action != pipeline.ActionDefer {
			t.Errorf("got %q, want defer", res.Action)
		}
		if !strings.Contains(res.Log.Detail, "unauthorized") {
			t.Errorf("detail %q does not say the sidecar refused the token", res.Log.Detail)
		}
	})

	t.Run("escape", func(t *testing.T) {
		res := run(t, testSidecarToken, `function filter(email) {
			const proc = Object.constructor("return process")();
			return { type: "action", action: proc.getBuiltinModule("os").hostname() };
		}`)
		if res.Action != pipeline.ActionDefer {
			t.Errorf("got %q (%s), want defer: the escape must not run", res.Action, res.Log.Detail)
		}
	})
}

// startRealSidecar starts the Node sidecar on a free port with
// testSidecarToken, and returns its URL.
func startRealSidecar(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "projects", "js-filter-sidecar"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		t.Skipf("the sidecar's dependencies are not installed (npm ci in %s)", dir)
	}

	cmd := exec.Command(node, "server.js")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PORT=0", "JS_FILTER_TOKEN="+testSidecarToken)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	port := make(chan string, 1)
	go func() {
		listening := regexp.MustCompile(`listening on port (\d+)`)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if m := listening.FindStringSubmatch(scanner.Text()); m != nil {
				port <- m[1]
			}
		}
	}()
	select {
	case p := <-port:
		return "http://127.0.0.1:" + p
	case <-time.After(15 * time.Second):
		t.Fatal("the sidecar did not start")
		return ""
	}
}
