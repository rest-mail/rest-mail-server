package apiclient

import (
	"bytes"
	"net/http"
	"runtime"
	"strconv"
	"testing"
)

// GetRawMessage allocates a source the API declares the length of once: it does not grow a
// buffer to fit it and then copy it again into a string, so a gateway holds one message's
// worth of memory for it, not several (#2107).
func TestGetRawMessageAllocatesTheSourceOnce(t *testing.T) {
	const size = 16 << 20
	source := bytes.Repeat([]byte("0123456789abcdef"), size/16)
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/42/raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		w.Header().Set("Content-Length", strconv.Itoa(len(source)))
		_, _ = w.Write(source)
	})
	c := New(srv.URL)
	if _, err := c.GetRawMessage("tok", 42); err != nil { // warm the connection
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	raw, err := c.GetRawMessage("tok", 42)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if raw != string(source) {
		t.Fatal("the source came back changed")
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d-octet source: %d octets allocated", size, allocated)
	if allocated > size+size/8 {
		t.Fatalf("allocated %d octets for a %d-octet source, want at most %d", allocated, size, size+size/8)
	}
}

// A declared length the body does not meet is an error, never a short source.
func TestGetRawMessageRefusesAShortBody(t *testing.T) {
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/42/raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("Subject: short\r\n\r\n"))
	})
	if raw, err := New(srv.URL).GetRawMessage("tok", 42); err == nil {
		t.Fatalf("a short body came back as %q", raw)
	}
}
