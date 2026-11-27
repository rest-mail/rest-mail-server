package apiclient

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
)

// rawCeiling is the read ceiling these tests give the client; oversize is a source many
// times longer, so an allocation that followed the source would show. allocationBound is
// the most a refusal may allocate: a buffer grown to the ceiling plus the transport's own,
// with room for the race detector's bookkeeping, and an eighth of oversize.
const (
	rawCeiling      = 1 << 20
	oversize        = 64 << 20
	allocationBound = 8 * rawCeiling
)

// rawSourceOf is a source of n octets, the same each time.
func rawSourceOf(n int) []byte {
	return bytes.Repeat([]byte("Subject: x\r\n\r\n0123456789abcdef"), n/30+1)[:n]
}

// allocatedBy is how many octets fn allocated, all goroutines counted.
func allocatedBy(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// serveRaw answers message 42's raw route with source, declaring its length or, with
// declare false, streaming it chunked with no length.
func serveRaw(t *testing.T, source []byte, declare bool) *Client {
	t.Helper()
	srv, mux := newTestServer(t)
	mux.HandleFunc("/api/v1/messages/42/raw", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		if declare {
			w.Header().Set("Content-Length", strconv.Itoa(len(source)))
			_, _ = w.Write(source)
			return
		}
		for rest := source; len(rest) > 0; {
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

// The ceiling a client has with no option is what the gateways configure at the default
// SMTP_MAX_MESSAGE_SIZE: the largest delivery body the API accepts.
func TestRawMessageCeilingDefaultsToTheDeliveryBodyLimit(t *testing.T) {
	want := (&config.Config{SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize}).InternalDeliveryBodyLimit()
	if got := New("http://api").maxRawBytes; got != want {
		t.Fatalf("default raw ceiling %d, want %d", got, want)
	}
	if got := New("http://api", WithMaxRawMessageBytes(0)).maxRawBytes; got != want {
		t.Fatalf("a zero ceiling left %d, want the default %d", got, want)
	}
}

// A source of exactly the ceiling comes back byte for byte, declared or streamed.
func TestRawMessageAtTheCeilingIsServedWhole(t *testing.T) {
	source := rawSourceOf(rawCeiling)
	for _, declare := range []bool{true, false} {
		raw, err := serveRaw(t, source, declare).GetRawMessage("tok", 42)
		if err != nil {
			t.Fatalf("declared %v: %v", declare, err)
		}
		if raw != string(source) {
			t.Fatalf("declared %v: the source came back changed (%d octets, want %d)", declare, len(raw), len(source))
		}
	}
}

// One octet past the ceiling is refused, declared or streamed.
func TestRawMessageOneOctetOverIsRefused(t *testing.T) {
	source := rawSourceOf(rawCeiling + 1)
	for _, declare := range []bool{true, false} {
		raw, err := serveRaw(t, source, declare).GetRawMessage("tok", 42)
		if !errors.Is(err, ErrRawMessageTooLarge) {
			t.Fatalf("declared %v: got %d octets, %v; want ErrRawMessageTooLarge", declare, len(raw), err)
		}
	}
}

// A source many times the ceiling is refused having allocated little: a declared length is
// refused before anything is allocated for it, and a streamed one stops one octet past the
// ceiling.
func TestRawMessageOversizeAllocatesNoMoreThanTheCeiling(t *testing.T) {
	source := rawSourceOf(oversize)
	for _, declare := range []bool{true, false} {
		c := serveRaw(t, source, declare)
		var err error
		allocated := allocatedBy(func() { _, err = c.GetRawMessage("tok", 42) })
		if !errors.Is(err, ErrRawMessageTooLarge) {
			t.Fatalf("declared %v: %v, want ErrRawMessageTooLarge", declare, err)
		}
		t.Logf("declared %v: %d-octet source refused having allocated %d octets", declare, oversize, allocated)
		if allocated > allocationBound {
			t.Fatalf("declared %v: allocated %d octets refusing a %d-octet source, want at most %d", declare, allocated, oversize, allocationBound)
		}
	}
}

// lyingAPI answers every request on one connection with a header declaring declared octets
// and then sends sent octets, more than it declared.
func lyingAPI(t *testing.T, declared, sent []byte, withLength bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		header := "HTTP/1.1 200 OK\r\nContent-Type: message/rfc822\r\nConnection: close\r\n"
		if withLength {
			header += fmt.Sprintf("Content-Length: %d\r\n", len(declared))
		}
		_, _ = conn.Write([]byte(header + "\r\n"))
		_, _ = conn.Write(declared)
		_, _ = conn.Write(sent)
	}()
	return "http://" + ln.Addr().String()
}

// An API that declares a length and sends more is held to what it declared: the source is
// the declared octets, and nothing past them is read or allocated. One that declares no
// length and sends past the ceiling is refused.
func TestRawMessageLyingLengthIsHeldToTheCeiling(t *testing.T) {
	declared := rawSourceOf(rawCeiling)
	extra := rawSourceOf(oversize)

	c := New(lyingAPI(t, declared, extra, true), WithMaxRawMessageBytes(rawCeiling))
	var raw string
	var err error
	allocated := allocatedBy(func() { raw, err = c.GetRawMessage("tok", 42) })
	if err != nil {
		t.Fatal(err)
	}
	if raw != string(declared) {
		t.Fatalf("got %d octets, want the %d declared", len(raw), len(declared))
	}
	if allocated > allocationBound {
		t.Fatalf("allocated %d octets for a %d-octet declared length", allocated, rawCeiling)
	}

	c = New(lyingAPI(t, declared, extra, false), WithMaxRawMessageBytes(rawCeiling))
	allocated = allocatedBy(func() { raw, err = c.GetRawMessage("tok", 42) })
	if !errors.Is(err, ErrRawMessageTooLarge) {
		t.Fatalf("no declared length: got %d octets, %v; want ErrRawMessageTooLarge", len(raw), err)
	}
	if allocated > allocationBound {
		t.Fatalf("no declared length: allocated %d octets refusing %d", allocated, rawCeiling+oversize)
	}
}
