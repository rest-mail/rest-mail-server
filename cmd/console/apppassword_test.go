package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// appPasswordRequest is one request the stand-in API saw.
type appPasswordRequest struct {
	method, path, auth string
	body               map[string]any
}

// appPasswordAPI stands in for the credential routes: it records each request
// and answers with status and body.
func appPasswordAPI(t *testing.T, status int, body string) (*httptest.Server, *[]appPasswordRequest) {
	t.Helper()
	var seen []appPasswordRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := appPasswordRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &req.body); err != nil {
				t.Errorf("body is not JSON: %v", err)
			}
		}
		seen = append(seen, req)
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// sessionToken is an unsigned token shaped like a session's, for account 42:
// the CLI reads its account_id and the stand-in never checks it.
func sessionToken() string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"HS256"}`)) + "." + enc([]byte(`{"account_id":42,"mfa":true}`)) + ".sig"
}

// noTerminal makes stdin a pipe for the test, whatever runs it.
func noTerminal(t *testing.T) {
	t.Helper()
	was := appPasswordTerminal
	appPasswordTerminal = func(io.Reader) bool { return false }
	t.Cleanup(func() { appPasswordTerminal = was })
}

func runAppPasswordTest(t *testing.T, args []string, token, stdin string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runAppPassword(context.Background(), args, token, strings.NewReader(stdin), http.DefaultClient, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

const listedAppPasswords = `{"data":[{"id":7,"name":"laptop","kind":"app_password","public_id":"abc","created_at":"2026-11-01T10:00:00Z","last_used_at":null,"expires_at":null}]}`

func TestAppPasswordList(t *testing.T) {
	noTerminal(t)
	srv, seen := appPasswordAPI(t, http.StatusOK, listedAppPasswords)
	token := sessionToken()

	code, out, errOut := runAppPasswordTest(t, []string{"list", "--server", srv.URL}, token, "")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	got := (*seen)[0]
	if got.method != http.MethodGet || got.path != "/api/v1/accounts/42/credentials" || got.auth != "Bearer "+token {
		t.Fatalf("request %s %s (authorized as the session: %v)", got.method, got.path, got.auth == "Bearer "+token)
	}
	for _, want := range []string{"ID", "NAME", "7", "laptop", "2026-11-01 10:00Z", "never"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}

	code, out, _ = runAppPasswordTest(t, []string{"list", "--server", srv.URL, "--account", "9", "--json"}, token, "")
	if code != exitOK || (*seen)[1].path != "/api/v1/accounts/9/credentials" {
		t.Fatalf("--account 9: exit %d, path %s", code, (*seen)[1].path)
	}
	var listed struct {
		AppPasswords []appPasswordCredential `json:"app_passwords"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed.AppPasswords) != 1 || listed.AppPasswords[0].ID != 7 || listed.AppPasswords[0].Name != "laptop" {
		t.Fatalf("--json list = %q (%v)", out, err)
	}
}

func TestAppPasswordListNeedsASessionOrAccount(t *testing.T) {
	noTerminal(t)
	srv, seen := appPasswordAPI(t, http.StatusOK, listedAppPasswords)
	code, _, errOut := runAppPasswordTest(t, []string{"list", "--server", srv.URL}, "rm_abc_notajwt", "")
	if code != exitUsage || !strings.Contains(errOut, "--account") || len(*seen) != 0 {
		t.Fatalf("a token without account_id: exit %d, stderr %q, %d requests", code, errOut, len(*seen))
	}
	code, _, errOut = runAppPasswordTest(t, []string{"list", "--server", srv.URL}, "", "")
	if code != exitUsage || !strings.Contains(errOut, "RESTMAIL_API_KEY") {
		t.Fatalf("no token: exit %d, stderr %q", code, errOut)
	}
}

const createdAppPasswordBody = `{"data":{"id":8,"name":"phone","kind":"app_password","public_id":"def","created_at":"2026-11-03T10:00:00Z","last_used_at":null,"expires_at":null,"password":"rm_def_s3cretvalue"}}`

func TestAppPasswordCreateWithPasswordStdin(t *testing.T) {
	noTerminal(t)
	srv, seen := appPasswordAPI(t, http.StatusCreated, createdAppPasswordBody)
	token := sessionToken()
	const current = "current-password-value"

	code, out, errOut := runAppPasswordTest(t, []string{"create", "--server", srv.URL, "--name", " phone ", "--expires-at", "2027-01-31T00:00:00Z", "--password-stdin"}, token, current+"\n")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	got := (*seen)[0]
	if got.method != http.MethodPost || got.path != "/api/v1/accounts/42/credentials" || got.auth != "Bearer "+token {
		t.Fatalf("request %s %s", got.method, got.path)
	}
	if got.body["name"] != "phone" || got.body["expires_at"] != "2027-01-31T00:00:00Z" {
		t.Errorf("body name %v, expires_at %v", got.body["name"], got.body["expires_at"])
	}
	if got.body["password"] != current {
		t.Error("the body does not carry the current password read from stdin, without its newline")
	}
	if strings.Contains(out, current) || strings.Contains(errOut, current) {
		t.Error("the current password was echoed")
	}
	if !strings.Contains(out, "rm_def_s3cretvalue") || !strings.Contains(out, "won't see it again") || strings.Count(out, "rm_def_s3cretvalue") != 1 {
		t.Errorf("the new app password must be printed once, with a warning:\n%s", out)
	}

	code, out, _ = runAppPasswordTest(t, []string{"create", "--server", srv.URL, "--name", "phone", "--password-stdin", "--json"}, token, current)
	var created appPasswordCreated
	if code != exitOK || json.Unmarshal([]byte(out), &created) != nil || created.ID != 8 || created.Password != "rm_def_s3cretvalue" {
		t.Fatalf("--json create: exit %d, %q", code, out)
	}
	if _, ok := (*seen)[1].body["expires_at"]; ok {
		t.Error("expires_at sent without --expires-at")
	}
}

func TestAppPasswordCreateAsksOnTheTerminal(t *testing.T) {
	srv, seen := appPasswordAPI(t, http.StatusCreated, createdAppPasswordBody)
	wasTerminal, wasRead := appPasswordTerminal, readHiddenPassword
	t.Cleanup(func() { appPasswordTerminal, readHiddenPassword = wasTerminal, wasRead })
	appPasswordTerminal = func(io.Reader) bool { return true }
	asked := 0
	readHiddenPassword = func(io.Reader) ([]byte, error) { asked++; return []byte("typed-password"), nil }

	code, out, errOut := runAppPasswordTest(t, []string{"create", "--server", srv.URL, "--name", "phone"}, sessionToken(), "")
	if code != exitOK || asked != 1 {
		t.Fatalf("exit %d, asked %d times, stderr %q", code, asked, errOut)
	}
	if (*seen)[0].body["password"] != "typed-password" {
		t.Error("the typed password was not sent")
	}
	if !strings.Contains(errOut, "Current password:") || strings.Contains(errOut+out, "typed-password") {
		t.Errorf("prompt on stderr, nothing echoed: stderr %q", errOut)
	}
}

func TestAppPasswordCreateRefusesWithoutAPassword(t *testing.T) {
	noTerminal(t)
	srv, seen := appPasswordAPI(t, http.StatusCreated, createdAppPasswordBody)
	for name, tc := range map[string]struct {
		args  []string
		stdin string
		want  string
	}{
		"no terminal, no --password-stdin": {[]string{"create", "--name", "phone"}, "piped", "--password-stdin"},
		"empty stdin":                      {[]string{"create", "--name", "phone", "--password-stdin"}, "\n", "current password"},
		"no name":                          {[]string{"create", "--password-stdin"}, "pw", "--name is required"},
		"password as a flag":               {[]string{"create", "--name", "phone", "--password", "pw"}, "", "not defined"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append(tc.args, "--server", srv.URL)
			code, _, errOut := runAppPasswordTest(t, args, sessionToken(), tc.stdin)
			if code != exitUsage || !strings.Contains(errOut, tc.want) {
				t.Fatalf("exit %d, stderr %q, want %q", code, errOut, tc.want)
			}
		})
	}
	if len(*seen) != 0 {
		t.Fatalf("%d requests sent", len(*seen))
	}
}

func TestAppPasswordCreateTwoFactorRefusal(t *testing.T) {
	noTerminal(t)
	for _, code := range []string{"recent_two_factor_required", "two_factor_required"} {
		t.Run(code, func(t *testing.T) {
			srv, _ := appPasswordAPI(t, http.StatusForbidden, `{"error":{"code":"`+code+`","message":"Sign in again with two-factor authentication"}}`)
			status, out, errOut := runAppPasswordTest(t, []string{"create", "--server", srv.URL, "--name", "phone", "--password-stdin"}, sessionToken(), "pw")
			if status != exitFailed || out != "" {
				t.Fatalf("exit %d, stdout %q", status, out)
			}
			for _, want := range []string{"Sign in again with two-factor authentication", "authenticator code", "RESTMAIL_API_KEY", "within 15 minutes"} {
				if !strings.Contains(errOut, want) {
					t.Errorf("stderr lacks %q: %q", want, errOut)
				}
			}
			_, _, jsonErr := runAppPasswordTest(t, []string{"create", "--server", srv.URL, "--name", "phone", "--password-stdin", "--json"}, sessionToken(), "pw")
			var refusal struct {
				Error struct{ Code, Message string } `json:"error"`
			}
			if json.Unmarshal([]byte(jsonErr), &refusal) != nil || refusal.Error.Code != code {
				t.Fatalf("--json refusal = %q", jsonErr)
			}
		})
	}
}

func TestAppPasswordRevoke(t *testing.T) {
	noTerminal(t)
	srv, seen := appPasswordAPI(t, http.StatusNoContent, "")
	token := sessionToken()

	code, out, errOut := runAppPasswordTest(t, []string{"revoke", "--server", srv.URL, "7"}, token, "n\n")
	if code != exitFailed || len(*seen) != 0 || out != "" || !strings.Contains(errOut, "Revoke app password 7?") {
		t.Fatalf("answered no: exit %d, %d requests, stdout %q, stderr %q", code, len(*seen), out, errOut)
	}

	code, out, errOut = runAppPasswordTest(t, []string{"revoke", "7", "--server", srv.URL}, token, "y\n")
	if code != exitOK || len(*seen) != 1 || !strings.Contains(out, "Revoked app password 7") {
		t.Fatalf("answered yes: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	got := (*seen)[0]
	if got.method != http.MethodDelete || got.path != "/api/v1/accounts/42/credentials/7" || got.auth != "Bearer "+token || got.body != nil {
		t.Fatalf("request %s %s", got.method, got.path)
	}

	code, out, errOut = runAppPasswordTest(t, []string{"revoke", "--server", srv.URL, "--yes", "--json", "7"}, token, "")
	if code != exitOK || strings.Contains(errOut, "[y/N]") || strings.TrimSpace(out) != `{"revoked":7}` {
		t.Fatalf("--yes --json: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestAppPasswordRevokeNotFound(t *testing.T) {
	noTerminal(t)
	srv, _ := appPasswordAPI(t, http.StatusNotFound, `{"error":{"code":"not_found","message":"Credential not found"}}`)
	code, _, errOut := runAppPasswordTest(t, []string{"revoke", "--server", srv.URL, "--yes", "99"}, sessionToken(), "")
	if code != exitFailed || !strings.Contains(errOut, "Credential not found") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestAppPasswordUsage(t *testing.T) {
	for _, args := range [][]string{{"revoke", "--server", "https://mail.example.com"}, {"list", "--server", "http://mail.example.com"}, {"rotate"}} {
		if code, _, _ := runAppPasswordTest(t, args, sessionToken(), ""); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	if code, out, _ := runAppPasswordTest(t, []string{"--help"}, "", ""); code != exitOK || !strings.Contains(out, "--password-stdin") {
		t.Errorf("--help: exit %d", code)
	}
}
