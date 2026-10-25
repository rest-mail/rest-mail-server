package apiclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	apitypes "github.com/restmail/restmail/internal/api/types"
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

type LoginResponse = endpoint.Data[apitypes.LoginResponse]

type protocolSignInEnvelope = endpoint.Data[apitypes.ProtocolSignedIn]

// SignInResponse combines the protocol response with the local Authorization header.
// The credential stays out of JSON; it is transport state, not an API response field.
type SignInResponse struct {
	protocolSignInEnvelope
	AccessToken string `json:"-"`
}

// SignIn signs a mail client in over protocol ("imap", "pop3" or "smtp") with
// what it gave as its password: one of the account's passwords, or an app
// password (#344). It is the one way the gateways sign in. No session is made:
// the response's AccessToken holds the address and password as HTTP Basic
// credentials, which every call that follows sends, and the API checks them
// against the account's credentials each time, so a revoked app password or a
// changed password stops working at the next call. An account with
// two-factor authentication signs in only with an app password.
func (c *Client) SignIn(email, password, protocol string) (*SignInResponse, error) {
	jsonBody, err := json.Marshal(apitypes.ProtocolSignInRequest{Protocol: protocol})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.baseURL+"/api/v1/auth/protocol-sign-in", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(email, password)
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST /api/v1/auth/protocol-sign-in: %w", err)
	}
	defer httpResp.Body.Close()

	var resp SignInResponse
	if err := c.decodeResponse(httpResp, &resp); err != nil {
		return nil, err
	}
	resp.AccessToken = req.Header.Get("Authorization")
	return &resp, nil
}

// authorize sets the Authorization header of a call made with token: HTTP Basic
// credentials as SignIn returns them, or else a JWT access token, sent as
// Bearer.
func authorize(req *http.Request, token string) {
	if strings.HasPrefix(token, "Basic ") {
		req.Header.Set("Authorization", token)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
}

// Login signs an account in interactively, with its address and password and,
// when it has two-factor authentication, a TOTP code, and returns the session.
// The API delivers the access token as the httpOnly restmail_access cookie (it
// is not in the response body for a browser), so the token is read from the
// Set-Cookie header.
//
// Later calls send the token as an Authorization: Bearer header (there is no
// cookie jar), so those requests carry no cookie and are transparently exempt
// from the API's CSRF guard.
func (c *Client) Login(email, password, totpCode string) (*LoginResponse, error) {
	body := apitypes.LoginRequest{Email: email, Password: password, TOTPCode: totpCode}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpResp, err := c.httpClient.Post(c.baseURL+"/api/v1/auth/login", "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("POST /api/v1/auth/login: %w", err)
	}
	defer httpResp.Body.Close()

	var resp LoginResponse
	if err := c.decodeResponse(httpResp, &resp); err != nil {
		return nil, err
	}
	for _, ck := range httpResp.Cookies() {
		if ck.Name == "restmail_access" && ck.Value != "" {
			resp.Data.AccessToken = ck.Value
			break
		}
	}
	return &resp, nil
}

// ── Mailbox Check ─────────────────────────────────────────────────────

type MailboxCheckResponse = endpoint.Data[apitypes.AddressCheck]

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

type DeliverRequest = apitypes.DeliverMessageBody

type deliveredMessageEnvelope = endpoint.Data[apitypes.Message]

// DeliverResponse contains either a delivered message or a processed receipt.
// Processed is local result metadata, decoded from the registered alternative body.
type DeliverResponse struct {
	deliveredMessageEnvelope
	Processed *apitypes.DeliveryProcessed `json:"-"`
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
	var wire endpoint.Data[json.RawMessage]
	if err := c.postInternal(ctx, "/api/v1/messages/deliver", req, &wire); err != nil {
		return nil, err
	}
	var processed apitypes.DeliveryProcessed
	if err := json.Unmarshal(wire.Data, &processed); err != nil {
		return nil, fmt.Errorf("decode delivery response: %w", err)
	}
	var resp DeliverResponse
	if processed.Status != "" {
		if processed.Status != "processed" {
			return nil, fmt.Errorf("unexpected delivery status %q", processed.Status)
		}
		resp.Processed = &processed
	} else if err := json.Unmarshal(wire.Data, &resp.Data); err != nil {
		return nil, fmt.Errorf("decode delivered message: %w", err)
	}
	return &resp, nil
}

// ── Message Send ──────────────────────────────────────────────────────

// SendRequest represents a message to be sent via the webmail API.
type SendRequest = apitypes.SendMessageRequest

// SendMessage sends a message via the webmail send API.
func (c *Client) SendMessage(token string, req *SendRequest) error {
	return c.postAuth("/api/v1/messages/send", token, req, nil)
}

// ── Folders ───────────────────────────────────────────────────────────

type Folder = apitypes.FolderSummary

type FolderListResponse = endpoint.List[apitypes.FolderSummary]

// ListFolders returns all folders for an account.
func (c *Client) ListFolders(token string, accountID uint) (*FolderListResponse, error) {
	var resp FolderListResponse
	if err := c.getAuth(fmt.Sprintf("/api/v1/accounts/%d/folders", accountID), token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Messages ──────────────────────────────────────────────────────────

type MessageSummary = apitypes.Message

type MessageDetail = apitypes.Message

type MessageListResponse = endpoint.List[apitypes.Message]

type MessageDetailResponse = endpoint.Data[apitypes.Message]

// ListMessages returns messages in a folder.
// maxGatewayMessages caps how many messages the IMAP/POP3 gateways load per
// folder, bounding memory for pathologically large mailboxes. It is a var (not a
// const) solely so tests can lower it to exercise the truncation path without
// synthesizing thousands of messages.
var maxGatewayMessages = 5000

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
			// The folder has more messages than the gateway will load. We keep the
			// newest maxGatewayMessages (the API pages newest-first); the oldest
			// become unreachable and EXISTS/STAT under-report (RFC 3501 §6.4.1 /
			// RFC 1939 §5). That silent truncation is a known limitation, so at
			// least make it observable to an operator.
			slog.Warn("gateway: folder message list truncated at cap; oldest messages unreachable",
				"account_id", accountID, "folder", folder,
				"loaded", len(all), "cap", maxGatewayMessages)
			break
		}
		cursor = resp.Pagination.Cursor
	}
	// Message sequence numbers are "ordered by ascending unique identifier" (RFC 3501
	// §2.3.1.2), and the ID is the UID. The API pages by date, which a copy or a dated
	// APPEND keeps from its source while getting a higher ID, so sort rather than reverse
	// (#805).
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
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
	authorize(req, token)
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

// MessageUpdate is the request registered by the API for message flag and folder changes.
type MessageUpdate = apitypes.UpdateMessageBody

// UpdateMessage updates message flags.
func (c *Client) UpdateMessage(token string, msgID uint, updates MessageUpdate) error {
	return c.patchAuth(fmt.Sprintf("/api/v1/messages/%d", msgID), token, updates, nil)
}

// DeleteMessage permanently deletes a message. The gateway serves IMAP EXPUNGE /
// CLOSE and POP3 QUIT-DELE (and the delete half of a MOVE) — protocols with no
// trash stage — so the delete must be permanent: it reclaims quota and removes
// the row rather than soft-deleting it into an unreachable, still-counted state.
func (c *Client) DeleteMessage(token string, msgID uint) error {
	return c.deleteAuth(fmt.Sprintf("/api/v1/messages/%d?permanent=true", msgID), token)
}

// ── Quota ─────────────────────────────────────────────────────────────

type QuotaResponse = endpoint.Data[apitypes.Quota]

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

type DomainItem = apitypes.Domain

type DomainListResponse = endpoint.List[apitypes.Domain]

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
	body := apitypes.CreateDomainRequest{Name: name, ServerType: &serverType}
	return c.postAuth("/api/v1/admin/domains", token, body, nil)
}

// DeleteDomain deletes a domain by ID (admin only).
func (c *Client) DeleteDomain(token string, id uint) error {
	return c.deleteAuth(fmt.Sprintf("/api/v1/admin/domains/%d", id), token)
}

// ── Admin Mailboxes ──────────────────────────────────────────────────

type MailboxItem = apitypes.Mailbox

type MailboxListResponse = endpoint.List[apitypes.Mailbox]

// ListMailboxes returns all mailboxes (admin only).
func (c *Client) ListMailboxes(token string) (*MailboxListResponse, error) {
	var resp MailboxListResponse
	if err := c.getAuth("/api/v1/admin/mailboxes", token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateMailbox creates an account, owned by the session's: POST
// /api/v1/accounts, which needs mailboxes:write on its domain. An empty
// password makes one with no password, reached only through its owner.
func (c *Client) CreateMailbox(token string, address, displayName, password string) error {
	body := apitypes.CreateAccountRequest{Address: address, DisplayName: displayName, Password: password}
	return c.postAuth("/api/v1/accounts", token, body, nil)
}

// DeleteMailbox deletes a mailbox by ID (admin only).
func (c *Client) DeleteMailbox(token string, id uint) error {
	return c.deleteAuth(fmt.Sprintf("/api/v1/admin/mailboxes/%d", id), token)
}

// ResetPassword resets a mailbox password (admin only).
func (c *Client) ResetPassword(token string, id uint, newPassword string) error {
	body := apitypes.UpdateMailboxRequest{Password: &newPassword}
	return c.patchAuth(fmt.Sprintf("/api/v1/admin/mailboxes/%d", id), token, body, nil)
}

// ── Pipelines ────────────────────────────────────────────────────────

type PipelineItem = apitypes.Pipeline

type PipelineListResponse = endpoint.List[apitypes.Pipeline]

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
	body := apitypes.UpdatePipelineRequestBody{Active: &active}
	return c.patchAuth(fmt.Sprintf("/api/v1/admin/pipelines/%d", id), token, body, nil)
}

// ── Queue Stats ──────────────────────────────────────────────────────

type QueueStatsResponse = endpoint.Data[apitypes.QueueStatistics]

// QueueStats returns queue statistics (admin only).
func (c *Client) QueueStats(token string) (*QueueStatsResponse, error) {
	var resp QueueStatsResponse
	if err := c.getAuth("/api/v1/admin/queue/stats", token, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Bans ──────────────────────────────────────────────────────────────

type BanListResponse = endpoint.List[apitypes.Ban]

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
	authorize(req, token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
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
	authorize(req, token)
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
	authorize(req, token)
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
	authorize(req, token)
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

// IsAuthRejection reports whether err is a DEFINITIVE credential rejection from
// the auth API — an HTTP 401 or 403. Only these count as a brute-force signal:
// transient failures (5xx, network/DNS/timeout errors, connection refused) leave
// credentials unverified and must NOT accrue against the auth-failure ban, or a
// brief API outage would lock out legitimate clients. A nil error is not a
// rejection.
func IsAuthRejection(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden
	}
	return false
}
