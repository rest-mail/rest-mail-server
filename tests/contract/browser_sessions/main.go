// This command proves browser sessions only in the disposable contract topology.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/auth"
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
type browser struct {
	endpoint string
	client   *http.Client
	cookies  []*http.Cookie
}

func main() {
	endpoint := flag.String("endpoint", "http://10.99.0.20:8080", "disposable topology API")
	spec := flag.String("spec", "", "saved served openapi.json")
	principals := flag.String("principals", ".cache/contract-principals.json", "existing contract fixtures")
	output := flag.String("output", ".cache/contract/browser-sessions.json", "credential-free receipt")
	flag.Parse()
	result, err := runFiles(*endpoint, *spec, *principals)
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

func runFiles(endpoint, specPath, principalsPath string) (report, error) {
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
	return prove(endpoint, spec, fixtures["admin"])
}

func (b *browser) call(method, path string, body []byte, csrf bool) (*http.Response, error) {
	req, err := http.NewRequest(method, b.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range b.cookies {
		if c.Path == "/" || path == c.Path || strings.HasPrefix(path, c.Path+"/") {
			req.AddCookie(c)
		}
		if csrf && c.Name == auth.CSRFCookieName {
			req.Header.Set(auth.CSRFHeaderName, c.Value)
		}
	}
	return b.client.Do(req)
}

// Never print a response body: login and refresh carry credentials in headers.
func readResponse(resp *http.Response) (map[string]interface{}, error) {
	defer resp.Body.Close()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var body map[string]interface{}
	_ = json.Unmarshal(data, &body)
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

var pathParameter = regexp.MustCompile(`\{[^{}]+\}`)

func prove(endpoint string, spec description, fixture principal) (result report, err error) {
	if !strings.HasSuffix(fixture.Address, "@restmail.test") || fixture.Password == "" {
		return result, errors.New("requires an existing restmail.test fixture")
	}
	if len(spec.Paths) == 0 {
		return result, errors.New("the served description has no paths")
	}
	b := &browser{endpoint: strings.TrimRight(endpoint, "/"), client: &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	check := func(label, variant, method, path string, payload []byte, csrf bool, status int, code string) (*http.Response, map[string]interface{}, error) {
		resp, callErr := b.call(method, path, payload, csrf)
		if callErr != nil {
			return nil, nil, fmt.Errorf("%s: request failed", label)
		}
		body, readErr := readResponse(resp)
		result.Checks = append(result.Checks, receipt{label, variant, resp.StatusCode})
		if readErr != nil {
			return resp, body, fmt.Errorf("%s: cannot read response", label)
		}
		if resp.StatusCode != status || (code != "" && errorCode(body) != code) {
			return resp, body, fmt.Errorf("%s: expected %d %s, got %d %s", label, status, code, resp.StatusCode, errorCode(body))
		}
		return resp, body, nil
	}
	payload, _ := json.Marshal(map[string]string{"email": fixture.Address, "password": fixture.Password})
	login, body, err := check("login", "cookie", http.MethodPost, "/api/v1/auth/login", payload, false, 200, "")
	if err != nil {
		return result, err
	}
	if err = sessionCookies(login.Cookies(), false); err != nil {
		return result, err
	}
	b.cookies = login.Cookies()
	// Revoke this probe's session even if a later check fails.
	defer func() {
		resp, e := b.call(http.MethodPost, "/api/v1/auth/logout", nil, true)
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
	if _, _, err = check("own folders", "cookie-without-csrf", http.MethodGet, "/api/v1/accounts/"+account+"/folders", nil, false, 200, ""); err != nil {
		return result, err
	}
	// This valid CSRF header reaches binding, without creating a resource.
	if _, _, err = check("folder validation", "cookie-with-csrf", http.MethodPost, "/api/v1/accounts/"+account+"/folders", []byte(`{"name":""}`), true, 422, ""); err != nil {
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
				if _, _, err = check(op.OperationID, "cookie-without-csrf", strings.ToUpper(method), target, []byte("{"), false, 403, "csrf_failed"); err != nil {
					return result, err
				}
				continue
			}
			// Safe reads must behave identically with and without the CSRF header;
			// their domain/path validation may still refuse the deliberately invalid ID.
			with, e := b.call(strings.ToUpper(method), target, nil, true)
			if e != nil {
				return result, fmt.Errorf("%s: safe control request failed", op.OperationID)
			}
			control, e := readResponse(with)
			if e != nil || with.StatusCode >= 500 || with.StatusCode == 401 || errorCode(control) == "csrf_failed" {
				return result, fmt.Errorf("%s: safe control failed (%d)", op.OperationID, with.StatusCode)
			}
			if _, _, err = check(op.OperationID, "cookie-without-csrf", strings.ToUpper(method), target, nil, false, with.StatusCode, errorCode(control)); err != nil {
				return result, err
			}
		}
	}
	if protectedUnsafe == 0 {
		return result, errors.New("description has no cookie-protected unsafe operation")
	}
	if _, _, err = check("account events", "cookie-without-csrf", http.MethodGet, "/api/v1/accounts/"+account+"/events", nil, false, 200, ""); err != nil {
		return result, err
	}
	refresh, body, err := check("refresh", "cookie-with-csrf", http.MethodPost, "/api/v1/auth/refresh", nil, true, 200, "")
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
	logout, _, err := check("logout", "cookie-with-csrf", http.MethodPost, "/api/v1/auth/logout", nil, true, 204, "")
	if err != nil {
		return result, err
	}
	if err = sessionCookies(logout.Cookies(), true); err != nil {
		return result, err
	}
	if _, _, err = check("revoked refresh", "cookie-with-csrf", http.MethodPost, "/api/v1/auth/refresh", nil, true, 401, ""); err != nil {
		return result, err
	}
	result.Passed = true
	return result, nil
}
