package apiclient

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
)

// CopyRawMessage writes what GetRawMessage returns, under the same ceiling, declared
// or streamed, and allocates nothing that grows with the source (#2107).
func TestCopyRawMessageIsGetRawMessageToAWriter(t *testing.T) {
	for _, declare := range []bool{true, false} {
		source := rawSourceOf(rawCeiling)
		c := serveRaw(t, source, declare)
		var b bytes.Buffer
		found, n, err := c.CopyRawMessage("tok", 42, &b)
		if err != nil || !found || n != int64(len(source)) || !bytes.Equal(b.Bytes(), source) {
			t.Fatalf("declared %v: found %v, %d octets, %v", declare, found, n, err)
		}
		allocated := allocatedBy(func() { _, _, err = c.CopyRawMessage("tok", 42, io.Discard) })
		t.Logf("declared %v: copying %d octets allocated %d", declare, len(source), allocated)
		if err != nil || allocated > rawCeiling/4 {
			t.Fatalf("declared %v: copying %d octets allocated %d, %v", declare, len(source), allocated, err)
		}

		over := serveRaw(t, rawSourceOf(rawCeiling+1), declare)
		var w bytes.Buffer
		_, n, err = over.CopyRawMessage("tok", 42, &w)
		if !errors.Is(err, ErrRawMessageTooLarge) {
			t.Fatalf("declared %v: one octet over: %v", declare, err)
		}
		// A declared length over the ceiling is refused before anything is read.
		if declare && (n != 0 || w.Len() != 0) {
			t.Fatalf("declared over the ceiling: %d octets written", w.Len())
		}
	}

	c := New(lyingAPI(t, rawSourceOf(rawCeiling), rawSourceOf(oversize), true), WithMaxRawMessageBytes(rawCeiling))
	var b bytes.Buffer
	if _, n, err := c.CopyRawMessage("tok", 42, &b); err != nil || n != rawCeiling {
		t.Fatalf("lying length: %d octets, %v", n, err)
	}
	c = New(lyingAPI(t, rawSourceOf(rawCeiling), rawSourceOf(oversize), false), WithMaxRawMessageBytes(rawCeiling))
	if _, _, err := c.CopyRawMessage("tok", 42, io.Discard); !errors.Is(err, ErrRawMessageTooLarge) {
		t.Fatalf("no declared length, past the ceiling: %v", err)
	}
}

// No stored source is not found, with nothing written; a refusal is an error.
func TestCopyRawMessageNotFoundAndRefused(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/1/raw", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/api/v1/messages/2/raw", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	})
	c := New(srv.URL)
	var b bytes.Buffer
	if found, n, err := c.CopyRawMessage("tok", 1, &b); found || n != 0 || err != nil || b.Len() != 0 {
		t.Fatalf("404: found %v, %d, %v", found, n, err)
	}
	if _, _, err := c.CopyRawMessage("tok", 2, &b); err == nil || b.Len() != 0 {
		t.Fatalf("403: %v, wrote %d", err, b.Len())
	}
}
