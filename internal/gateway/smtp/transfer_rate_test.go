package smtp

import (
	"bufio"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

// The transcript tests below run against the production server construction
// (newSMTPServer + the same conn wrapper the accept path installs), with the
// policy scaled to tens/hundreds of milliseconds so enforcement outcomes are
// decided by orders-of-magnitude margins, not scheduling luck.

// TestSMTP_TransferRate_TrickleDropped is the core anti-slowloris case: a
// client that keeps a DATA transfer trickling far below the average-rate floor
// after the grace period must be dropped with nothing delivered. The stall
// timeout is set huge so only the rate floor can be what kills it — tricklers
// survive stall timeouts by sending a byte occasionally.
func TestSMTP_TransferRate_TrickleDropped(t *testing.T) {
	back := newMockBackend()
	back.local["alice@local.test"] = true
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false, func(s *Server) {
		// Floor 1 MiB/s: a ~400 B/s trickle is below it by three orders of
		// magnitude, so any read completing after the 100 ms grace violates.
		s.SetTransferRatePolicy(1<<20, 100*time.Millisecond, 10*time.Second)
	})
	h.ehlo()

	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.cmd("DATA"); !strings.HasPrefix(r, "354") {
		t.Fatalf("DATA = %q, want 354...", r)
	}

	// Trickle ~10 bytes every 25 ms, never sending the terminating dot. The
	// server must cut the connection shortly after the grace period; the
	// client observes it as a failed write (or a dead read — never a reply).
	dropped := false
	for i := 0; i < 200; i++ { // bounded: ~5 s worst case, typically ~150 ms
		_ = h.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := h.conn.Write([]byte("trickle..\r\n")); err != nil {
			dropped = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !dropped {
		t.Fatal("trickling client was not dropped by the rate floor")
	}
	if got := back.deliveredTo(); len(got) != 0 {
		t.Errorf("nothing must be delivered from a dropped trickle, got %v", got)
	}
}

// TestSMTP_TransferRate_StallDropped: a client that goes fully silent
// mid-DATA beyond the stall timeout is dropped — with the rate floor
// explicitly disabled (0), proving the stall timeout still applies on its own.
func TestSMTP_TransferRate_StallDropped(t *testing.T) {
	back := newMockBackend()
	back.local["alice@local.test"] = true
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false, func(s *Server) {
		s.SetTransferRatePolicy(0, 50*time.Millisecond, 250*time.Millisecond)
	})
	h.ehlo()

	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.cmd("DATA"); !strings.HasPrefix(r, "354") {
		t.Fatalf("DATA = %q, want 354...", r)
	}

	// A few bytes, then dead silence — no dot, no more data.
	h.send("Subject: stalled")

	// The 250 ms stall timeout must kill the connection long before this 5 s
	// read deadline; a reply would mean the server accepted a stalled message.
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, err := h.cr.ReadString('\n'); err == nil {
		t.Fatalf("stalled transfer got a reply %q, want dropped connection", strings.TrimSpace(line))
	}
	if got := back.deliveredTo(); len(got) != 0 {
		t.Errorf("nothing must be delivered from a stalled transfer, got %v", got)
	}
}

// TestSMTP_TransferRate_NormalDelivery: with enforcement enabled (aggressive
// floor, sane grace), a normal-speed transfer is delivered exactly as before.
func TestSMTP_TransferRate_NormalDelivery(t *testing.T) {
	back := newMockBackend()
	back.local["alice@local.test"] = true
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false, func(s *Server) {
		s.SetTransferRatePolicy(1<<20, 5*time.Second, 5*time.Second)
	})
	h.ehlo()

	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Errorf("normal-speed DATA under enforcement = %q, want 250", final)
	}
	if got := back.deliveredTo(); !reflect.DeepEqual(got, []string{"alice@local.test"}) {
		t.Errorf("delivered = %v, want [alice@local.test]", got)
	}
}

// TestSMTP_TransferRate_CommandIdleNotEnforced guards the critical correctness
// point: between commands clients legitimately idle, and the rate machinery
// must not apply there (go-smtp's per-command ReadTimeout bounds command
// silence). With a policy that would kill any tracked connection idling past
// the 100 ms grace, a client pausing 400 ms between commands must live on.
func TestSMTP_TransferRate_CommandIdleNotEnforced(t *testing.T) {
	back := newMockBackend()
	back.local["alice@local.test"] = true
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false, func(s *Server) {
		s.SetTransferRatePolicy(1<<20, 100*time.Millisecond, 10*time.Second)
	})
	h.ehlo()

	time.Sleep(400 * time.Millisecond) // command-phase idle, well past grace

	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM after idle = %q, want 250 (rate machinery must not track command phase)", r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT after idle = %q", r)
	}
	if r := h.cmd("QUIT"); replyCode(r) != "221" {
		t.Errorf("QUIT = %q, want 221", r)
	}
}

// TestSMTP_TransferRate_FailOpenUnexpectedWrapping: if the session cannot dig
// the tracker out of the connection (unexpected wrapping — here a bare pipe
// conn with no wrapper at all), enforcement is skipped and mail still flows.
// Never break mail because introspection failed.
func TestSMTP_TransferRate_FailOpenUnexpectedWrapping(t *testing.T) {
	back := newMockBackend()
	back.local["alice@local.test"] = true
	store := newMockStore()

	client, server := net.Pipe()
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 1000})
	// TLS as production has it, but the listener deliberately NOT wrapped: the point
	// is what happens when the rate tracker is missing, not what happens without TLS.
	s := NewServer("smtp.test", back, harnessTLSConfig(t), store, limiter)
	srv := s.newSMTPServer(false)

	listener := newOneConnListener(server) // deliberately NOT wrapped
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(listener)
	}()

	h := &smtpHarness{t: t, back: back, store: store, conn: client,
		cr: bufio.NewReader(client), cw: bufio.NewWriter(client), done: done}
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	t.Cleanup(func() { _ = srv.Close() })

	if _, final := h.readReply(); !strings.HasPrefix(final, "220") {
		t.Fatalf("greeting = %q, want 220...", final)
	}
	h.upgradeTLS(client)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<alice@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Errorf("DATA without tracker = %q, want 250 (fail open)", final)
	}
	if got := back.deliveredTo(); !reflect.DeepEqual(got, []string{"alice@local.test"}) {
		t.Errorf("delivered = %v, want [alice@local.test]", got)
	}
}
