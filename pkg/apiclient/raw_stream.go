package apiclient

import (
	"fmt"
	"io"
	"net/http"
)

// CopyRawMessage writes the pristine stored RFC 2822 bytes of a message to w as they
// arrive, so the caller need not hold them (#2107): what GetRawMessage returns, under
// the same read ceiling (WithMaxRawMessageBytes) and with the same refusals. found is
// false, with nothing written, when the server has no stored raw form for the message.
// On an error some octets may have been written; the caller discards them.
func (c *Client) CopyRawMessage(token string, msgID uint, w io.Writer) (found bool, n int64, err error) {
	base, client := c.destination(token)
	req, err := http.NewRequest("GET", base+fmt.Sprintf("/api/v1/messages/%d/raw", msgID), nil)
	if err != nil {
		return false, 0, err
	}
	authorize(req, token)
	resp, err := c.send(client, req)
	if err != nil {
		return false, 0, fmt.Errorf("GET raw message %d: %w", msgID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}
	if err := c.checkStatus(resp); err != nil {
		return false, 0, err
	}
	n, err = copyRawSource(w, resp, c.maxRawBytes)
	if err != nil {
		return true, n, fmt.Errorf("GET raw message %d: %w", msgID, err)
	}
	return true, n, nil
}

// copyRawSource is readRawSource writing to w: a declared length over limit is refused
// before anything is read, a body shorter than it declared is an error, and a body with
// no declared length is refused one octet past limit.
func copyRawSource(w io.Writer, resp *http.Response, limit int64) (int64, error) {
	if resp.ContentLength > limit {
		return 0, fmt.Errorf("%w: the API declared %d octets, the ceiling is %d", ErrRawMessageTooLarge, resp.ContentLength, limit)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return n, fmt.Errorf("read raw message: %w", err)
	}
	if n > limit {
		return n, fmt.Errorf("%w: the API sent more than %d octets", ErrRawMessageTooLarge, limit)
	}
	if resp.ContentLength > 0 && n != resp.ContentLength {
		return n, fmt.Errorf("read raw message: %w", io.ErrUnexpectedEOF)
	}
	return n, nil
}
