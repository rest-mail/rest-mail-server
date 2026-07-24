package apiclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	// defaultRequestTimeout bounds every gateway↔API call when no size-aware
	// message deadline is configured (WithMessageDeadline). It matches the
	// historical fixed client timeout.
	defaultRequestTimeout = 30 * time.Second
	// internalCheckTimeout bounds the tiny tokenless recipient-existence check
	// (CheckMailbox) independently of the — possibly much larger, size-derived —
	// message deadline, so a RCPT-time lookup fails fast when the API is
	// unresponsive instead of holding the SMTP session open for the whole
	// large-message delivery budget.
	internalCheckTimeout = 30 * time.Second
)

// Client is the REST API client used by all gateway protocol handlers.
//
// It holds TWO destinations, because the API can expose two listeners:
//
//   - the public client (baseURL/httpClient) serves every token/credential
//     route — Login and all Bearer-token user routes (folders, messages, quota,
//     …). These are authenticated by JWT or user credentials, so they do NOT
//     use mTLS and are served on the public listener.
//   - the internal client (internalBaseURL/internalHTTPClient) serves ONLY the
//     two tokenless machine-to-machine routes, CheckMailbox and DeliverMessage.
//     When internal mTLS is configured this points at the API's dedicated mTLS
//     listener and presents the gateway client certificate.
//
// Routing is therefore PER-ENDPOINT, not per-client: switching the whole base
// URL to the internal listener would 404 every user route (breaking IMAP/POP3
// retrieval and SMTP submission). When internal mTLS is NOT configured, the
// internal client is the same as the public client, so behavior is byte-for-
// byte unchanged.
type Client struct {
	baseURL    string
	httpClient *http.Client

	internalBaseURL    string
	internalHTTPClient *http.Client

	// msgDeadline is the maximum time a single message-carrying call
	// (DeliverMessage upload, GetRawMessage download) may take. It is applied as
	// the Timeout on both HTTP clients so a large-but-admin-permitted message is
	// not stranded by a short fixed timeout (OSI-7). Derived from the configured
	// SMTP_MAX_MESSAGE_SIZE and a floor throughput via WithMessageDeadline, so it
	// is always a finite, bounded value (never zero/infinite); it defaults to
	// defaultRequestTimeout when the option is not supplied.
	msgDeadline time.Duration
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithInternalMTLS routes the two tokenless machine calls (CheckMailbox,
// DeliverMessage) to a separate internal base URL over a transport that
// presents the gateway client certificate (and verifies the API server cert
// against the internal CA in tlsCfg). Every other method keeps using the public
// base URL/transport unchanged. With no option the internal client equals the
// public client, so plain-HTTP (non-mTLS) deployments are unaffected.
func WithInternalMTLS(internalBaseURL string, tlsCfg *tls.Config) Option {
	return func(c *Client) {
		// Clone the default transport so connection-pooling/timeout defaults are
		// preserved; only the TLS config is overridden.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = tlsCfg
		c.internalBaseURL = internalBaseURL
		c.internalHTTPClient = &http.Client{
			// A placeholder; New applies the effective (size-aware) timeout to
			// both clients after all options run, so ordering with
			// WithMessageDeadline does not matter.
			Timeout:   defaultRequestTimeout,
			Transport: transport,
		}
	}
}

// WithMessageDeadline sets the maximum time a single message-carrying call
// (DeliverMessage, GetRawMessage) may take — the size-aware upper bound derived
// by the caller from the configured SMTP_MAX_MESSAGE_SIZE and a floor throughput
// (config.InternalDeliveryDeadline). It replaces the fixed default timeout so a
// large-but-permitted message is not stranded, while staying BOUNDED: a
// non-positive value is ignored, so the timeout can never be disabled (no
// slowloris-reintroducing infinite wait). Applies to both the public and the
// internal client.
func WithMessageDeadline(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.msgDeadline = d
		}
	}
}

// New creates a new API client pointing at the given (public) base URL. Options
// may add a separate internal mTLS destination for the two machine routes.
func New(baseURL string, opts ...Option) *Client {
	httpClient := &http.Client{Timeout: defaultRequestTimeout}
	c := &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
		// Default: the two machine routes share the public client/base URL, so
		// default-off behavior is exactly as before.
		internalBaseURL:    baseURL,
		internalHTTPClient: httpClient,
		msgDeadline:        defaultRequestTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	// Apply the effective message deadline to BOTH clients after all options run
	// (WithInternalMTLS may have replaced the internal client). The public client
	// carries GetRawMessage (a full stored message streamed back to IMAP/POP3);
	// the internal client carries DeliverMessage (a full inbound message uploaded
	// to the API). Both must permit a max-size body, so both get the same
	// size-aware, bounded timeout. When WithMessageDeadline was not supplied this
	// is defaultRequestTimeout, i.e. behavior is unchanged.
	if c.msgDeadline > 0 {
		c.httpClient.Timeout = c.msgDeadline
		c.internalHTTPClient.Timeout = c.msgDeadline
	}
	return c
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

// Login authenticates a mailbox user and returns an access token.
func (c *Client) Login(email, password string) (*LoginResponse, error) {
	body := map[string]string{"email": email, "password": password}
	var resp LoginResponse
	if err := c.post("/api/v1/auth/login", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// LoginAdmin authenticates an admin user and returns an access token.
func (c *Client) LoginAdmin(username, password string) (*LoginResponse, error) {
	body := map[string]string{"username": username, "password": password}
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

// CheckMailbox verifies a recipient address exists. This is a tokenless
// machine-to-machine call, so it goes to the internal (mTLS, when configured)
// destination rather than the public listener.
func (c *Client) CheckMailbox(address string) (*MailboxCheckResponse, error) {
	// A recipient-existence check carries no message body, so it is bounded by
	// the short internalCheckTimeout — NOT the (possibly large) size-aware
	// message deadline — so a RCPT-time lookup fails fast if the API is
	// unresponsive.
	ctx, cancel := context.WithTimeout(context.Background(), internalCheckTimeout)
	defer cancel()
	var resp MailboxCheckResponse
	if err := c.getInternal(ctx, "/api/mailboxes?address="+url.QueryEscape(address), &resp); err != nil {
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
	// RawMessage carries the pristine RFC 2822 wire bytes. It MUST be []byte, not
	// string: encoding/json base64-encodes a []byte field, so arbitrary octets
	// (8bit/binary content-transfer-encoding bodies, undeclared-charset high
	// bytes) round-trip verbatim. A JSON string field would silently replace
	// every invalid-UTF-8 byte with U+FFFD on marshal, corrupting the stored
	// message and breaking DKIM body hashes and BODY[] byte fidelity.
	RawMessage []byte `json:"raw_message,omitempty"`
	ClientIP   string `json:"client_ip,omitempty"`
	HeloName   string `json:"helo_name,omitempty"`

	// Inbound transport-security metrics, populated only by the inbound-MX
	// (port 25) SMTP path so the operator can always see how much mail arrives
	// encrypted vs plaintext. Pointer/empty so they are additive and omitted by
	// callers that never set them (IMAP APPEND, local webmail send, authenticated
	// submission): a nil ReceivedTLS means "not an inbound-MX delivery — not
	// applicable", which the API persists as NULL (distinct from plaintext).
	ReceivedTLS *bool  `json:"received_tls,omitempty"`
	TLSVersion  string `json:"tls_version,omitempty"`
	TLSCipher   string `json:"tls_cipher,omitempty"`
}

type DeliverResponse struct {
	Data struct {
		ID        uint   `json:"id"`
		MailboxID uint   `json:"mailbox_id"`
		Subject   string `json:"subject"`
	} `json:"data"`
}

// DeliverMessage delivers a message to a local mailbox. This is a tokenless
// machine-to-machine call, so it goes to the internal (mTLS, when configured)
// destination rather than the public listener.
func (c *Client) DeliverMessage(req *DeliverRequest) (*DeliverResponse, error) {
	// A delivery uploads a full inbound message body, so it is bounded by the
	// size-aware message deadline rather than a fixed short timeout — a 128 MiB
	// body cannot complete in the old 30 s. The bound is finite (never infinite),
	// so slowloris on this internal hop stays capped.
	ctx, cancel := context.WithTimeout(context.Background(), c.msgDeadline)
	defer cancel()
	var resp DeliverResponse
	if err := c.postInternal(ctx, "/api/v1/messages/deliver", req, &resp); err != nil {
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
	RawSize        int             `json:"raw_size"`
	HasAttachments bool            `json:"has_attachments"`
	IsRead         bool            `json:"is_read"`
	IsFlagged      bool            `json:"is_flagged"`
	IsStarred      bool            `json:"is_starred"`
	IsDraft        bool            `json:"is_draft"`
	ReceivedAt     time.Time       `json:"received_at"`
}

// WireSize returns the octet count a protocol gateway must report for this
// message (IMAP RFC822.SIZE, POP3 STAT/LIST): the exact size of the stored
// raw message when the server recorded one (raw_size > 0), else the legacy
// size_bytes approximation for messages that have no stored raw form and are
// served via a rebuilt fallback.
func (m MessageSummary) WireSize() int {
	if m.RawSize > 0 {
		return m.RawSize
	}
	return m.SizeBytes
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
// maxGatewayMessages caps how many messages the IMAP/POP3 gateways load per
// folder, bounding memory for pathologically large mailboxes.
const maxGatewayMessages = 5000

// ListMessages returns the FULL folder in oldest-first order. The IMAP/POP3
// gateways assign sequence numbers and UIDs from this slice, so it must be
// complete (not just the newest page) and monotonic. The API pages newest-first
// with a cursor; follow the cursor to the end, then reverse.
func (c *Client) ListMessages(token string, accountID uint, folder string) (*MessageListResponse, error) {
	var all []MessageSummary
	cursor := ""
	for {
		path := fmt.Sprintf("/api/v1/accounts/%d/folders/%s/messages?limit=100",
			accountID, url.PathEscape(folder))
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var resp MessageListResponse
		if err := c.getAuth(path, token, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		if resp.Pagination == nil || !resp.Pagination.HasMore || resp.Pagination.Cursor == "" {
			break
		}
		if len(all) >= maxGatewayMessages {
			break
		}
		cursor = resp.Pagination.Cursor
	}
	// Reverse newest-first → oldest-first so seq/UID are ascending.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return &MessageListResponse{Data: all}, nil
}

// GetMessage returns a full message by ID.
func (c *Client) GetMessage(token string, msgID uint) (*MessageDetailResponse, error) {
	var resp MessageDetailResponse
	if err := c.getAuth(fmt.Sprintf("/api/v1/messages/%d", msgID), token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetRawMessage returns the pristine stored RFC 2822 bytes for a message, so that
// IMAP/POP3 clients receive the original message verbatim (attachments, To/Cc,
// custom headers and any DKIM-Signature preserved). When the server has no stored
// raw form for the message (e.g. locally-composed items), it responds 404 and this
// returns an empty string with a nil error, letting callers fall back to a
// reconstructed message.
func (c *Client) GetRawMessage(token string, msgID uint) (string, error) {
	req, err := http.NewRequest("GET", c.baseURL+fmt.Sprintf("/api/v1/messages/%d/raw", msgID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET raw message %d: %w", msgID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if err := c.checkStatus(resp); err != nil {
		return "", err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
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

// get performs a tokenless GET against the public destination.
func (c *Client) get(path string, out interface{}) error {
	resp, err := c.httpClient.Get(c.baseURL + path)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, out)
}

// getInternal performs a tokenless GET against the internal destination
// (the mTLS listener when configured, else the public client — see Client). The
// context carries the per-call deadline; the client Timeout is the outer bound.
func (c *Client) getInternal(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.internalBaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.internalHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	return c.decodeResponse(resp, out)
}

// postInternal performs a tokenless POST against the internal destination
// (the mTLS listener when configured, else the public client — see Client). The
// context carries the per-call (size-aware) deadline for the message-body
// upload; the client Timeout is the outer bound.
func (c *Client) postInternal(ctx context.Context, path string, body interface{}, out interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.internalBaseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.internalHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
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
