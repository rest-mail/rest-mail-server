package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

// deliverChunk is how much of the request is buffered on its way to the API.
const deliverChunk = 32 << 10

// DeliverContent says where a delivery's source is read from, so that it travels from disk to the
// API without being held in memory. RawMessage answers a new reader at the start of the source,
// because a request can be sent more than once on a connection that has gone stale. Nil means
// there is no source.
type DeliverContent struct {
	RawMessage func() io.Reader
}

// DeliverMessageFrom is DeliverMessage for a message read from content: req.RawMessage is ignored.
// The API receives the same JSON fields, with the same values, as DeliverMessage sends for them,
// in a request of unknown length.
func (c *Client) DeliverMessageFrom(req *DeliverRequest, content DeliverContent) (*DeliverResponse, error) {
	const path = "/api/v1/messages/deliver"
	small := *req
	small.RawMessage = nil
	head, err := json.Marshal(&small)
	if err != nil {
		return nil, err
	}

	// Bounded as DeliverMessage is, by the size-aware message deadline.
	ctx, cancel := context.WithTimeout(context.Background(), c.msgDeadline)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.internalBaseURL+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	open := func() (io.ReadCloser, error) {
		reader, writer := io.Pipe()
		go func() { writer.CloseWithError(content.encode(writer, head)) }()
		return reader, nil
	}
	httpReq.Body, _ = open()
	httpReq.GetBody = open
	httpReq.ContentLength = -1
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.send(c.internalHTTPClient, httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	var wire endpoint.Data[json.RawMessage]
	if err := c.decodeResponse(resp, &wire); err != nil {
		return nil, err
	}
	return decodeDelivered(wire)
}

// encode writes the request body: head, the JSON object of the request's small fields, with the
// source added to it as it is read.
func (d DeliverContent) encode(w io.Writer, head []byte) error {
	out := bufio.NewWriterSize(w, deliverChunk)
	_, _ = out.Write(bytes.TrimSuffix(head, []byte("}")))
	if d.RawMessage != nil {
		if len(head) > len("{}") {
			_ = out.WriteByte(',')
		}
		_, _ = out.WriteString(`"raw_message":"`)
		if err := copyBase64(out, d.RawMessage()); err != nil {
			return err
		}
		_ = out.WriteByte('"')
	}
	_ = out.WriteByte('}')
	return out.Flush()
}

// copyBase64 is how a []byte field is written in JSON, which is what keeps the message's octets
// intact when they are not UTF-8 (#173).
func copyBase64(w io.Writer, src io.Reader) error {
	encoder := base64.NewEncoder(base64.StdEncoding, w)
	if _, err := io.Copy(encoder, src); err != nil {
		return err
	}
	return encoder.Close()
}
