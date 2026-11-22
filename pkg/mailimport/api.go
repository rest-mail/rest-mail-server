package mailimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

type Request func(context.Context, string, string, io.Reader) (*http.Response, error)

func Post(ctx context.Context, request Request, account uint32, session string, message Message) (Result, int, error) {
	var zero Result
	body, err := json.Marshal(message)
	if err != nil {
		return zero, 0, errors.New("could not encode the import")
	}
	target := fmt.Sprintf("/api/v1/accounts/%d/messages/import", account)
	if session != "" {
		target += "?import_session=" + url.QueryEscape(session)
	}
	response, err := request(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return zero, 0, errors.New("the import request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return zero, response.StatusCode, nil
	}
	var result struct {
		Data Result `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if decoder.Decode(&result) != nil {
		return zero, response.StatusCode, errors.New("the server returned an invalid import response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return zero, response.StatusCode, errors.New("the server returned trailing import response data")
	}
	return result.Data, response.StatusCode, nil
}

func OpenSession(ctx context.Context, request Request, account uint32, client string) (string, error) {
	body, _ := json.Marshal(map[string]string{"client_name": client})
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err := sessionRequest(ctx, request, http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import-sessions", account), body, http.StatusCreated, &result)
	if err != nil {
		return "", err
	}
	if len(result.Data.ID) != 32 || strings.Trim(result.Data.ID, "0123456789abcdef") != "" {
		return "", errors.New("the server returned an invalid import session id")
	}
	return result.Data.ID, nil
}

func CloseSession(ctx context.Context, request Request, account uint32, session string) error {
	var result struct {
		Data apitypes.ImportSessionSummary `json:"data"`
	}
	return sessionRequest(ctx, request, http.MethodDelete, fmt.Sprintf("/api/v1/accounts/%d/messages/import-sessions/%s", account, session), nil, http.StatusOK, &result)
}

func sessionRequest(ctx context.Context, request Request, method, path string, body []byte, want int, out any) error {
	response, err := request(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return errors.New("the import session request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		return fmt.Errorf("server refused the import session (HTTP %d)", response.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out) != nil {
		return errors.New("the server returned an invalid import session response")
	}
	return nil
}
