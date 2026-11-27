package apiclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ImportContent says where an import's message is read from, so that it travels to the API
// without being held in memory (#2107). Open answers a new reader at the start of the
// message, because a request can be sent more than once on a connection that has gone
// stale; each reader yields exactly Size octets.
type ImportContent struct {
	Open func() io.Reader
	Size int64
}

// ImportMessageFrom is ImportMessage for a message read from content: req.RawMessage is
// ignored. The API receives the same JSON fields, with the same values, as ImportMessage
// sends for them, with raw_message last, in a request whose Content-Length is known.
func (c *Client) ImportMessageFrom(token string, accountID uint, session string, req *ImportRequest, content ImportContent) (*ImportResponse, error) {
	if content.Open == nil || content.Size < 0 {
		return nil, errors.New("apiclient: an import needs its message")
	}
	path := fmt.Sprintf("/api/v1/accounts/%d/messages/import", accountID)
	if session != "" {
		path += "?import_session=" + url.QueryEscape(session)
	}
	head, err := importHead(req)
	if err != nil {
		return nil, err
	}
	encode := DeliverContent{RawMessage: content.Open}

	base, client := c.destination(token)
	httpReq, err := http.NewRequest(http.MethodPost, base+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	open := func() (io.ReadCloser, error) {
		reader, writer := io.Pipe()
		go func() { writer.CloseWithError(encode.encode(writer, head)) }()
		return reader, nil
	}
	httpReq.Body, _ = open()
	httpReq.GetBody = open
	httpReq.ContentLength = importBodyLength(head, content.Size)
	authorize(httpReq, token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.send(client, httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	var out ImportResponse
	if err := c.decodeResponse(resp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// importHead is the JSON object of req's fields other than raw_message, which
// ImportMessageBody writes even when it is empty.
func importHead(req *ImportRequest) ([]byte, error) {
	small := *req
	small.RawMessage = nil
	whole, err := json.Marshal(&small)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(whole, &fields); err != nil {
		return nil, err
	}
	delete(fields, "raw_message")
	return json.Marshal(fields)
}

// importBodyLength is the length of what DeliverContent.encode writes for head and a
// message of size octets: head without its closing brace, a comma when head has fields,
// the raw_message member with size octets in base64, and the closing brace.
func importBodyLength(head []byte, size int64) int64 {
	length := int64(len(head)) - 1 + int64(len(`"raw_message":""}`)) + (size+2)/3*4
	if len(head) > len("{}") {
		length++
	}
	return length
}
