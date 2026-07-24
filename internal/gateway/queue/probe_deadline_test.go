package queue

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestProbeRESTMAILHasIODeadline is the red-green guard for #169: the RESTMAIL
// capability probe must bound its socket I/O with a deadline so a remote that
// accepts the TCP connection and then never speaks cannot park a queue worker
// forever.
//
// The stub listener accepts the connection and then stays completely silent — no
// SMTP greeting is ever written, modelling a silent (or malicious) MX. Before the
// fix the probe blocked in the greeting read (smtp.NewClient) with no deadline,
// so this test hung until the outer bound below tripped. After the fix the
// socket deadline fires and probeRESTMAIL returns promptly with probed == false,
// so the caller falls through to plain SMTP.
func TestProbeRESTMAILHasIODeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open and stay silent — never write a greeting.
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	}()

	// Short per-attempt budget so the probe's deadline fires quickly and the test
	// stays fast; allowPrivateDest permits the loopback dial past the SSRF guard.
	w := &Worker{
		sendDeadline:     func(int64) time.Duration { return 200 * time.Millisecond },
		allowPrivateDest: true,
		hostname:         "test.local",
	}

	done := make(chan bool, 1)
	go func() {
		probed, _, _ := w.probeRESTMAIL(ln.Addr().String(), "silent.mx.test")
		done <- probed
	}()

	// The probe budget is 200ms, so a correctly-bounded probe returns well within
	// this. Exceeding it means the probe has no I/O deadline and is wedged on the
	// silent socket — the exact defect #169 describes.
	select {
	case probed := <-done:
		if probed {
			t.Fatal("a silent MX must not be reported as a completed probe")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not return: capability probe has no I/O deadline (#169) — a silent MX wedges the worker")
	}
}

// TestDeliverRESTMAILErrorBodyBounded verifies the second half of #169: a non-2xx
// RESTMAIL response body is read through a bounded reader, so an
// attacker-advertised endpoint cannot exhaust worker memory by returning a
// multi-gigabyte error body. The error surfaced to the caller must be capped,
// not the full stream.
func TestDeliverRESTMAILErrorBodyBounded(t *testing.T) {
	const oversize = (64 << 10) + (1 << 20) // 1 MiB past the 64 KiB cap
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(bytes.Repeat([]byte("A"), oversize))
	}))
	defer srv.Close()

	// tlsInsecure accepts the httptest self-signed cert; allowPrivateDest permits
	// the loopback endpoint — isolating the body cap as the property under test.
	w := &Worker{sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true}
	err := w.deliverRESTMAILHTTPS(srv.URL, testItem())
	if err == nil {
		t.Fatal("expected a non-2xx RESTMAIL response to surface as an error")
	}
	if len(err.Error()) > (64<<10)+512 {
		t.Fatalf("error body not bounded: got %d bytes, want <= ~64 KiB (#169)", len(err.Error()))
	}
}
