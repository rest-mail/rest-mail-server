package apiclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is the REST API client used by all gateway protocol handlers.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new API client pointing at the given base URL.
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ── Auth ──────────────────────────────────────────────────────────────

type LoginResponse struct {
	Data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		User        struct {
			ID          uint   `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
	} `json:"data"`
}

// Login authenticates a user and returns an access token.
func (c *Client) Login(email, password string) (*LoginResponse, error) {
	body := map[string]string{"email": email, "password": password}
	var resp LoginResponse
	if err := c.post("/api/v1/auth/login", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Mailbox Check ─────────────────────────────────────────────────────

type MailboxCheckResponse struct {
	Data struct {
		Exists    bool   `json:"exists"`
		MailboxID uint   `json:"mailbox_id"`
		Address   string `json:"address"`
	} `json:"data"`
}

// CheckMailbox verifies a recipient address exists.
func (c *Client) CheckMailbox(address string) (*MailboxCheckResponse, error) {
	var resp MailboxCheckResponse
	if err := c.get("/api/mailboxes?address="+url.QueryEscape(address), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Message Delivery ──────────────────────────────────────────────────

type DeliverRequest struct {
	Address      string          `json:"address"`
	MailboxID    uint            `json:"mailbox_id,omitempty"`
	Sender       string          `json:"sender"`
	SenderName   string          `json:"sender_name,omitempty"`
	RecipientsTo json.RawMessage `json:"recipients_to,omitempty"`
	RecipientsCc json.RawMessage `json:"recipients_cc,omitempty"`
	Subject      string          `json:"subject"`
	BodyText     string          `json:"body_text"`
	BodyHTML     string          `json:"body_html,omitempty"`
	MessageID    string          `json:"message_id,omitempty"`
	InReplyTo    string          `json:"in_reply_to,omitempty"`
	References   string          `json:"references,omitempty"`
	RawMessage   string          `json:"raw_message,omitempty"`
	ClientIP     string          `json:"client_ip,omitempty"`
	HeloName     string          `json:"helo_name,omitempty"`
}

type DeliverResponse struct {
	Data struct {
		ID        uint   `json:"id"`
		MailboxID uint   `json:"mailbox_id"`
		Subject   string `json:"subject"`
	} `json:"data"`
}

// DeliverMessage delivers a message to a local mailbox.
func (c *Client) DeliverMessage(req *DeliverRequest) (*DeliverResponse, error) {
	var resp DeliverResponse
	if err := c.post("/api/v1/messages/deliver", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Message Send ──────────────────────────────────────────────────────

// SendRequest represents a message to be sent via the webmail API.
type SendRequest struct {
	From     string   `json:"from"`
	To       []string `json:"to"`
	Cc       []string `json:"cc,omitempty"`
	Bcc      []string `json:"bcc,omitempty"`
	Subject  string   `json:"subject"`
	BodyText string   `json:"body_text"`
	BodyHTML string   `json:"body_html,omitempty"`
}

// SendMessage sends a message via the webmail send API.
func (c *Client) SendMessage(token string, req *SendRequest) error {
	return c.postAuth("/api/v1/messages/send", token, req, nil)
}

// ── Folders ───────────────────────────────────────────────────────────

type Folder struct {
	Name   string `json:"name"`
	Total  int64  `json:"total"`
	Unread int64  `json:"unread"`
}

type FolderListResponse struct {
	Data []Folder `json:"data"`
}

// ListFolders returns all folders for an account.
func (c *Client) ListFolders(token string, accountID uint) (*FolderListResponse, error) {
	var resp FolderListResponse
	if err := c.getAuth(fmt.Sprintf("/api/v1/accounts/%d/folders", accountID), token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Messages ──────────────────────────────────────────────────────────

type MessageSummary struct {
	ID             uint            `json:"id"`
	MailboxID      uint            `json:"mailbox_id"`
	Folder         string          `json:"folder"`
	MessageID      string          `json:"message_id"`
	Sender         string          `json:"sender"`
	SenderName     string          `json:"sender_name"`
	RecipientsTo   json.RawMessage `json:"recipients_to"`
	Subject        string          `json:"subject"`
	SizeBytes      int             `json:"size_bytes"`
	HasAttachments bool            `json:"has_attachments"`
	IsRead         bool            `json:"is_read"`
	IsFlagged      bool            `json:"is_flagged"`
	IsStarred      bool            `json:"is_starred"`
	IsDraft        bool            `json:"is_draft"`
	ReceivedAt     time.Time       `json:"received_at"`
}

type MessageDetail struct {
	MessageSummary
	BodyText   string          `json:"body_text"`
	BodyHTML   string          `json:"body_html"`
	Headers    json.RawMessage `json:"headers"`
	InReplyTo  string          `json:"in_reply_to"`
	References string          `json:"references"`
	ThreadID   string          `json:"thread_id"`
}

type MessageListResponse struct {
	Data       []MessageSummary `json:"data"`
	Pagination *struct {
		Cursor  string `json:"cursor"`
		HasMore bool   `json:"has_more"`
		Total   int64  `json:"total"`
	} `json:"pagination"`
}

type MessageDetailResponse struct {
	Data MessageDetail `json:"data"`
}

// ListMessages returns messages in a folder.
func (c *Client) ListMessages(token string, accountID uint, folder string) (*MessageListResponse, error) {
	var resp MessageListResponse
	path := fmt.Sprintf("/api/v1/accounts/%d/folders/%s/messages?limit=100", accountID, url.PathEscape(folder))
	if err := c.getAuth(path, token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetMessage returns a full message by ID.
func (c *Client) GetMessage(token string, msgID uint) (*MessageDetailResponse, error) {
	var resp MessageDetailResponse
	if err := c.getAuth(fmt.Sprintf("/api/v1/messages/%d", msgID), token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateMessage updates message flags.
func (c *Client) UpdateMessage(token string, msgID uint, updates map[string]interface{}) error {
	return c.patchAuth(fmt.Sprintf("/api/v1/messages/%d", msgID), token, updates, nil)
}

// DeleteMessage deletes a message.
func (c *Client) DeleteMessage(token string, msgID uint) error {
	return c.deleteAuth(fmt.Sprintf("/api/v1/messages/%d", msgID), token)
}

// ── Quota ─────────────────────────────────────────────────────────────

type QuotaResponse struct {
	Data struct {
		QuotaBytes     int64   `json:"quota_bytes"`
		QuotaUsedBytes int64   `json:"quota_used_bytes"`
		MessageCount   int64   `json:"message_count"`
		PercentUsed    float64 `json:"percent_used"`
	} `json:"data"`
}

// GetQuota returns quota usage for an account.
func (c *Client) GetQuota(token string, accountID uint) (*QuotaResponse, error) {
	var resp QuotaResponse
	if err := c.getAuth(fmt.Sprintf("/api/v1/accounts/%d/quota", accountID), token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Search ────────────────────────────────────────────────────────────

// Search performs full-text search across messages.
func (c *Client) Search(token string, accountID uint, query string, folder string) (*MessageListResponse, error) {
	var resp MessageListResponse
	path := fmt.Sprintf("/api/v1/accounts/%d/search?q=%s", accountID, url.QueryEscape(query))
	if folder != "" {
		path += "&folder=" + url.QueryEscape(folder)
	}
	if err := c.getAuth(path, token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Admin Domains ─────────────────────────────────────────────────────

type DomainItem struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	ServerType string `json:"server_type"`
	Active     bool   `json:"active"`
}

type DomainListResponse struct {
	Data []DomainItem `json:"data"`
}

// ListDomains returns all domains (admin only).
func (c *Client) ListDomains(token string) (*DomainListResponse, error) {
	var resp DomainListResponse
	if err := c.getAuth("/api/v1/admin/domains", token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateDomain creates a new domain (admin only).
func (c *Client) CreateDomain(token string, name, serverType string) error {
	body := map[string]interface{}{
		"name":        name,
		"server_type": serverType,
	}
	return c.postAuth("/api/v1/admin/domains", token, body, nil)
}

// DeleteDomain deletes a domain by ID (admin only).
func (c *Client) DeleteDomain(token string, id uint) error {
	return c.deleteAuth(fmt.Sprintf("/api/v1/admin/domains/%d", id), token)
}

// ── Admin Mailboxes ──────────────────────────────────────────────────

type MailboxItem struct {
	ID          uint   `json:"id"`
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	DomainID    uint   `json:"domain_id"`
	Active      bool   `json:"active"`
}

type MailboxListResponse struct {
	Data []MailboxItem `json:"data"`
}

// ListMailboxes returns all mailboxes (admin only).
func (c *Client) ListMailboxes(token string) (*MailboxListResponse, error) {
	var resp MailboxListResponse
	if err := c.getAuth("/api/v1/admin/mailboxes", token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateMailbox creates a new mailbox (admin only).
func (c *Client) CreateMailbox(token string, address, displayName, password string, domainID uint) error {
	body := map[string]interface{}{
		"address":      address,
		"display_name": displayName,
		"password":     password,
		"domain_id":    domainID,
	}
	return c.postAuth("/api/v1/admin/mailboxes", token, body, nil)
}

// DeleteMailbox deletes a mailbox by ID (admin only).
func (c *Client) DeleteMailbox(token string, id uint) error {
	return c.deleteAuth(fmt.Sprintf("/api/v1/admin/mailboxes/%d", id), token)
}

// ResetPassword resets a mailbox password (admin only).
func (c *Client) ResetPassword(token string, id uint, newPassword string) error {
	body := map[string]interface{}{"password": newPassword}
	return c.patchAuth(fmt.Sprintf("/api/v1/admin/mailboxes/%d", id), token, body, nil)
}

// ── Pipelines ────────────────────────────────────────────────────────

type PipelineItem struct {
	ID        uint            `json:"id"`
	DomainID  uint            `json:"domain_id"`
	Direction string          `json:"direction"`
	Filters   json.RawMessage `json:"filters"`
	Active    bool            `json:"active"`
}

type PipelineListResponse struct {
	Data []PipelineItem `json:"data"`
}

// ListPipelines returns pipelines, optionally filtered by domain_id.
func (c *Client) ListPipelines(token string, domainID uint) (*PipelineListResponse, error) {
	var resp PipelineListResponse
	path := "/api/v1/admin/pipelines"
	if domainID > 0 {
		path += fmt.Sprintf("?domain_id=%d", domainID)
	}
	if err := c.getAuth(path, token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TogglePipeline toggles a pipeline's active status.
func (c *Client) TogglePipeline(token string, id uint, active bool) error {
	body := map[string]interface{}{"active": active}
	return c.patchAuth(fmt.Sprintf("/api/v1/admin/pipelines/%d", id), token, body, nil)
}

// ── Queue Stats ──────────────────────────────────────────────────────

type QueueStatsResponse struct {
	Data struct {
		Total   int64 `json:"total"`
		Pending int64 `json:"pending"`
		Failed  int64 `json:"failed"`
	} `json:"data"`
}

// QueueStats returns queue statistics (admin only).
func (c *Client) QueueStats(token string) (*QueueStatsResponse, error) {
	var resp QueueStatsResponse
	if err := c.getAuth("/api/v1/admin/queue/stats", token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Bans ──────────────────────────────────────────────────────────────

type BanListResponse struct {
	Data []struct {
		ID       uint   `json:"id"`
		IP       string `json:"ip"`
		Protocol string `json:"protocol"`
	} `json:"data"`
	Pagination *struct {
		Total int64 `json:"total"`
	} `json:"pagination"`
}

// ListBans returns active bans (admin only).
func (c *Client) ListBans(token string) (*BanListResponse, error) {
	var resp BanListResponse
	if err := c.getAuth("/api/v1/admin/bans?active=true&limit=1", token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── HTTP helpers ──────────────────────────────────────────────────────

func (c *Client) get(path string, out interface{}) error {
	resp, err := c.httpClient.Get(c.baseURL + path)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, out)
}

func (c *Client) getAuth(path, token string, out interface{}) error {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, out)
}

func (c *Client) post(path string, body interface{}, out interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Post(c.baseURL+path, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, out)
}

func (c *Client) postAuth(path, token string, body interface{}, out interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		return c.decodeResponse(resp, out)
	}
	return c.checkStatus(resp)
}

func (c *Client) patchAuth(path, token string, body interface{}, out interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PATCH", c.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		return c.decodeResponse(resp, out)
	}
	return c.checkStatus(resp)
}

func (c *Client) deleteAuth(path, token string) error {
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", path, err)
	}
	defer resp.Body.Close()
	return c.checkStatus(resp)
}

func (c *Client) decodeResponse(resp *http.Response, out interface{}) error {
	if err := c.checkStatus(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return &APIError{
		StatusCode: resp.StatusCode,
		Body:       string(body),
	}
}

// APIError represents an error response from the API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Body)
}
