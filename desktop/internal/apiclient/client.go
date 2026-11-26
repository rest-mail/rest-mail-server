package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

type Transport interface {
	Do(context.Context, string, string, io.Reader) (*http.Response, error)
}

type Client struct {
	transport Transport
}

type Page struct {
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeSnoozed bool   `json:"include_snoozed,omitempty"`
}

type Pagination struct {
	Cursor  string `json:"cursor,omitempty"`
	HasMore bool   `json:"has_more"`
	Total   int64  `json:"total,omitempty"`
}

type MessagePage struct {
	Data        []apitypes.Message `json:"data"`
	Pagination  *Pagination        `json:"pagination,omitempty"`
	UIDValidity uint32             `json:"uid_validity,omitempty"`
}

type Draft struct {
	From       string   `json:"from"`
	To         []string `json:"to"`
	Cc         []string `json:"cc"`
	Subject    string   `json:"subject"`
	BodyText   string   `json:"body_text"`
	BodyHTML   string   `json:"body_html"`
	InReplyTo  string   `json:"in_reply_to"`
	References string   `json:"references"`
}

type Error struct {
	Status int
	Code   string
}

func (err *Error) Error() string {
	return fmt.Sprintf("mail API returned %d (%s)", err.Status, err.Code)
}

func New(transport Transport) *Client {
	return &Client{transport: transport}
}

func (client *Client) request(ctx context.Context, method, path string, body, result any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	response, err := client.transport.Do(ctx, method, "/api/v1"+path, reader)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &Error{Status: response.StatusCode, Code: http.StatusText(response.StatusCode)}
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(result)
}

func (client *Client) ListFolders(ctx context.Context, accountID uint) ([]apitypes.FolderSummary, error) {
	var result struct {
		Data []apitypes.FolderSummary `json:"data"`
	}
	err := client.request(ctx, http.MethodGet, fmt.Sprintf("/accounts/%d/folders", accountID), nil, &result)
	return result.Data, err
}

func (client *Client) ListMessages(ctx context.Context, accountID uint, folder string, page Page, query string) (MessagePage, error) {
	if query != "" {
		return client.Search(ctx, accountID, query, folder, page)
	}
	var result MessagePage
	path := fmt.Sprintf("/accounts/%d/folders/%s/messages?%s", accountID, url.PathEscape(folder), page.values().Encode())
	err := client.request(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (page Page) values() url.Values {
	values := url.Values{}
	if page.IncludeSnoozed {
		values.Set("include_snoozed", "true")
	}
	if page.Cursor != "" {
		values.Set("cursor", page.Cursor)
	}
	if page.Limit > 0 {
		values.Set("limit", strconv.Itoa(page.Limit))
	}
	return values
}

func (client *Client) GetMessage(ctx context.Context, messageID uint) (apitypes.Message, error) {
	var result struct {
		Data apitypes.Message `json:"data"`
	}
	err := client.request(ctx, http.MethodGet, fmt.Sprintf("/messages/%d", messageID), nil, &result)
	return result.Data, err
}

func (client *Client) Search(ctx context.Context, accountID uint, query, folder string, page Page) (MessagePage, error) {
	var result MessagePage
	values := page.values()
	values.Set("q", query)
	if folder != "" {
		values.Set("folder", folder)
	}
	err := client.request(ctx, http.MethodGet, fmt.Sprintf("/accounts/%d/search?%s", accountID, values.Encode()), nil, &result)
	return result, err
}

func (client *Client) SendMessage(ctx context.Context, message apitypes.SendMessageRequest) error {
	return client.request(ctx, http.MethodPost, "/messages/send", message, nil)
}

func (client *Client) SaveDraft(ctx context.Context, draftID uint, draft Draft) (apitypes.Message, error) {
	path, method := "/messages/draft", http.MethodPost
	if draftID != 0 {
		path, method = fmt.Sprintf("/messages/draft/%d", draftID), http.MethodPut
	}
	var result struct {
		Data apitypes.Message `json:"data"`
	}
	err := client.request(ctx, method, path, draft, &result)
	return result.Data, err
}

func (client *Client) SetFlags(ctx context.Context, messageIDs []uint, flags apitypes.UpdateMessageBody) error {
	for _, messageID := range messageIDs {
		if err := client.request(ctx, http.MethodPatch, fmt.Sprintf("/messages/%d", messageID), flags, nil); err != nil {
			return fmt.Errorf("message %d: %w", messageID, err)
		}
	}
	return nil
}

func (client *Client) MoveMessages(ctx context.Context, messageIDs []uint, folder string) error {
	return client.SetFlags(ctx, messageIDs, apitypes.UpdateMessageBody{Folder: &folder})
}

func (client *Client) DeleteMessages(ctx context.Context, messageIDs []uint, permanent bool) error {
	for _, messageID := range messageIDs {
		path := fmt.Sprintf("/messages/%d?permanent=%t", messageID, permanent)
		if err := client.request(ctx, http.MethodDelete, path, nil, nil); err != nil {
			return fmt.Errorf("message %d: %w", messageID, err)
		}
	}
	return nil
}
