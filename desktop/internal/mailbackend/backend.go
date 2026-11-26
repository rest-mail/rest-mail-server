package mailbackend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/rest-mail/rest-mail-server/desktop/internal/imapmail"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

type Manager struct {
	*session.Manager
	accounts *imapmail.Store
	mu       sync.Mutex
	enabled  bool
	onChange func(session.SessionStatus)
	onRemove func(uint) error
}

func New(rest *session.Manager, accounts *imapmail.Store) *Manager {
	return &Manager{Manager: rest, accounts: accounts, enabled: true}
}

func (manager *Manager) local() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.enabled && manager.accounts != nil
}

func (manager *Manager) Status() session.SessionStatus {
	status := manager.Manager.Status()
	if status.SignedIn || !manager.local() {
		return status
	}
	accounts := manager.accounts.ListAccounts()
	if len(accounts) == 0 {
		return status
	}
	account := accounts[0]
	return session.SessionStatus{SignedIn: true, Server: "imap+tls://" + account.Host, Email: account.Address, AccountID: account.ID}
}

func (manager *Manager) SetOnChange(callback func(session.SessionStatus)) {
	manager.mu.Lock()
	manager.onChange = callback
	manager.mu.Unlock()
	manager.Manager.SetOnChange(func(_ session.SessionStatus) { manager.notify() })
}

func (manager *Manager) notify() {
	manager.mu.Lock()
	callback := manager.onChange
	manager.mu.Unlock()
	if callback != nil {
		callback(manager.Status())
	}
}

func (manager *Manager) AddIMAPAccount(ctx context.Context, input imapmail.AccountInput) (imapmail.Account, error) {
	if manager.accounts == nil {
		return imapmail.Account{}, errors.New("IMAP keychain unavailable")
	}
	account, err := manager.accounts.AddIMAPAccount(ctx, input)
	if err == nil {
		manager.mu.Lock()
		manager.enabled = true
		manager.mu.Unlock()
		manager.notify()
	}
	return account, err
}

func (manager *Manager) RemoveIMAPAccount(id uint) error {
	if manager.accounts == nil {
		return errors.New("IMAP keychain unavailable")
	}
	err := manager.accounts.RemoveIMAPAccount(id)
	if err == nil && manager.onRemove != nil {
		err = manager.onRemove(id)
	}
	manager.notify()
	return err
}

func (manager *Manager) SetOnRemove(callback func(uint) error) { manager.onRemove = callback }

func (manager *Manager) ListAccounts() []imapmail.Account {
	accounts := []imapmail.Account{}
	if status := manager.Manager.Status(); status.SignedIn {
		accounts = append(accounts, imapmail.Account{Type: "restmail", ID: status.AccountID, Address: status.Email, Host: status.Server})
	}
	if manager.accounts != nil {
		accounts = append(accounts, manager.accounts.ListAccounts()...)
	}
	return accounts
}

func (manager *Manager) SignOut() error {
	manager.mu.Lock()
	manager.enabled = false
	manager.mu.Unlock()
	if manager.accounts != nil {
		manager.accounts.Close()
	}
	err := manager.Manager.SignOut()
	manager.notify()
	return err
}

func (manager *Manager) SensitiveValues() []string {
	values := manager.Manager.SensitiveValues()
	if manager.accounts != nil {
		values = append(values, manager.accounts.SensitiveValues()...)
	}
	return values
}

func (manager *Manager) Redact(data []byte) []byte {
	data = manager.Manager.Redact(data)
	if manager.accounts != nil {
		data = manager.accounts.Redact(data)
	}
	return data
}

func (manager *Manager) Close() {
	if manager.accounts != nil {
		manager.accounts.Close()
	}
}

func (manager *Manager) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return manager.DoRequest(ctx, request)
}

func (manager *Manager) DoRequest(ctx context.Context, request *http.Request) (*http.Response, error) {
	request = request.WithContext(ctx)
	if manager.local() {
		if response, handled, err := manager.accounts.Handle(request); handled || err != nil {
			if handled && request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/api/v1/accounts/") {
				if err == nil && response != nil && response.StatusCode >= 200 && response.StatusCode < 300 && manager.onRemove != nil {
					if id, parseError := strconv.ParseUint(strings.TrimPrefix(request.URL.Path, "/api/v1/accounts/"), 10, 53); parseError == nil {
						if removeError := manager.onRemove(uint(id)); removeError != nil {
							response.Body.Close()
							return nil, removeError
						}
					}
				}
				manager.notify()
			}
			return response, err
		}
		if strings.TrimSuffix(request.URL.Path, "/") == "/api/v1/accounts" && request.Method == http.MethodGet {
			accounts := []apitypes.AccountResponse{}
			if manager.Manager.Status().SignedIn {
				response, err := manager.Manager.DoRequest(ctx, request)
				if err != nil {
					return nil, err
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					return nil, errors.New("could not list REST Mail accounts")
				}
				var result struct {
					Data []apitypes.AccountResponse `json:"data"`
				}
				if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result) != nil {
					return nil, errors.New("invalid REST Mail account list")
				}
				for _, account := range result.Data {
					if account.ID >= imapmail.IDBase {
						return nil, errors.New("REST Mail account ID overlaps desktop-reserved IDs")
					}
				}
				accounts = result.Data
			}
			locals := manager.accounts.AccountsResponse()
			if len(accounts) > 0 {
				for index := range locals {
					locals[index].IsPrimary = false
				}
			}
			accounts = append(accounts, locals...)
			encoded, _ := json.Marshal(map[string]any{"data": accounts})
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(manager.Redact(encoded)))}, nil
		}
		if !manager.Manager.Status().SignedIn {
			return &http.Response{StatusCode: http.StatusNotImplemented, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"not_supported","message":"This feature requires a REST Mail account"}}`))}, nil
		}
	}
	return manager.Manager.DoRequest(ctx, request)
}
