package imapmail

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

func (store *Store) jsonResponse(status int, value any) *http.Response {
	encoded, err := json.Marshal(value)
	if err == nil {
		var decoded any
		if json.Unmarshal(encoded, &decoded) == nil {
			encoded, err = json.Marshal(store.scrub(decoded))
		}
	}
	if err != nil {
		status = http.StatusInternalServerError
		encoded = []byte(`{"error":{"code":"encoding_error","message":"Could not encode IMAP response"}}`)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(store.Redact(encoded)))}
}

func (store *Store) scrub(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, child := range typed {
			result[string(store.Redact([]byte(key)))] = store.scrub(child)
		}
		return result
	case []any:
		for index, child := range typed {
			typed[index] = store.scrub(child)
		}
	case string:
		return string(store.Redact([]byte(typed)))
	}
	return value
}

func (store *Store) failure(status int) *http.Response {
	return store.jsonResponse(status, map[string]any{"error": map[string]string{"code": "imap_error", "message": http.StatusText(status)}})
}

func decodeBody(request *http.Request, value any) error {
	if request.Body == nil {
		return errors.New("request body required")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxMessageSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid request body")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid request body")
	}
	return nil
}

func (store *Store) AccountsResponse() []apitypes.AccountResponse {
	accounts := []apitypes.AccountResponse{}
	for index, account := range store.ListAccounts() {
		accounts = append(accounts, apitypes.AccountResponse{ID: account.ID, Address: account.Address, DisplayName: account.Address, IsPrimary: index == 0})
	}
	return accounts
}

func (store *Store) Handle(request *http.Request) (*http.Response, bool, error) {
	path := strings.TrimPrefix(request.URL.EscapedPath(), "/api/v1/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return nil, false, nil
	}
	id, _ := strconv.ParseUint(parts[1], 10, 53)
	numeric := uint(id)
	ctx := request.Context()
	result := func(data any) (*http.Response, bool, error) {
		return store.jsonResponse(http.StatusOK, map[string]any{"data": data}), true, nil
	}
	fail := func(status int) (*http.Response, bool, error) { return store.failure(status), true, nil }
	page := apiclient.Page{Cursor: request.URL.Query().Get("cursor")}
	page.Limit, _ = strconv.Atoi(request.URL.Query().Get("limit"))
	if parts[0] == "accounts" && numeric >= IDBase {
		if _, ok := store.Account(numeric); !ok {
			return fail(http.StatusNotFound)
		}
		if len(parts) == 2 && request.Method == http.MethodDelete {
			if store.RemoveIMAPAccount(numeric) != nil {
				return fail(http.StatusBadGateway)
			}
			return result(map[string]bool{"ok": true})
		}
		if len(parts) < 3 {
			return fail(http.StatusNotFound)
		}
		switch parts[2] {
		case "events":
			if request.Method != http.MethodGet {
				return fail(http.StatusMethodNotAllowed)
			}
			return store.events(request, numeric), true, nil
		case "folders":
			if len(parts) == 3 && request.Method == http.MethodGet {
				folders, err := store.ListFolders(ctx, numeric)
				if err != nil {
					return fail(http.StatusBadGateway)
				}
				return result(folders)
			}
			folder := ""
			if len(parts) >= 4 {
				var err error
				folder, err = url.PathUnescape(parts[3])
				if err != nil {
					return fail(http.StatusBadRequest)
				}
			}
			if len(parts) == 5 && parts[4] == "messages" && request.Method == http.MethodGet {
				messages, err := store.ListMessages(ctx, numeric, folder, page, "")
				if err != nil {
					return fail(http.StatusBadGateway)
				}
				return store.jsonResponse(http.StatusOK, messages), true, nil
			}
			if request.Method == http.MethodPost || request.Method == http.MethodPatch || request.Method == http.MethodDelete {
				var body struct {
					Name string `json:"name"`
				}
				if request.Method != http.MethodDelete && decodeBody(request, &body) != nil {
					return fail(http.StatusBadRequest)
				}
				if len(parts) == 3 && request.Method == http.MethodPost {
					folder = body.Name
				}
				if err := store.Folder(ctx, numeric, request.Method, folder, body.Name); err != nil {
					return fail(http.StatusBadGateway)
				}
				return result(apitypes.FolderSummary{Folder: body.Name})
			}
		case "search":
			if request.Method != http.MethodGet {
				return fail(http.StatusMethodNotAllowed)
			}
			messages, err := store.Search(ctx, numeric, request.URL.Query().Get("q"), request.URL.Query().Get("folder"), page)
			if err != nil {
				return fail(http.StatusBadGateway)
			}
			return store.jsonResponse(http.StatusOK, messages), true, nil
		case "contacts":
			if request.Method == http.MethodGet {
				return result([]any{})
			}
		}
		return fail(http.StatusNotImplemented)
	}
	if parts[0] == "messages" && parts[1] == "send" && request.Method == http.MethodPost {
		if request.Body == nil {
			return fail(http.StatusBadRequest)
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxMessageSize+1))
		if err != nil || len(body) > maxMessageSize {
			return fail(http.StatusRequestEntityTooLarge)
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var message apitypes.SendMessageRequest
		if json.Unmarshal(body, &message) != nil {
			return nil, false, nil
		}
		for _, account := range store.ListAccounts() {
			if strings.EqualFold(account.Address, message.From) {
				if decodeBody(request, &message) != nil {
					return fail(http.StatusBadRequest)
				}
				if store.SendMessage(ctx, account, message) != nil {
					return fail(http.StatusBadGateway)
				}
				return result(map[string]bool{"sent": true})
			}
		}
		return nil, false, nil
	}
	if parts[0] == "messages" && parts[1] == "draft" && request.Method == http.MethodPost {
		if request.Body == nil {
			return fail(http.StatusBadRequest)
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxMessageSize+1))
		if err != nil || len(body) > maxMessageSize {
			return fail(http.StatusRequestEntityTooLarge)
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var draft struct {
			From string `json:"from"`
		}
		if json.Unmarshal(body, &draft) == nil {
			for _, account := range store.ListAccounts() {
				if strings.EqualFold(account.Address, draft.From) {
					return fail(http.StatusNotImplemented)
				}
			}
		}
		return nil, false, nil
	}
	if parts[0] == "messages" && numeric >= IDBase {
		if _, _, ok := store.resolve(numeric); !ok {
			return fail(http.StatusNotFound)
		}
		if len(parts) == 2 {
			switch request.Method {
			case http.MethodGet:
				message, _, _, err := store.GetMessage(ctx, numeric)
				if err != nil {
					return fail(http.StatusBadGateway)
				}
				return result(message)
			case http.MethodPatch:
				var update apitypes.UpdateMessageBody
				if decodeBody(request, &update) != nil {
					return fail(http.StatusBadRequest)
				}
				if store.UpdateMessage(ctx, numeric, update) != nil {
					return fail(http.StatusBadGateway)
				}
				return result(map[string]bool{"ok": true})
			case http.MethodDelete:
				if store.DeleteMessage(ctx, numeric, request.URL.Query().Get("permanent") == "true") != nil {
					return fail(http.StatusBadGateway)
				}
				return result(map[string]bool{"ok": true})
			}
		}
		if len(parts) == 3 && request.Method == http.MethodGet && (parts[2] == "attachments" || parts[2] == "raw") {
			_, attachments, raw, err := store.GetMessage(ctx, numeric)
			if err != nil {
				return fail(http.StatusBadGateway)
			}
			if parts[2] == "attachments" {
				return result(attachments)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"message/rfc822"}}, Body: io.NopCloser(bytes.NewReader(store.Redact(raw)))}, true, nil
		}
		return fail(http.StatusNotImplemented)
	}
	if parts[0] == "attachments" && numeric >= IDBase {
		if request.Method != http.MethodGet {
			return fail(http.StatusMethodNotAllowed)
		}
		store.mu.Lock()
		ref, ok := store.attachments[numeric]
		store.mu.Unlock()
		if !ok {
			return fail(http.StatusNotFound)
		}
		_, attachments, raw, err := store.GetMessage(ctx, ref.Message)
		if err != nil {
			return fail(http.StatusBadGateway)
		}
		var attachment Attachment
		for _, candidate := range attachments {
			if candidate.ID == numeric {
				attachment = candidate
			}
		}
		if attachment.ID == 0 {
			return fail(http.StatusNotFound)
		}
		data, err := attachmentData(raw, ref.Index)
		if err != nil {
			return fail(http.StatusBadGateway)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{attachment.ContentType}, "Content-Disposition": []string{mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename})}}, Body: io.NopCloser(bytes.NewReader(store.Redact(data)))}, true, nil
	}
	return nil, false, nil
}
