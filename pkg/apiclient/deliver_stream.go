package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync/atomic"

	"github.com/rest-mail/rest-mail-server/pkg/envelope"
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
	var wire envelope.Data[json.RawMessage]
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

// ExactDeliveryBody streams the delivery JSON for a verified source of a
// known size. It owns raw after successful construction. Cancelling ctx must
// also interrupt reads from raw.
type ExactDeliveryBody struct {
	*io.PipeReader
	Length int64
	read   atomic.Int64
	done   chan struct{}
}

func NewExactDeliveryBody(ctx context.Context, fields any, raw io.ReadCloser, rawSize int64) (*ExactDeliveryBody, error) {
	head, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if len(head) < 2 || head[0] != '{' {
		return nil, fmt.Errorf("delivery fields must be a JSON object")
	}
	extra := int64(len(head) + len(`"raw_message":""`))
	if len(head) > len("{}") {
		extra++
	}
	if rawSize < 0 || rawSize > (math.MaxInt64-extra)/4*3 {
		return nil, fmt.Errorf("delivery source size out of range: %d", rawSize)
	}
	pipe, writer := io.Pipe()
	body := &ExactDeliveryBody{PipeReader: pipe, Length: extra + (rawSize+2)/3*4, done: make(chan struct{})}
	stop := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	go func() {
		defer close(body.done)
		defer stop()
		defer raw.Close()
		source := &exactSourceReader{raw: raw, remaining: rawSize}
		content := DeliverContent{RawMessage: func() io.Reader { return source }}
		_ = writer.CloseWithError(content.encode(writer, head))
	}()
	return body, nil
}

func (b *ExactDeliveryBody) Read(p []byte) (int, error) {
	n, err := b.PipeReader.Read(p)
	b.read.Add(int64(n))
	return n, err
}

// Complete reports whether the transport consumed the entire verified JSON
// body, including its closing brace.
func (b *ExactDeliveryBody) Complete() bool { return b.read.Load() == b.Length }

// Wait waits for the source to be closed. Call after closing the HTTP body
// and cancelling its source context if the upload stopped early.
func (b *ExactDeliveryBody) Wait() { <-b.done }

type exactSourceReader struct {
	raw       io.Reader
	remaining int64
}

func (r *exactSourceReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.raw.Read(extra[:])
		if n != 0 {
			return 0, fmt.Errorf("delivery source exceeded its recorded length")
		}
		return 0, err
	}
	n, err := r.raw.Read(p[:min(int64(len(p)), r.remaining)])
	r.remaining -= int64(n)
	if err == io.EOF && r.remaining != 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
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
