package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

func TestAuthenticatedReadExamplesWithVertrag(t *testing.T) {
	useRealGrants(t)
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to run the real authenticated-read boundary check")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	production, _ := newTestRouter(t)
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	permitted := authReadExampleRouter(t, jwt, "listCapabilities", capabilityExampleDB(t), true)
	token := mailboxToken(t, jwt)
	failingStatus := authReadExampleRouter(t, jwt, "getTwoFactorStatus", newFailingGormDB(t), true)
	failingCatalogue := authReadExampleRouter(t, jwt, "listCapabilities", newFailingGormDB(t), true)
	failingReads := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == authReadPaths["getTwoFactorStatus"] {
			failingStatus.ServeHTTP(w, r)
		} else {
			failingCatalogue.ServeHTTP(w, r)
		}
	})
	both := []string{authReadPaths["getTwoFactorStatus"], authReadPaths["listCapabilities"]}
	for _, tc := range []struct {
		name, status, token string
		paths               []string
		handler             http.Handler
	}{
		{"missing credentials", "401", "", both, production},
		{"permitted catalogue", "200", token, []string{authReadPaths["listCapabilities"]}, permitted},
		{"missing permission", "403", token, []string{authReadPaths["listCapabilities"]}, authReadExampleRouter(t, jwt, "listCapabilities", newFailingGormDB(t), false)},
		{"oversized transport body", "413", token, both, failingReads},
		{"failed stores", "500", token, both, failingReads},
		{"failed session lookup", "503", token, both, production},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkAuthReadWithVertrag(t, binary, tc.handler, tc.token, tc.status, tc.paths)
		})
	}
}

func checkAuthReadWithVertrag(t *testing.T, binary string, handler http.Handler, token, status string, paths []string) {
	t.Helper()
	doc := contractDocument(t, describedRouters(t).generator("authenticated example boundary"))
	all := doc["paths"].(map[string]interface{})
	selected := map[string]interface{}{}
	for _, path := range paths {
		selected[path] = map[string]interface{}{"get": all[path].(map[string]interface{})["get"]}
	}
	doc["paths"] = selected
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	spec, reportPath := filepath.Join(dir, "openapi.json"), filepath.Join(dir, "report.json")
	if err := os.WriteFile(spec, data, 0600); err != nil {
		t.Fatal(err)
	}
	if status == "413" {
		// Transport preconditions are outside the typed GET request. A
		// normal Vertrag hook supplies nine real bytes to the eight-byte
		// fixture cap; no schema or expected response is rewritten.
		hook := "import vertrag_hooks as hooks\n"
		for _, path := range paths {
			operation := selected[path].(map[string]interface{})["get"].(map[string]interface{})["operationId"].(string)
			hook += "@hooks.before('" + operation + "')\n"
		}
		hook += "def oversized_body(transaction):\n    transaction['request']['body'] = 'oversized'\n"
		if err := os.WriteFile(filepath.Join(dir, "oversize.py"), []byte(hook), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./oversize.py\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	args := []string{"run", "--endpoint", server.URL, "--only-matching", " > " + status,
		"--reporter", "json", "--output", reportPath}
	if token != "" {
		args = append(args, "--header", "Authorization: Bearer "+token)
	}
	args = append(args, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir // Production phase hooks and credentials cannot change these cases.
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("Vertrag rejected the actual registered response: %v\n%s", runErr, output)
	}
	data, err = os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Summary struct{ Total, Passing, Failing, Errors, Skipped int }
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode Vertrag report (%d bytes): %v\nCLI output: %s\nReport: %s", len(data), err, output, data)
	}
	if report.Summary.Total != len(paths) || report.Summary.Passing != len(paths) || report.Summary.Failing != 0 || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
		t.Fatalf("not every selected transaction ran and passed: %s", data)
	}
}

func TestAuthenticatedReadBatchWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to run the real authenticated-read batch")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	unavailable, failed, snapshot, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	pending, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, path := range authReadBatchPaths {
		paths = append(paths, path)
	}
	for _, tc := range []struct {
		name, status, token string
		handler             http.Handler
	}{
		{"permitted snapshot", "200", token, snapshot},
		{"missing credentials", "401", "", unavailable},
		{"unfinished setup", "403", pending.AccessToken, failed},
		{"oversized transport body", "413", token, failed},
		{"failed stores", "500", token, failed},
		{"failed session lookup", "503", token, unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) { checkAuthReadWithVertrag(t, binary, tc.handler, tc.token, tc.status, paths) })
	}
}
