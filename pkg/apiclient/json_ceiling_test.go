package apiclient

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// jsonLimit is the JSON ceiling of a client given rawCeiling.
const jsonLimit = jsonCeilingFactor * rawCeiling

// jsonAllocationBound is the most a refused streamed body may allocate: the decoder's grown
// buffer and the partly built string, a few times the ceiling, and a fifth of oversize. A
// declared refusal is held to the raw tests' small bound instead.
const jsonAllocationBound = 5 * jsonLimit

const detailFrame = `{"data":{"id":42,"body_text":""}}`

// detailJSON is a message-detail response of exactly n octets, its text body padded.
func detailJSON(n int) []byte {
	return []byte(`{"data":{"id":42,"body_text":"` + strings.Repeat("a", n-len(detailFrame)) + `"}}`)
}

// serveDetail answers message 42's detail with body, declaring its length or, with declare
// false, streaming it chunked with no length.
func serveDetail(t *testing.T, body []byte, declare bool) *Client {
	t.Helper()
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if declare {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
			return
		}
		for rest := body; len(rest) > 0; {
			n := min(len(rest), 32<<10)
			if _, err := w.Write(rest[:n]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			rest = rest[n:]
		}
	})
	return New(srv.URL, WithMaxRawMessageBytes(rawCeiling))
}

func TestJSONCeilingIsAMultipleOfTheRawCeiling(t *testing.T) {
	if got := jsonCeiling(rawCeiling); got != jsonLimit {
		t.Fatalf("jsonCeiling(%d) = %d, want %d", rawCeiling, got, jsonLimit)
	}
	if got := jsonCeiling(1<<62 + 1); got <= 0 {
		t.Fatalf("jsonCeiling of a huge ceiling overflowed: %d", got)
	}
}

// A detail exactly at the ceiling decodes, declared or streamed.
func TestMessageDetailAtTheJSONCeilingDecodes(t *testing.T) {
	for _, declare := range []bool{true, false} {
		d, err := serveDetail(t, detailJSON(jsonLimit), declare).GetMessage("tok", 42)
		if err != nil || d.Data.ID != 42 || len(d.Data.BodyText) != jsonLimit-len(detailFrame) {
			t.Fatalf("declared %v: %v", declare, err)
		}
	}
}

// One octet over, declared: refused from the headers, before the body is read.
func TestMessageDetailDeclaredOverTheJSONCeilingIsRefusedUnread(t *testing.T) {
	for _, n := range []int{jsonLimit + 1, oversize} {
		c := serveDetail(t, detailJSON(n), true)
		var err error
		got := allocatedBy(func() { _, err = c.GetMessage("tok", 42) })
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("%d octets: got %v, want ErrResponseTooLarge", n, err)
		}
		if got > allocationBound {
			t.Fatalf("%d octets: allocated %d, more than %d", n, got, allocationBound)
		}
	}
}

// One octet over with no declared length, and a body far over: refused having read a
// bounded amount.
func TestMessageDetailStreamedOverTheJSONCeilingIsRefused(t *testing.T) {
	for _, n := range []int{jsonLimit + 1, oversize} {
		c := serveDetail(t, detailJSON(n), false)
		var err error
		got := allocatedBy(func() { _, err = c.GetMessage("tok", 42) })
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("%d octets: got %v, want ErrResponseTooLarge", n, err)
		}
		if got > jsonAllocationBound {
			t.Fatalf("%d octets: allocated %d, more than %d", n, got, jsonAllocationBound)
		}
	}
}

// A small response is unchanged, and a malformed one stays a decode error.
func TestSmallJSONResponsesAreUnchanged(t *testing.T) {
	d, err := serveDetail(t, []byte(`{"data":{"id":42}}`+"\n"), false).GetMessage("tok", 42)
	if err != nil || d.Data.ID != 42 {
		t.Fatalf("small: %v, %+v", err, d)
	}
	_, err = serveDetail(t, []byte(`{"data":`), true).GetMessage("tok", 42)
	if err == nil || errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("malformed: %v, want a decode error", err)
	}
}
