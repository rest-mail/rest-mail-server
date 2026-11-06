// This command proves browser sessions only in the disposable contract topology.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/rest-mail/rest-mail-server/internal/auth"
)

type principal struct{ Address, Password string }
type operation struct {
	OperationID string                `json:"operationId"`
	Security    []map[string][]string `json:"security"`
}
type description struct {
	Security []map[string][]string           `json:"security"`
	Paths    map[string]map[string]operation `json:"paths"`
}
type receipt struct {
	Operation string `json:"operation"`
	Principal string `json:"principal"`
	Status    int    `json:"status"`
}
type report struct {
	Passed bool      `json:"passed"`
	Checks []receipt `json:"checks"`
}

// sessionStarts is optional state that lets reset and recovery succeed for a disposable
// account other than the fixture admin. The contract run does not provide it yet: a reset
// needs a minted reset_admin token, and recovery an account's authenticator secret and an
// unspent recovery code.
type sessionStarts struct {
	Reset   *resetStart   `json:"reset"`
	Recover *recoverStart `json:"recover"`
}
type resetStart struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}
type recoverStart struct {
	Email        string `json:"email"`
	TOTPSecret   string `json:"totp_secret"`
	RecoveryCode string `json:"recovery_code"`
	NewPassword  string `json:"new_password"`
}
type browser struct {
	endpoint string
	client   *http.Client
	cookies  []*http.Cookie
}

type csrfVariant string

const (
	csrfValid         csrfVariant = "cookie-with-csrf"
	csrfMissing       csrfVariant = "cookie-without-csrf"
	csrfMismatched    csrfVariant = "cookie-mismatched-csrf"
	csrfCookieMissing csrfVariant = "cookie-without-csrf-cookie"
)

func main() {
	endpoint := flag.String("endpoint", "http://10.99.0.20:8080", "disposable topology API")
	spec := flag.String("spec", "", "saved served openapi.json")
	principals := flag.String("principals", ".cache/contract-principals.json", "existing contract fixtures")
	output := flag.String("output", ".cache/contract/browser-sessions.json", "credential-free receipt")
	starts := flag.String("session-starts", "", "optional reset and recovery state for disposable accounts")
	flag.Parse()
	result, err := runFiles(*endpoint, *spec, *principals, *starts)
	data, marshalErr := json.MarshalIndent(result, "", "  ")
	if marshalErr == nil {
		marshalErr = os.WriteFile(*output, append(data, '\n'), 0600)
	}
	if err == nil {
		err = marshalErr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser session contract:", err)
		os.Exit(1)
	}
	fmt.Printf("browser session contract: %d checks passed\n", len(result.Checks))
}

func runFiles(endpoint, specPath, principalsPath, startsPath string) (report, error) {
	var spec description
	var fixtures map[string]principal
	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		return report{}, err
	}
	if err = json.Unmarshal(specBytes, &spec); err != nil {
		return report{}, err
	}
	fixtureBytes, err := os.ReadFile(principalsPath)
	if err != nil {
		return report{}, err
	}
	if err = json.Unmarshal(fixtureBytes, &fixtures); err != nil {
		return report{}, err
	}
	if startsPath == "" {
		return prove(endpoint, spec, fixtures["admin"])
	}
	var starts sessionStarts
	startBytes, err := os.ReadFile(startsPath)
	if err != nil {
		return report{}, err
	}
	if err = json.Unmarshal(startBytes, &starts); err != nil {
		return report{}, errors.New("invalid session-starts file")
	}
	return prove(endpoint, spec, fixtures["admin"], starts)
}

func (b *browser) call(method, path string, body []byte, csrf csrfVariant) (*http.Response, error) {
	req, err := http.NewRequest(method, b.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range b.cookies {
		if (c.Path == "/" || path == c.Path || strings.HasPrefix(path, c.Path+"/")) &&
			!(csrf == csrfCookieMissing && c.Name == auth.CSRFCookieName) {
			req.AddCookie(c)
		}
		if c.Name == auth.CSRFCookieName {
			switch csrf {
			case csrfValid, csrfCookieMissing:
				req.Header.Set(auth.CSRFHeaderName, c.Value)
			case csrfMismatched:
				req.Header.Set(auth.CSRFHeaderName, c.Value+"-mismatch")
			}
		}
	}
	return b.client.Do(req)
}

// Never print a response body: login and refresh carry credentials in headers.
func readResponse(resp *http.Response) (map[string]interface{}, error) {
	defer resp.Body.Close()
	mediaType := ""
	if header := resp.Header.Get("Content-Type"); header != "" {
		var err error
		mediaType, _, err = mime.ParseMediaType(header)
		if err != nil {
			return nil, errors.New("invalid response media type")
		}
	}
	if mediaType == "text/event-stream" {
		return nil, nil
	}
	// Read one byte beyond the allowance so a truncated valid prefix cannot pass.
	const maxResponseBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("cannot read response")
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("response exceeds byte limit")
	}
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return nil, nil
	}
	if len(data) == 0 && (resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified ||
		(resp.Request != nil && resp.Request.Method == http.MethodHead)) {
		return nil, nil
	}
	var body map[string]interface{}
	if json.Unmarshal(data, &body) != nil || body == nil {
		return nil, errors.New("invalid JSON response object")
	}
	return body, nil
}

func errorCode(body map[string]interface{}) string {
	value, _ := body["error"].(map[string]interface{})
	code, _ := value["code"].(string)
	return code
}

func sessionCookies(cookies []*http.Cookie, expired bool) error {
	expected := map[string]string{auth.AccessCookieName: "/", auth.RefreshCookieName: "/api/v1/auth", auth.CSRFCookieName: "/"}
	seen := map[string]bool{}
	for _, c := range cookies {
		path, ok := expected[c.Name]
		if !ok {
			continue
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate %s cookie", c.Name)
		}
		seen[c.Name] = true
		if !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != path || c.HttpOnly != (c.Name != auth.CSRFCookieName) || c.Domain != "" {
			return fmt.Errorf("%s cookie has wrong security attributes", c.Name)
		}
		if expired {
			if c.Value != "" || c.MaxAge >= 0 {
				return fmt.Errorf("%s cookie was not expired", c.Name)
			}
		} else if c.Value == "" || c.MaxAge <= 0 {
			return fmt.Errorf("%s cookie has no live value or lifetime", c.Name)
		}
	}
	for name := range expected {
		if !seen[name] {
			return fmt.Errorf("missing %s cookie", name)
		}
	}
	return nil
}

// noSessionCookies refuses a refusal that sets or clears any session cookie.
func noSessionCookies(cookies []*http.Cookie) error {
	for _, c := range cookies {
		switch c.Name {
		case auth.AccessCookieName, auth.RefreshCookieName, auth.CSRFCookieName:
			return fmt.Errorf("refusal set the %s cookie", c.Name)
		}
	}
	return nil
}

var pathParameter = regexp.MustCompile(`\{[^{}]+\}`)

// refusedStartPassword is what the refused reset and recovery send. Those refusals
// are about the token and the codes, so the password has to pass the chosen-password
// rule, which comes first on a recovery and takes nothing from the account's address:
// the throwaway contract-recover-<n>@restmail.test.
const refusedStartPassword = "amber-quorum-lantern-unused-58"

func prove(endpoint string, spec description, fixture principal, starts ...sessionStarts) (result report, err error) {
	if !strings.HasSuffix(fixture.Address, "@restmail.test") || fixture.Password == "" {
		return result, errors.New("requires an existing restmail.test fixture")
	}
	if len(spec.Paths) == 0 {
		return result, errors.New("the served description has no paths")
	}
	b := &browser{endpoint: strings.TrimRight(endpoint, "/"), client: &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	check := func(label string, variant csrfVariant, method, path string, payload []byte, status int, code string) (*http.Response, map[string]interface{}, error) {
		resp, callErr := b.call(method, path, payload, variant)
		if callErr != nil {
			return nil, nil, fmt.Errorf("%s: request failed", label)
		}
		body, readErr := readResponse(resp)
		result.Checks = append(result.Checks, receipt{label, string(variant), resp.StatusCode})
		if readErr != nil {
			return resp, body, fmt.Errorf("%s: cannot read response", label)
		}
		if resp.StatusCode != status || (code != "" && errorCode(body) != code) {
			return resp, body, fmt.Errorf("%s (%s): expected %d %s, got status %d or a different error code", label, variant, status, code, resp.StatusCode)
		}
		return resp, body, nil
	}
	payload, _ := json.Marshal(map[string]string{"email": fixture.Address, "password": fixture.Password})
	login, body, err := check("login", "cookie", http.MethodPost, "/api/v1/auth/login", payload, 200, "")
	if err != nil {
		return result, err
	}
	if err = sessionCookies(login.Cookies(), false); err != nil {
		return result, err
	}
	b.cookies = login.Cookies()
	// A started session must not reuse a refresh or CSRF value issued before it.
	issued := map[string]bool{}
	remember := func(cookies []*http.Cookie) {
		for _, c := range cookies {
			if c.Name == auth.RefreshCookieName || c.Name == auth.CSRFCookieName {
				issued[c.Value] = true
			}
		}
	}
	remember(b.cookies)
	// Revoke this probe's session even if a later check fails.
	defer func() {
		resp, e := b.call(http.MethodPost, "/api/v1/auth/logout", nil, csrfValid)
		if e == nil {
			_ = resp.Body.Close()
		}
	}()
	data, _ := body["data"].(map[string]interface{})
	if data["access_token"] != nil || data["refresh_token"] != nil {
		return result, errors.New("browser response exposes a token")
	}
	user, _ := data["user"].(map[string]interface{})
	id, _ := user["id"].(float64)
	if id < 1 {
		return result, errors.New("browser login has no account id")
	}
	account := strconv.FormatUint(uint64(id), 10)
	if _, _, err = check("own folders", csrfMissing, http.MethodGet, "/api/v1/accounts/"+account+"/folders", nil, 200, ""); err != nil {
		return result, err
	}
	// This valid CSRF header reaches binding, without creating a resource.
	if _, _, err = check("folder validation", csrfValid, http.MethodPost, "/api/v1/accounts/"+account+"/folders", []byte(`{"name":""}`), 422, ""); err != nil {
		return result, err
	}
	paths := make([]string, 0, len(spec.Paths))
	for path := range spec.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	protectedUnsafe := 0
	for _, path := range paths {
		methods := spec.Paths[path]
		for _, method := range []string{"get", "head", "options", "post", "put", "patch", "delete", "trace"} {
			op, ok := methods[method]
			if !ok {
				continue
			}
			security := op.Security
			if security == nil {
				security = spec.Security
			}
			cookie := false
			for _, requirement := range security {
				if _, ok := requirement["CookieAuth"]; ok {
					cookie = true
				}
			}
			if !cookie {
				continue
			}
			// Invalid resource IDs and malformed JSON avoid handler side effects if a
			// regression lets a request through CSRF. No excluded operation is hidden.
			target := pathParameter.ReplaceAllString(path, "contract-invalid-id")
			safe := method == "get" || method == "head" || method == "options"
			if !safe {
				protectedUnsafe++
				for _, variant := range []csrfVariant{csrfMissing, csrfMismatched, csrfCookieMissing} {
					if _, _, err = check(op.OperationID, variant, strings.ToUpper(method), target, []byte("{"), 403, "csrf_failed"); err != nil {
						return result, err
					}
				}
				continue
			}
			// Safe reads must behave identically with and without the CSRF header;
			// their domain/path validation may still refuse the deliberately invalid ID.
			with, e := b.call(strings.ToUpper(method), target, nil, csrfValid)
			if e != nil {
				return result, fmt.Errorf("%s: safe control request failed", op.OperationID)
			}
			control, e := readResponse(with)
			if e != nil || with.StatusCode >= 500 || with.StatusCode == 401 || errorCode(control) == "csrf_failed" {
				return result, fmt.Errorf("%s: safe control failed (%d)", op.OperationID, with.StatusCode)
			}
			for _, variant := range []csrfVariant{csrfMissing, csrfMismatched, csrfCookieMissing} {
				resp, body, e := check(op.OperationID, variant, strings.ToUpper(method), target, nil, with.StatusCode, "")
				if e != nil {
					return result, e
				}
				if errorCode(body) != errorCode(control) {
					return result, fmt.Errorf("%s (%s): safe response error code differs from control (status %d)", op.OperationID, variant, resp.StatusCode)
				}
			}
		}
	}
	if protectedUnsafe == 0 {
		return result, errors.New("description has no cookie-protected unsafe operation")
	}
	if _, _, err = check("account events", csrfMissing, http.MethodGet, "/api/v1/accounts/"+account+"/events", nil, 200, ""); err != nil {
		return result, err
	}
	// Reset and recovery are public and rely on SameSite=Strict: a browser that carries a
	// session but no CSRF header reaches them, and a refusal starts no session. A wrong
	// token spends nothing, and a refused recovery is charged only to a throwaway address.
	refusedReset, _ := json.Marshal(map[string]string{"token": "contract-invalid-token", "password": refusedStartPassword})
	refusedRecovery, _ := json.Marshal(map[string]string{"email": fmt.Sprintf("contract-recover-%d@restmail.test", time.Now().UnixNano()),
		"totp_code": "000000", "recovery_code": "contract-invalid-code", "new_password": refusedStartPassword})
	for _, refusal := range []struct {
		label, path, code string
		payload           []byte
	}{
		{"refused reset", "/api/v1/auth/reset", "invalid_token", refusedReset},
		{"refused recovery", "/api/v1/auth/recover", "recovery_failed", refusedRecovery},
	} {
		resp, _, e := check(refusal.label, csrfMissing, http.MethodPost, refusal.path, refusal.payload, 401, refusal.code)
		if e != nil {
			return result, e
		}
		if err = noSessionCookies(resp.Cookies()); err != nil {
			return result, fmt.Errorf("%s: %w", refusal.label, err)
		}
	}
	refresh, body, err := check("refresh", csrfValid, http.MethodPost, "/api/v1/auth/refresh", nil, 200, "")
	if err != nil {
		return result, err
	}
	if err = sessionCookies(refresh.Cookies(), false); err != nil {
		return result, err
	}
	data, _ = body["data"].(map[string]interface{})
	if data["access_token"] != nil || data["refresh_token"] != nil {
		return result, errors.New("browser refresh exposes a token")
	}
	for _, old := range b.cookies {
		if old.Name != auth.RefreshCookieName && old.Name != auth.CSRFCookieName {
			continue
		}
		for _, fresh := range refresh.Cookies() {
			if fresh.Name == old.Name && fresh.Value == old.Value {
				return result, fmt.Errorf("refresh did not rotate %s", old.Name)
			}
		}
	}
	b.cookies = refresh.Cookies()
	remember(b.cookies)
	logout, _, err := check("logout", csrfValid, http.MethodPost, "/api/v1/auth/logout", nil, 204, "")
	if err != nil {
		return result, err
	}
	if err = sessionCookies(logout.Cookies(), true); err != nil {
		return result, err
	}
	if _, _, err = check("revoked refresh", csrfValid, http.MethodPost, "/api/v1/auth/refresh", nil, 401, ""); err != nil {
		return result, err
	}
	// A reset or recovery that succeeds signs in like login: the same three cookies, fresh
	// values, no token in the body, and a session that signs out like any other. The person
	// is locked out, so the browser starts with no cookies and sends no CSRF header.
	started := func(label, path string, payload []byte) error {
		b.cookies = nil
		resp, body, e := check(label, "cookie", http.MethodPost, path, payload, 200, "")
		if e != nil {
			return e
		}
		if e = sessionCookies(resp.Cookies(), false); e != nil {
			return fmt.Errorf("%s: %w", label, e)
		}
		for _, c := range resp.Cookies() {
			if issued[c.Value] && (c.Name == auth.RefreshCookieName || c.Name == auth.CSRFCookieName) {
				return fmt.Errorf("%s reused an earlier %s value", label, c.Name)
			}
		}
		data, _ := body["data"].(map[string]interface{})
		if data["access_token"] != nil || data["refresh_token"] != nil {
			return fmt.Errorf("browser %s exposes a token", label)
		}
		user, _ := data["user"].(map[string]interface{})
		email, _ := user["email"].(string)
		if !strings.HasSuffix(email, "@restmail.test") || strings.EqualFold(email, fixture.Address) {
			return fmt.Errorf("%s did not start a session for a disposable account", label)
		}
		b.cookies = resp.Cookies()
		remember(b.cookies)
		end, _, e := check(label+" logout", csrfValid, http.MethodPost, "/api/v1/auth/logout", nil, 204, "")
		if e != nil {
			return e
		}
		return sessionCookies(end.Cookies(), true)
	}
	for _, start := range starts {
		if start.Reset != nil {
			payload, _ := json.Marshal(map[string]string{"token": start.Reset.Token, "password": start.Reset.Password})
			if err = started("reset", "/api/v1/auth/reset", payload); err != nil {
				return result, err
			}
		}
		if start.Recover != nil {
			// Recovery changes the account's password: never the fixture admin's.
			if !strings.HasSuffix(start.Recover.Email, "@restmail.test") || strings.EqualFold(start.Recover.Email, fixture.Address) {
				return result, errors.New("recovery requires a disposable restmail.test account")
			}
			code, e := totp.GenerateCode(start.Recover.TOTPSecret, time.Now())
			if e != nil {
				return result, errors.New("recovery fixture has an invalid authenticator secret")
			}
			payload, _ := json.Marshal(map[string]string{"email": start.Recover.Email, "totp_code": code,
				"recovery_code": start.Recover.RecoveryCode, "new_password": start.Recover.NewPassword})
			if err = started("recovery", "/api/v1/auth/recover", payload); err != nil {
				return result, err
			}
		}
	}
	result.Passed = true
	return result, nil
}
