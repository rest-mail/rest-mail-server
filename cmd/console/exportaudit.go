package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// exportSession is the server's record of one export (#2116 AC03b): opened
// before anything is written, which the server audits as the export's start
// and refuses unless the key holds mail:export on the account, and closed when
// the export ends, which it audits as completed or aborted with the rows and
// bytes written.
type exportSession struct {
	ctx     context.Context
	client  *http.Client
	base    *url.URL
	token   string
	account uint64
	id      string
}

// exportCloseTimeout bounds the request that records an export's end. It is
// made even after Ctrl-C, so an interrupted export is recorded as aborted.
const exportCloseTimeout = 30 * time.Second

// openExportSession asks the server to record the start of an export of data
// written as format, and answers the session to close it with.
func openExportSession(ctx context.Context, client *http.Client, base *url.URL, token string, account uint64, data, format string) (*exportSession, error) {
	s := &exportSession{ctx: ctx, client: client, base: base, token: token, account: account}
	var opened struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	status, err := s.post(ctx, "/api/v1/accounts/"+strconv.FormatUint(account, 10)+"/exports",
		map[string]string{"data": data, "format": format}, &opened)
	switch {
	case err != nil:
		return nil, fmt.Errorf("could not open the audited export: %w", err)
	case status == http.StatusForbidden || status == http.StatusNotFound:
		return nil, fmt.Errorf("the server refused to open the export (HTTP %d); check the account and that the key holds mail:export on it", status)
	case status != http.StatusCreated || opened.Data.ID == "":
		return nil, fmt.Errorf("the server did not open the audited export (HTTP %d)", status)
	}
	s.id = opened.Data.ID
	return s, nil
}

// close records how the export ended. Its request is made even when the
// export's own context is done.
func (s *exportSession) close(completed bool, rows, written int64, skipped []string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), exportCloseTimeout)
	defer cancel()
	result := "aborted"
	if completed {
		result = "completed"
	}
	body := map[string]any{"result": result, "rows": rows, "bytes": written}
	if len(skipped) > 0 {
		body["skipped_folders"] = skipped
	}
	status, err := s.post(ctx, "/api/v1/accounts/"+strconv.FormatUint(s.account, 10)+"/exports/"+s.id+"/close", body, nil)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("HTTP %d", status)
	}
	if err != nil {
		return fmt.Errorf("the server did not record the end of the export: %w", err)
	}
	return nil
}

// end records the export's end and answers err, or the failure to record it.
func (s *exportSession) end(err error, rows, written int64, skipped []string) error {
	closeErr := s.close(err == nil, rows, written, skipped)
	if err == nil {
		return closeErr
	}
	if closeErr != nil {
		return fmt.Errorf("%w; %w", err, closeErr)
	}
	return err
}

func (s *exportSession) post(ctx context.Context, path string, body, into any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base.String()+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, errors.New("could not prepare the request")
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, errors.New("the request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if into != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into); err != nil {
			return resp.StatusCode, errors.New("the server returned an invalid response")
		}
	}
	return resp.StatusCode, nil
}
