package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	keyring "github.com/zalando/go-keyring"
)

const activeAccount = "active-account"

type credential struct {
	Server       string `json:"server"`
	Email        string `json:"email"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

type pendingLogin struct {
	server   string
	email    string
	password string
	expires  time.Time
}

type Manager struct {
	mu     sync.Mutex
	client *http.Client
	// streams carries event streams. It has no whole-response timeout, because an
	// event stream stays open for the session; the caller's context ends it.
	streams   *http.Client
	current   credential
	access    string
	accountID uint
	setup     []string
	expires   time.Time
	pending   pendingLogin
	secrets   map[string]struct{}
	onChange  func(SessionStatus)
	lastState SessionStatus
}

func New() *Manager {
	return &Manager{
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		streams: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
			},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		secrets: make(map[string]struct{}),
	}
}

func ValidateServer(server string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("server must be an HTTPS origin")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && parsed.Hostname() == "localhost") {
		return "", errors.New("HTTPS is required except for http://localhost")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}

func (manager *Manager) SetOnChange(callback func(SessionStatus)) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.onChange = callback
}

func (manager *Manager) notify() {
	manager.mu.Lock()
	status := manager.statusLocked()
	changed := !sameStatus(status, manager.lastState)
	manager.lastState = status
	callback := manager.onChange
	manager.mu.Unlock()
	if changed && callback != nil {
		callback(status)
	}
}

func (manager *Manager) Status() SessionStatus {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.statusLocked()
}

func (manager *Manager) statusLocked() SessionStatus {
	if manager.access == "" {
		return SessionStatus{}
	}
	return SessionStatus{SignedIn: true, Server: manager.current.Server, Email: manager.current.Email, AccountID: manager.accountID, SetupRequired: slices.Clone(manager.setup)}
}

func sameStatus(left, right SessionStatus) bool {
	return left.SignedIn == right.SignedIn && left.Server == right.Server && left.Email == right.Email &&
		left.AccountID == right.AccountID && slices.Equal(left.SetupRequired, right.SetupRequired)
}

func (manager *Manager) SignIn(server, email, password string) (SignInResult, error) {
	defer manager.notify()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.pending = pendingLogin{}
	if manager.access != "" {
		return SignInResult{}, errors.New("sign out before changing accounts")
	}
	validated, err := ValidateServer(server)
	if err != nil {
		return SignInResult{}, err
	}
	if strings.TrimSpace(email) == "" || password == "" || strings.ContainsAny(email, "\r\n") {
		return SignInResult{}, errors.New("email and password are required")
	}
	login := pendingLogin{server: validated, email: strings.TrimSpace(email), password: password, expires: time.Now().Add(5 * time.Minute)}
	err = manager.loginLocked(login, "")
	var apiError *APIError
	if errors.As(err, &apiError) && apiError.Code == "totp_required" {
		manager.pending = login
		return SignInResult{NeedsTOTP: true}, nil
	}
	if err != nil {
		return SignInResult{}, err
	}
	return SignInResult{SetupRequired: slices.Clone(manager.setup)}, nil
}

func (manager *Manager) SubmitTOTP(code string) error {
	defer manager.notify()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.pending.password == "" || time.Now().After(manager.pending.expires) {
		manager.pending = pendingLogin{}
		return errors.New("sign in again before submitting a two-factor code")
	}
	if strings.TrimSpace(code) == "" {
		return errors.New("a two-factor code is required")
	}
	err := manager.loginLocked(manager.pending, code)
	if err == nil {
		manager.pending = pendingLogin{}
	}
	return err
}

func (manager *Manager) loginLocked(login pendingLogin, code string) error {
	response, err := manager.auth(context.Background(), login.server, "/api/v1/auth/login", apitypes.LoginRequest{
		Email: login.email, Password: login.password, TOTPCode: code, Client: "native",
	})
	if err != nil {
		return err
	}
	return manager.acceptLocked(credential{Server: login.server, Email: login.email}, response)
}

// accountKey names a credential's keychain item. It holds no control characters:
// on macOS go-keyring drives /usr/bin/security interactively, one command per line,
// so a newline in the account name broke every save with no keychain item written.
func accountKey(saved credential) string {
	return saved.Email + " at " + saved.Server
}

func (manager *Manager) acceptLocked(saved credential, response apitypes.LoginResponse) error {
	if response.AccessToken == "" || response.RefreshToken == "" || response.ExpiresIn <= 0 {
		return errors.New("server did not return a native session")
	}
	saved.RefreshToken = response.RefreshToken
	manager.secrets[response.AccessToken] = struct{}{}
	manager.secrets[response.RefreshToken] = struct{}{}
	encoded, err := json.Marshal(saved)
	if err != nil {
		return errors.New("could not encode credential")
	}
	if err := keyring.Set(KeychainService, accountKey(saved), string(encoded)); err != nil {
		return errors.New("could not save credential in the OS keychain")
	}
	metadata, _ := json.Marshal(credential{Server: saved.Server, Email: saved.Email})
	if err := keyring.Set(KeychainService, activeAccount, string(metadata)); err != nil {
		_ = keyring.Delete(KeychainService, accountKey(saved))
		return errors.New("could not save active account in the OS keychain")
	}
	manager.current, manager.access = saved, response.AccessToken
	manager.accountID = response.User.ID
	manager.setup = slices.Clone(response.SetupRequired)
	lifetime := time.Duration(response.ExpiresIn) * time.Second
	manager.expires = time.Now().Add(lifetime - min(30*time.Second, lifetime/10))
	return nil
}

func (manager *Manager) auth(ctx context.Context, server, path string, body any) (apitypes.LoginResponse, error) {
	var result struct {
		Data  apitypes.LoginResponse `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return result.Data, errors.New("could not encode authentication request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server+path, bytes.NewReader(encoded))
	if err != nil {
		return result.Data, errors.New("invalid authentication request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := manager.client.Do(request)
	if err != nil {
		return result.Data, errors.New("authentication server unavailable")
	}
	defer response.Body.Close()
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "authentication_failed"
		if result.Error.Code == "totp_required" {
			code = "totp_required"
		}
		return result.Data, &APIError{Status: response.StatusCode, Code: code, Message: http.StatusText(response.StatusCode)}
	}
	if decodeErr != nil {
		return result.Data, errors.New("invalid authentication response")
	}
	return result.Data, nil
}

func (manager *Manager) Restore(ctx context.Context) error {
	defer manager.notify()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.access != "" {
		return nil
	}
	metadata, err := keyring.Get(KeychainService, activeAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return errors.New("could not read active account from the OS keychain")
	}
	var saved credential
	if json.Unmarshal([]byte(metadata), &saved) != nil {
		return errors.New("invalid saved account")
	}
	validated, err := ValidateServer(saved.Server)
	if err != nil || validated != saved.Server || saved.Email == "" {
		return errors.New("invalid saved account")
	}
	stored, err := keyring.Get(KeychainService, accountKey(saved))
	if err != nil {
		return errors.New("could not read credential from the OS keychain")
	}
	var full credential
	if json.Unmarshal([]byte(stored), &full) != nil || full.Server != saved.Server || full.Email != saved.Email || full.RefreshToken == "" {
		return errors.New("invalid saved credential")
	}
	manager.current = full
	manager.secrets[full.RefreshToken] = struct{}{}
	return manager.refreshLocked(ctx)
}

func (manager *Manager) refreshLocked(ctx context.Context) error {
	if manager.current.RefreshToken == "" {
		return ErrSignedOut
	}
	response, err := manager.auth(ctx, manager.current.Server, "/api/v1/auth/refresh", map[string]string{"refresh_token": manager.current.RefreshToken})
	if err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) && (apiError.Status == http.StatusUnauthorized || apiError.Status == http.StatusForbidden) {
			_ = manager.clearLocked()
		}
		return err
	}
	return manager.acceptLocked(manager.current, response)
}

func (manager *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			manager.mu.Lock()
			if manager.current.RefreshToken != "" && time.Now().After(manager.expires) {
				_ = manager.refreshLocked(ctx)
			}
			manager.mu.Unlock()
			manager.notify()
		}
	}
}

func (manager *Manager) clearLocked() error {
	saved := manager.current
	manager.current, manager.access, manager.pending = credential{}, "", pendingLogin{}
	manager.accountID = 0
	manager.setup = nil
	var failures []error
	for _, account := range []string{accountKey(saved), activeAccount} {
		if err := keyring.Delete(KeychainService, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			failures = append(failures, errors.New("could not delete credential from the OS keychain"))
		}
	}
	return errors.Join(failures...)
}

func (manager *Manager) SignOut() error {
	defer manager.notify()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	saved := manager.current
	err := manager.clearLocked()
	if saved.RefreshToken != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = manager.auth(ctx, saved.Server, "/api/v1/auth/logout", map[string]string{"refresh_token": saved.RefreshToken})
	}
	return err
}

func (manager *Manager) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, errors.New("invalid API request")
	}
	request.Header.Set("Content-Type", "application/json")
	return manager.DoRequest(ctx, request)
}

func (manager *Manager) DoRequest(ctx context.Context, incoming *http.Request) (*http.Response, error) {
	defer manager.notify()
	manager.mu.Lock()
	if expected, ok := ctx.Value(serverBinding{}).(string); ok && expected != manager.current.Server {
		manager.mu.Unlock()
		return nil, errors.New("the import server changed; sign in to the original server and rerun")
	}
	if manager.access == "" {
		manager.mu.Unlock()
		return nil, ErrSignedOut
	}
	if time.Now().After(manager.expires) {
		if err := manager.refreshLocked(ctx); err != nil {
			manager.mu.Unlock()
			return nil, err
		}
	}
	path := incoming.URL.RequestURI()
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		manager.mu.Unlock()
		return nil, errors.New("API path must be relative to the server")
	}
	request, err := http.NewRequestWithContext(ctx, incoming.Method, manager.current.Server+path, incoming.Body)
	if err != nil {
		manager.mu.Unlock()
		return nil, errors.New("invalid API request")
	}
	request.Header.Set("Authorization", "Bearer "+manager.access)
	for _, header := range []string{"Content-Type", "Accept", "Idempotency-Key", "Range", "If-Range"} {
		for _, value := range incoming.Header.Values(header) {
			request.Header.Add(header, value)
		}
	}
	request.Header.Set("Accept-Encoding", "identity")
	manager.mu.Unlock()
	client := manager.client
	if IsEventStream(incoming.Header.Get("Accept")) {
		client = manager.streams
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("mail server unavailable")
	}
	return response, nil
}

// IsEventStream reports whether a media type names a server-sent event stream.
func IsEventStream(mediaType string) bool {
	return strings.Contains(strings.ToLower(mediaType), "text/event-stream")
}

func (manager *Manager) Redact(data []byte) []byte {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	for secret := range manager.secrets {
		data = bytes.ReplaceAll(data, []byte(secret), []byte("[redacted]"))
	}
	return data
}

func (manager *Manager) SensitiveValues() []string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	values := make([]string, 0, len(manager.secrets))
	for secret := range manager.secrets {
		values = append(values, secret)
	}
	return values
}
