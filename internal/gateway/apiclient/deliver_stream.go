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
	"unicode/utf8"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

// jsonTextChunk is how much of a text field is encoded to JSON at a time.
const jsonTextChunk = 32 << 10

// DeliverContent says where a delivery's large fields are read from, so that they travel from
// disk to the API without being held in memory. Each function answers a new reader at the start
// of the field, because a request can be sent more than once on a connection that has gone stale.
// A nil function means the field is absent.
type DeliverContent struct {
	RawMessage func() io.Reader
	BodyText   func() io.Reader
	BodyHTML   func() io.Reader
}

// DeliverMessageFrom is DeliverMessage for a message read from content: the fields req names
// there are ignored. The API receives the same JSON fields, with the same values, as
// DeliverMessage sends for them, in a request of unknown length.
func (c *Client) DeliverMessageFrom(req *DeliverRequest, content DeliverContent) (*DeliverResponse, error) {
	const path = "/api/v1/messages/deliver"
	small := *req
	small.BodyText, small.BodyHTML, small.RawMessage = "", "", nil
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
// large fields added to it as they are read.
func (d DeliverContent) encode(w io.Writer, head []byte) error {
	out := bufio.NewWriterSize(w, jsonTextChunk)
	_, _ = out.Write(bytes.TrimSuffix(head, []byte("}")))
	separator := ""
	if len(head) > len("{}") {
		separator = ","
	}
	field := func(name string, open func() io.Reader, copyValue func(io.Writer, io.Reader) error) error {
		if open == nil {
			return nil
		}
		_, _ = out.WriteString(separator + `"` + name + `":"`)
		separator = ","
		if err := copyValue(out, open()); err != nil {
			return err
		}
		_ = out.WriteByte('"')
		return nil
	}
	if err := field("body_text", d.BodyText, copyJSONText); err != nil {
		return err
	}
	if err := field("body_html", d.BodyHTML, copyJSONText); err != nil {
		return err
	}
	if err := field("raw_message", d.RawMessage, copyBase64); err != nil {
		return err
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

// copyJSONText writes src as the inside of a JSON string, as json.Marshal writes that text whole.
// A chunk is cut only between characters, so an invalid byte is replaced as it would be anyway.
func copyJSONText(w io.Writer, src io.Reader) error {
	buffer := make([]byte, jsonTextChunk)
	pending := 0
	for {
		n, readErr := src.Read(buffer[pending:])
		have := buffer[:pending+n]
		ready := have
		if readErr == nil {
			ready = have[:len(have)-incompleteRune(have)]
		}
		if len(ready) > 0 {
			encoded, err := json.Marshal(string(ready))
			if err != nil {
				return err
			}
			if _, err := w.Write(encoded[1 : len(encoded)-1]); err != nil {
				return err
			}
		}
		pending = copy(buffer, have[len(ready):])
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// incompleteRune says how many bytes at the end of text are the start of a character that the
// next read may finish.
func incompleteRune(text []byte) int {
	for back := 1; back < utf8.UTFMax && back <= len(text); back++ {
		start := len(text) - back
		if utf8.RuneStart(text[start]) {
			if utf8.FullRune(text[start:]) {
				return 0
			}
			return back
		}
	}
	return 0
}
