package e2e

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// ── Configuration ────────────────────────────────────────────────────

// The suite runs as a user ON the simulated internet: `task test:e2e` executes
// it in a container attached to the testbed mailnet with the testbed dnsmasq
// as its only resolver, so mail hosts are addressed by their DNS names.
// Exceptions addressed by infra IP, documented inline:
//   - the API (10.99.0.20 is the testbed contract; it has no bare DNS name)
//   - the reference servers' IMAP/POP3: their DNS models a unified host
//     (SRV → mail1.test:143 = postfix's IP) but dovecot is a separate
//     container — until the reference fragments grow imap./pop3. subdomain
//     records, the suite dials dovecot's static IP directly.
var (
	apiBaseURL       = envOr("API_BASE_URL", "http://10.99.0.20:8080")
	mail1SMTPAddr    = envOr("MAIL1_SMTP_ADDR", "mail1.test:25")
	mail2SMTPAddr    = envOr("MAIL2_SMTP_ADDR", "mail2.test:25")
	restmailSMTPAddr = envOr("RESTMAIL_SMTP_ADDR", "restmail.test:25")
	// The reference servers keep the legacy cleartext ports on purpose: they are the
	// simulated internet rest-mail has to interoperate with, and postfix/dovecot there
	// still offer 143/110/587 with STARTTLS.
	mail1IMAPAddr = envOr("MAIL1_IMAP_ADDR", "10.99.0.111:143")
	mail2IMAPAddr = envOr("MAIL2_IMAP_ADDR", "10.99.0.112:143")
	mail1POP3Addr = envOr("MAIL1_POP3_ADDR", "10.99.0.111:110")
	// rest-mail's own client access is implicit TLS only — there is nothing listening on
	// 143, 110 or 587 to connect to.
	restmailIMAPAddr   = envOr("RESTMAIL_IMAP_ADDR", "imap.restmail.test:993")
	restmailPOP3Addr   = envOr("RESTMAIL_POP3_ADDR", "pop3.restmail.test:995")
	mail1SubmitAddr    = envOr("MAIL1_SUBMIT_ADDR", "mail1.test:587")
	restmailSubmitAddr = envOr("RESTMAIL_SUBMIT_ADDR", "restmail.test:465")
	adminPassword      = envOr("ADMIN_PASSWORD", "password123")
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// requireStack is set when E2E_REQUIRE_STACK is non-empty. The CI e2e job sets
// it so that "stack not reachable" preconditions (no admin token, gateway not
// dialable, …) become HARD failures instead of skips: a misconfigured or
// half-up topology must never make the suite green by skipping every stage.
// Locally the flag is unset, so those same conditions still skip — running
// `go test ./...` on a laptop without the testbed keeps passing.
var requireStack = os.Getenv("E2E_REQUIRE_STACK") != ""

// skipOrFail records a "stack not reachable" precondition. With
// E2E_REQUIRE_STACK set it is fatal (CI); otherwise it skips (local dev).
func skipOrFail(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	if requireStack {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// ── HTTP Client ──────────────────────────────────────────────────────

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

// ── API Helper ───────────────────────────────────────────────────────

type apiClient struct {
	baseURL string
	token   string
}

func newAPIClient() *apiClient {
	return &apiClient{baseURL: apiBaseURL}
}

func (c *apiClient) get(path string) (*http.Response, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return httpClient.Do(req)
}

func (c *apiClient) post(path string, body interface{}) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return httpClient.Do(req)
}

func (c *apiClient) patch(path string, body interface{}) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("PATCH", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return httpClient.Do(req)
}

func (c *apiClient) delete(path string) (*http.Response, error) {
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return httpClient.Do(req)
}

// accessCookieName is the httpOnly session cookie the API sets on a successful
// login (internal/auth.AccessCookieName). Since #194 the access token is
// delivered ONLY as this cookie — it is no longer in the login response body —
// so the harness reads it from the Set-Cookie header. The Go HTTP client can see
// httpOnly cookies via resp.Cookies() (httpOnly only hides them from browser
// JavaScript, not from the HTTP client), and the harness then replays the value
// as an Authorization: Bearer header on subsequent calls. The middleware accepts
// both cookie and Bearer, and the Bearer path is exempt from the CSRF check, so
// no cookie jar or X-CSRF-Token plumbing is needed here.
const accessCookieName = "restmail_access"

// tokenFromCookies extracts the access-token value from a login response's
// Set-Cookie header, returning "" when the cookie is absent.
func tokenFromCookies(resp *http.Response) string {
	for _, ck := range resp.Cookies() {
		if ck.Name == accessCookieName {
			return ck.Value
		}
	}
	return ""
}

// loginAdmin authenticates as an RBAC admin user (admin_users table — the
// seeded `admin` account) and stores the access token. The same endpoint
// serves both flows: `username` selects the admin path, `email` the mailbox
// path.
func (c *apiClient) rawLoginAdmin(username, password string) error {
	resp, err := c.post("/api/v1/auth/login", map[string]string{
		"username": username,
		"password": password,
	})
	if err != nil {
		return fmt.Errorf("admin login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("admin login failed (%d): %s", resp.StatusCode, body)
	}
	c.token = tokenFromCookies(resp)
	if c.token == "" {
		return fmt.Errorf("admin login: no %s cookie in response", accessCookieName)
	}
	return nil
}

// login authenticates and stores the access token.
func (c *apiClient) rawLogin(email, password string) error {
	resp, err := c.post("/api/v1/auth/login", map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("login failed (%d): %s", resp.StatusCode, body)
	}

	c.token = tokenFromCookies(resp)
	if c.token == "" {
		return fmt.Errorf("login: no %s cookie in response", accessCookieName)
	}
	return nil
}

// ── Cached auth (rate-limit aware) ───────────────────────────────────
//
// The API enforces a per-client-IP token-bucket rate limit on
// /api/v1/auth/login (burst 15 @ 1 rps, on by default). This suite runs all 13
// stages from a SINGLE IP (the golang test container on the mailnet) and many
// stages authenticate the same principals (the seeded admin, testuser, eve, …)
// over and over. A fresh login every time drains the bucket and trips 429
// rate_limited part-way through the run — the limiter doing exactly its job
// against what looks like credential brute-force. A real client logs in ONCE
// and reuses its access token (valid 15m by default, far longer than a whole
// suite run), so login()/loginAdmin() memoize the token per (identifier,
// password). This keeps the rate limiter ON — full security coverage — while
// staying within its budget. Negative-auth checks in the suite all go through
// the SMTP/IMAP/POP3 protocols (never this HTTP endpoint), and the one HTTP
// password change re-logs in under a NEW password (a distinct cache key), so
// caching never masks an auth regression.
var (
	tokenCacheMu sync.Mutex
	tokenCache   = map[string]string{}
)

func (c *apiClient) cachedLogin(key, identifier, password string, admin bool) error {
	tokenCacheMu.Lock()
	tok, ok := tokenCache[key]
	tokenCacheMu.Unlock()
	if ok {
		c.token = tok
		return nil
	}

	var err error
	if admin {
		err = c.rawLoginAdmin(identifier, password)
	} else {
		err = c.rawLogin(identifier, password)
	}
	if err != nil {
		return err
	}

	tokenCacheMu.Lock()
	tokenCache[key] = c.token
	tokenCacheMu.Unlock()
	return nil
}

// login authenticates as a mailbox user, reusing a cached token when present.
func (c *apiClient) login(email, password string) error {
	return c.cachedLogin("user\x00"+email+"\x00"+password, email, password, false)
}

// loginAdmin authenticates as an RBAC admin, reusing a cached token when present.
func (c *apiClient) loginAdmin(username, password string) error {
	return c.cachedLogin("admin\x00"+username+"\x00"+password, username, password, true)
}

// ── JSON decode helper ───────────────────────────────────────────────

func decodeJSON(resp *http.Response, out interface{}) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func readBody(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// ── Assertion helpers ────────────────────────────────────────────────

func requireStatus(t *testing.T, resp *http.Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		body := readBody(resp)
		t.Fatalf("expected status %d, got %d: %s", expected, resp.StatusCode, body)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── Polling / Wait helpers ───────────────────────────────────────────

// waitForAPI polls the health endpoint until the API is reachable.
func waitForAPI(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(apiBaseURL + "/api/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("API not reachable at %s after %s", apiBaseURL, timeout)
}

// waitForMessage polls for a message matching the subject in a mailbox.
func waitForMessage(t *testing.T, client *apiClient, accountID uint, folder, subject string, timeout time.Duration) uint {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.get(fmt.Sprintf("/api/v1/accounts/%d/folders/%s/messages?limit=50", accountID, folder))
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var result struct {
			Data []struct {
				ID      uint   `json:"id"`
				Subject string `json:"subject"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &result); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		for _, msg := range result.Data {
			if msg.Subject == subject {
				return msg.ID
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("message with subject %q not found in account %d folder %s after %s", subject, accountID, folder, timeout)
	return 0
}

// ── Admin helpers ────────────────────────────────────────────────────

type domainInfo struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	ServerType string `json:"server_type"`
}

type mailboxInfo struct {
	ID          uint   `json:"id"`
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	DomainID    uint   `json:"domain_id"`
}

// createDomain creates a domain via the admin API.
func createDomain(t *testing.T, client *apiClient, name, serverType string) domainInfo {
	t.Helper()
	resp, err := client.post("/api/v1/admin/domains", map[string]string{
		"name":        name,
		"server_type": serverType,
	})
	requireNoError(t, err)

	if resp.StatusCode == http.StatusConflict {
		// Already exists, fetch it
		resp.Body.Close()
		resp, err = client.get("/api/v1/admin/domains")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var list struct {
			Data []domainInfo `json:"data"`
		}
		resp2, _ := client.get("/api/v1/admin/domains")
		if err := decodeJSON(resp2, &list); err != nil {
			t.Fatalf("decode domains list: %v", err)
		}
		for _, d := range list.Data {
			if d.Name == name {
				return d
			}
		}
		t.Fatalf("domain %s not found after conflict", name)
	}

	requireStatus(t, resp, http.StatusCreated)
	var result struct {
		Data domainInfo `json:"data"`
	}
	resp2, _ := client.post("/api/v1/admin/domains", map[string]string{
		"name":        name,
		"server_type": serverType,
	})
	// The first resp was already consumed for status check; re-create
	resp2.Body.Close()

	// Actually decode from the original response
	// We need to re-do the call since we consumed the body in requireStatus
	resp3, err := client.post("/api/v1/admin/domains", map[string]string{
		"name":        name,
		"server_type": serverType,
	})
	requireNoError(t, err)
	defer resp3.Body.Close()

	// It will be a conflict now
	if resp3.StatusCode == http.StatusConflict {
		// Fetch it
		return getDomainByName(t, client, name)
	}
	if err := json.NewDecoder(resp3.Body).Decode(&result); err != nil {
		t.Fatalf("decode create domain: %v", err)
	}
	return result.Data
}

func getDomainByName(t *testing.T, client *apiClient, name string) domainInfo {
	t.Helper()
	resp, err := client.get("/api/v1/admin/domains")
	requireNoError(t, err)

	var list struct {
		Data []domainInfo `json:"data"`
	}
	if err := decodeJSON(resp, &list); err != nil {
		t.Fatalf("decode domains: %v", err)
	}
	for _, d := range list.Data {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("domain %s not found", name)
	return domainInfo{}
}

// createMailbox creates a mailbox via the admin API.
func createMailbox(t *testing.T, client *apiClient, address, password, displayName string) mailboxInfo {
	t.Helper()
	resp, err := client.post("/api/v1/admin/mailboxes", map[string]string{
		"address":      address,
		"password":     password,
		"display_name": displayName,
	})
	requireNoError(t, err)

	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return getMailboxByAddress(t, client, address)
	}

	var result struct {
		Data mailboxInfo `json:"data"`
	}
	if resp.StatusCode != http.StatusCreated {
		body := readBody(resp)
		t.Fatalf("create mailbox %s: status %d: %s", address, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode create mailbox: %v", err)
	}
	resp.Body.Close()
	return result.Data
}

func getMailboxByAddress(t *testing.T, client *apiClient, address string) mailboxInfo {
	t.Helper()
	resp, err := client.get("/api/v1/admin/mailboxes")
	requireNoError(t, err)

	var list struct {
		Data []mailboxInfo `json:"data"`
	}
	if err := decodeJSON(resp, &list); err != nil {
		t.Fatalf("decode mailboxes: %v", err)
	}
	for _, m := range list.Data {
		if m.Address == address {
			return m
		}
	}
	t.Fatalf("mailbox %s not found", address)
	return mailboxInfo{}
}

// ── Stage ordering via subtests ──────────────────────────────────────

// TestStages runs all test stages in order. If an earlier stage fails,
// later stages are skipped — there's no point testing advanced features
// against broken infrastructure.
func TestStages(t *testing.T) {
	waitForAPI(t, 60*time.Second)

	// Each stage returns true if it passed. Later stages check earlier results.
	t.Run("Stage1_Infrastructure", testStage1Infrastructure)

	if t.Failed() {
		t.Fatal("Stage 1 failed — cannot proceed to Stage 2+")
	}

	t.Run("Stage2_TraditionalMail", testStage2TraditionalMail)

	if t.Failed() {
		t.Fatal("Stage 2 failed — cannot proceed to Stage 3+ (traditional mail is the reference implementation)")
	}

	t.Run("Stage3_GatewayInbound", testStage3GatewayInbound)
	t.Run("Stage4_GatewayOutbound", testStage4GatewayOutbound)
	t.Run("Stage5_Indistinguishability", testStage5Indistinguishability)
	t.Run("Stage6_RestmailUpgrade", testStage6RestmailUpgrade)
	t.Run("Stage7_WebmailFlows", testStage7WebmailFlows)
	t.Run("Stage8_ConsoleFlows", testStage8ConsoleFlows)
	t.Run("Stage9_DatabaseConsistency", testStage9DatabaseConsistency)
	t.Run("Stage10_Verification", testStage10Verification)
	t.Run("Stage11_QueueRetry", testStage11QueueRetry)
	t.Run("Stage12_BounceDSN", testStage12BounceDSN)
	t.Run("Stage13_ImapIdle", testStage13ImapIdle)
}

// e2eInboundFilters mirrors pipeline.DefaultInboundPipeline, minus greylist:
// greylisting defers the first attempt for a new (sender, recipient, ip)
// triplet, which forces every single-shot delivery assertion in the suite to
// retry-or-fail. No stage asserts greylist behaviour (the outbound queue's
// defer/retry — stages 11/12 — is a separate mechanism), so it's dropped here.
// SPF/DKIM/DMARC stay on: senders inject from no-policy domains (no _dmarc
// record) so DMARC returns "none" rather than enforcing a p=reject.
const e2eInboundFilters = `[
 {"name":"size_check","type":"action","enabled":true,"config":{"max_size_mb":25}},
 {"name":"spf_check","type":"action","enabled":true,"config":{"fail_action":"tag"}},
 {"name":"dkim_verify","type":"transform","enabled":true,"config":{"fail_action":"tag"}},
 {"name":"arc_verify","type":"transform","enabled":true,"config":{}},
 {"name":"dmarc_check","type":"action","enabled":true,"config":{"fail_action":"quarantine"}},
 {"name":"domain_allowlist","type":"action","enabled":true,"config":{}},
 {"name":"contact_whitelist","type":"action","enabled":true,"config":{}},
 {"name":"header_validate","type":"action","enabled":true,"config":{}},
 {"name":"recipient_check","type":"action","enabled":true,"config":{}},
 {"name":"extract_attachments","type":"transform","enabled":true,"config":{"storage_path":"/attachments"}},
 {"name":"sieve","type":"transform","enabled":true,"config":{}}
]`

// ensureE2EPipeline installs (or updates) restmail.test's inbound pipeline
// with e2eInboundFilters. Idempotent: patches the existing inbound pipeline
// if one exists, creates it otherwise.
func ensureE2EPipeline(t *testing.T, client *apiClient) {
	t.Helper()

	resp, err := client.get("/api/v1/admin/domains")
	requireNoError(t, err)
	var domResult struct {
		Data []struct {
			ID   uint   `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&domResult)
	resp.Body.Close()
	requireNoError(t, err)
	var domainID uint
	for _, d := range domResult.Data {
		if d.Name == "restmail.test" {
			domainID = d.ID
		}
	}
	if domainID == 0 {
		t.Fatal("restmail.test domain not found")
	}

	filters := json.RawMessage(e2eInboundFilters)

	resp, err = client.get(fmt.Sprintf("/api/v1/admin/pipelines?domain_id=%d", domainID))
	requireNoError(t, err)
	var pipeResult struct {
		Data []struct {
			ID        uint   `json:"id"`
			Direction string `json:"direction"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&pipeResult)
	resp.Body.Close()
	requireNoError(t, err)

	for _, p := range pipeResult.Data {
		if p.Direction == "inbound" {
			resp, err := client.patch(fmt.Sprintf("/api/v1/admin/pipelines/%d", p.ID),
				map[string]interface{}{"filters": filters, "active": true})
			requireNoError(t, err)
			body := readBody(resp)
			if resp.StatusCode >= 300 {
				t.Fatalf("update e2e inbound pipeline: %d: %s", resp.StatusCode, body)
			}
			t.Logf("updated inbound pipeline %d with e2e pipeline (no greylist)", p.ID)
			return
		}
	}

	resp, err = client.post("/api/v1/admin/pipelines",
		map[string]interface{}{"domain_id": domainID, "direction": "inbound", "filters": filters, "active": true})
	requireNoError(t, err)
	body := readBody(resp)
	if resp.StatusCode >= 300 {
		t.Fatalf("create e2e inbound pipeline: %d: %s", resp.StatusCode, body)
	}
	t.Log("created inbound pipeline with e2e pipeline (no greylist)")
}

// restmailInbox logs in as a restmail mailbox user and returns an authed
// client plus the user's own primary webmail-account id — the {id} that the
// /accounts/{id}/folders/... message endpoints expect (NOT the mailbox id).
// Both seeded and admin-created mailboxes get a primary webmail account
// automatically.
func restmailInbox(t *testing.T, address, password string) (*apiClient, uint) {
	t.Helper()
	c := newAPIClient()
	requireNoError(t, c.login(address, password))
	resp, err := c.get("/api/v1/accounts")
	requireNoError(t, err)
	var result struct {
		Data []struct {
			ID        uint `json:"id"`
			IsPrimary bool `json:"is_primary"`
		} `json:"data"`
	}
	requireNoError(t, decodeJSON(resp, &result))
	for _, a := range result.Data {
		if a.IsPrimary {
			return c, a.ID
		}
	}
	if len(result.Data) > 0 {
		return c, result.Data[0].ID
	}
	t.Fatalf("no webmail account for %s", address)
	return nil, 0
}

// requireSuccess accepts any 2xx (e.g. /messages/send returns 201 Created).
func requireSuccess(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("expected 2xx, got %d: %s", resp.StatusCode, readBody(resp))
	}
}
