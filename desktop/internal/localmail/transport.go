package localmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/localstore"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

func responseJSON(value any, source string, offline bool) (*http.Response, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	header := http.Header{"Content-Type": []string{"application/json"}, "X-Restmail-Source": []string{source}, "X-Restmail-Offline": []string{strconv.FormatBool(offline)}}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(encoded))}, nil
}

func (service *Service) requestAccount(parts []string) *accountStore {
	if len(parts) < 2 || parts[0] != "accounts" {
		return nil
	}
	id, err := strconv.ParseUint(parts[1], 10, 53)
	if err != nil || id == 0 {
		return nil
	}
	account, _ := service.account(uint(id))
	return account
}

func (service *Service) findMessage(ctx context.Context, id uint) *accountStore {
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, account := range service.accounts {
		if _, err := account.store.Get(ctx, id, false); err == nil {
			return account
		}
	}
	return nil
}

func (service *Service) DoRequest(ctx context.Context, request *http.Request) (*http.Response, error) {
	identity := service.Backend.Status()
	if !identity.SignedIn {
		return nil, session.ErrSignedOut
	}
	response, err := service.doRequest(session.BindServer(ctx, identity.Server), request, identity)
	if !sameSession(identity, service.Backend.Status()) {
		if response != nil {
			response.Body.Close()
		}
		return nil, session.ErrSignedOut
	}
	return response, err
}

func (service *Service) doRequest(ctx context.Context, request *http.Request, identity session.SessionStatus) (*http.Response, error) {
	if err := service.Activate(); err != nil {
		return nil, err
	}
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "accounts" && parts[2] == "events" {
		return service.Backend.DoRequest(ctx, request)
	}
	account := service.requestAccount(parts)
	if request.Method == http.MethodGet && account != nil {
		if len(parts) == 3 && parts[2] == "search" {
			return service.search(ctx, request, account)
		}
		account.mu.Lock()
		offline := account.offline
		account.mu.Unlock()
		if len(parts) == 3 && parts[2] == "folders" {
			folders, err := account.store.Folders(ctx)
			if err == nil && len(folders) > 0 {
				account.wake()
				return responseJSON(map[string]any{"data": folders, "source": "local", "offline": offline}, "local", offline)
			}
		}
		if len(parts) == 5 && parts[2] == "folders" && parts[4] == "messages" {
			cursor := request.URL.Query().Get("cursor")
			if cursor == "" || strings.HasPrefix(cursor, "local-list:") {
				before := int64(0)
				if cursor != "" {
					var err error
					before, err = strconv.ParseInt(strings.TrimPrefix(cursor, "local-list:"), 10, 64)
					if err != nil {
						return nil, errors.New("invalid local cursor")
					}
				}
				messages, more, err := account.store.List(ctx, parts[3], before, 50)
				status, statusErr := account.store.Status(ctx)
				if err == nil && statusErr == nil && (len(messages) > 0 || status.Complete || cursor != "") {
					page := apiclient.MessagePage{Data: messages, Pagination: &apiclient.Pagination{HasMore: more}}
					if more {
						page.Pagination.Cursor = fmt.Sprintf("local-list:%d", messages[len(messages)-1].ID)
					}
					return responseJSON(page, "local", offline)
				}
			}
		}
	}
	if len(parts) >= 2 && parts[0] == "messages" {
		parsed, err := strconv.ParseUint(parts[1], 10, 53)
		if err == nil && parsed > 0 {
			id := uint(parsed)
			account = service.findMessage(ctx, id)
			if request.Method == http.MethodGet && account != nil {
				account.mu.Lock()
				offline := account.offline
				account.mu.Unlock()
				if len(parts) == 2 {
					message, err := account.store.Get(ctx, id, true)
					if err == nil {
						return responseJSON(map[string]any{"data": message, "source": "local", "offline": offline}, "local", offline)
					}
				}
				if len(parts) == 3 && parts[2] == "attachments" {
					attachments, err := account.store.Attachments(ctx, id)
					if err == nil && len(attachments) > 0 {
						return responseJSON(map[string]any{"data": attachments}, "local", offline)
					}
				}
			}
			if len(parts) == 2 && account != nil && (request.Method == http.MethodPatch || request.Method == http.MethodDelete) {
				var response *http.Response
				err = account.syncer.Write(ctx, func() error {
					var upstreamError error
					response, upstreamError = service.Backend.DoRequest(ctx, request)
					if upstreamError != nil {
						return upstreamError
					}
					if response.StatusCode >= 200 && response.StatusCode < 300 && sameSession(identity, service.Backend.Status()) {
						return account.store.Remove(ctx, id)
					}
					return nil
				})
				if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 && sameSession(identity, service.Backend.Status()) {
					_ = account.syncer.Apply(ctx, account.store, account.id, id)
				}
				account.wake()
				if err != nil && response != nil {
					response.Body.Close()
				}
				return response, err
			}
		}
	}
	response, err := service.Backend.DoRequest(ctx, request)
	if !sameSession(identity, service.Backend.Status()) {
		if response != nil {
			response.Body.Close()
		}
		return nil, session.ErrSignedOut
	}
	if err != nil {
		return service.fallback(ctx, request, parts, err)
	}
	if response.StatusCode >= 500 {
		fallback, fallbackError := service.fallback(ctx, request, parts, errors.New("mail server unavailable"))
		if fallbackError == nil {
			response.Body.Close()
			return fallback, nil
		}
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if request.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "accounts" {
			if id, parseError := strconv.ParseUint(parts[1], 10, 53); parseError == nil {
				if err = service.Forget(uint(id)); err != nil {
					response.Body.Close()
					return nil, err
				}
			}
		}
		if request.Method == http.MethodGet {
			return service.observe(ctx, parts, response, identity)
		}
	}
	return response, nil
}

func (service *Service) fallback(ctx context.Context, request *http.Request, parts []string, cause error) (*http.Response, error) {
	if request.Method != http.MethodGet {
		return nil, cause
	}
	if len(parts) == 1 && parts[0] == "accounts" {
		if primary, err := service.account(0); err == nil {
			if catalog, readError := primary.store.Catalog(ctx); readError == nil && len(catalog) > 0 {
				return responseJSON(map[string]any{"data": catalog, "offline": true}, "local", true)
			}
		}
		accounts := []map[string]any{}
		for _, account := range service.Backend.ListAccounts() {
			accounts = append(accounts, map[string]any{"id": account.ID, "address": account.Address, "display_name": account.Address, "is_primary": len(accounts) == 0, "type": account.Type})
		}
		return responseJSON(map[string]any{"data": accounts, "offline": true}, "local", true)
	}
	return nil, cause
}

func (service *Service) observe(ctx context.Context, parts []string, response *http.Response, identity session.SessionStatus) (*http.Response, error) {
	eligible := len(parts) == 1 && parts[0] == "accounts" || len(parts) == 2 && parts[0] == "messages" || len(parts) == 3 && parts[0] == "accounts" && parts[2] == "folders" || len(parts) == 5 && parts[0] == "accounts" && parts[2] == "folders" && parts[4] == "messages"
	if !eligible {
		return response, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<20)+1))
	response.Body.Close()
	if err != nil || len(body) > 64<<20 {
		if err == nil {
			err = errors.New("mail response too large")
		}
		return nil, err
	}
	body = service.Backend.Redact(body)
	response.Body = io.NopCloser(bytes.NewReader(body))
	service.mu.Lock()
	if !sameSession(service.identity, identity) || !sameSession(service.Backend.Status(), identity) {
		service.mu.Unlock()
		response.Body.Close()
		return nil, session.ErrSignedOut
	}
	accounts := make(map[uint]*accountStore)
	for id, account := range service.accounts {
		accounts[id] = account
	}
	service.mu.Unlock()
	var target *accountStore
	if len(parts) > 1 && parts[0] == "accounts" {
		id, _ := strconv.ParseUint(parts[1], 10, 53)
		target = accounts[uint(id)]
	}
	switch {
	case len(parts) == 1:
		var result struct {
			Data []apitypes.AccountResponse `json:"data"`
		}
		if json.Unmarshal(body, &result) == nil {
			service.mu.Lock()
			if !sameSession(service.identity, identity) || !sameSession(service.Backend.Status(), identity) {
				service.mu.Unlock()
				response.Body.Close()
				return nil, session.ErrSignedOut
			}
			for _, account := range result.Data {
				if err = service.openLocked(account.ID); err != nil {
					break
				}
			}
			if primary, exists := service.accounts[identity.AccountID]; exists && err == nil {
				err = primary.store.PutCatalog(ctx, result.Data)
			}
			service.mu.Unlock()
		}
	case len(parts) == 2:
		var result struct {
			Data apitypes.Message `json:"data"`
		}
		if json.Unmarshal(body, &result) == nil && result.Data.ID != 0 {
			account := accounts[result.Data.MailboxID]
			if account != nil {
				err = account.store.Put(ctx, result.Data, !result.Data.HasAttachments)
				account.wake()
			}
		}
	case len(parts) == 3:
		var result struct {
			Data []apitypes.FolderSummary `json:"data"`
		}
		if json.Unmarshal(body, &result) == nil {
			if target != nil {
				err = target.store.PutFolders(ctx, result.Data)
			}
		}
	case len(parts) == 5:
		var result apiclient.MessagePage
		if json.Unmarshal(body, &result) == nil {
			if target != nil {
				err = target.store.PutBatch(ctx, result.Data, false, 0)
			}
		}
	}
	if err != nil {
		response.Body.Close()
		return nil, err
	}
	return response, nil
}

func (service *Service) search(ctx context.Context, request *http.Request, account *accountStore) (*http.Response, error) {
	values := request.URL.Query()
	query, folder := values.Get("q"), values.Get("folder")
	offset := 0
	cursor := values.Get("cursor")
	if strings.HasPrefix(cursor, "local:") {
		var err error
		offset, err = strconv.Atoi(strings.TrimPrefix(cursor, "local:"))
		if err != nil {
			return nil, err
		}
	}
	result, err := account.store.Search(ctx, query, folder, 50, offset)
	if err != nil {
		return nil, err
	}
	account.mu.Lock()
	result.Offline = account.offline
	account.mu.Unlock()
	if values.Get("desktop_remote") == "1" && !result.Complete {
		remoteRequest := request.Clone(ctx)
		copiedURL := *request.URL
		remoteRequest.URL = &copiedURL
		values.Del("desktop_remote")
		if strings.HasPrefix(cursor, "local:") {
			values.Del("cursor")
		}
		remoteRequest.URL.RawQuery = values.Encode()
		remoteCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		remote, remoteError := service.Backend.DoRequest(remoteCtx, remoteRequest)
		if remoteError == nil {
			defer remote.Body.Close()
			var page apiclient.MessagePage
			if remote.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(remote.Body, 32<<20)).Decode(&page) == nil {
				seen := make(map[uint]bool)
				for _, hit := range result.Data {
					seen[hit.ID] = true
				}
				for _, message := range page.Data {
					if !seen[message.ID] {
						result.Data = append(result.Data, localstore.Hit{Message: message, Source: "server"})
						seen[message.ID] = true
					}
				}
				result.Source = "local+server"
				result.Offline = false
			} else {
				result.Offline = remote.StatusCode >= 500
			}
		} else {
			result.Offline = true
		}
	}
	return responseJSON(result, result.Source, result.Offline)
}
