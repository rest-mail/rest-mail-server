package e2e

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// ── Configuration ────────────────────────────────────────────────────

var (
	apiBaseURL     = envOr("API_BASE_URL", "http://localhost:8080")
	mail1SMTPAddr  = envOr("MAIL1_SMTP_ADDR", "172.20.0.11:25")
	mail2SMTPAddr  = envOr("MAIL2_SMTP_ADDR", "172.20.0.12:25")
	mail3SMTPAddr  = envOr("MAIL3_SMTP_ADDR", "172.20.0.13:25")
	mail1IMAPAddr  = envOr("MAIL1_IMAP_ADDR", "172.20.0.14:143")
	mail2IMAPAddr  = envOr("MAIL2_IMAP_ADDR", "172.20.0.15:143")
	mail3IMAPAddr  = envOr("MAIL3_IMAP_ADDR", "172.20.0.13:143")
	mail1POP3Addr  = envOr("MAIL1_POP3_ADDR", "172.20.0.14:110")
	mail3POP3Addr  = envOr("MAIL3_POP3_ADDR", "172.20.0.13:110")
	mail1SubmitAddr = envOr("MAIL1_SUBMIT_ADDR", "172.20.0.11:587")
	mail3SubmitAddr = envOr("MAIL3_SUBMIT_ADDR", "172.20.0.13:587")
	dnsServerAddr  = envOr("DNS_SERVER_ADDR", "172.20.0.3:53")
	adminPassword  = envOr("ADMIN_PASSWORD", "password123")
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

// login authenticates and stores the access token.
func (c *apiClient) login(email, password string) error {
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

	var result struct {
		Data struct {
			AccessToken string `json:"access_token"`
			User        struct {
				ID uint `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	c.token = result.Data.AccessToken
	return nil
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
	t.Run("Stage8_TUIFlows", testStage8TUIFlows)
	t.Run("Stage9_DatabaseConsistency", testStage9DatabaseConsistency)
	t.Run("Stage10_Verification", testStage10Verification)
}
