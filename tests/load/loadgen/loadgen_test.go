package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/rest-mail/go-smtp/pkg/smtp"
	"github.com/rest-mail/go-smtp/pkg/smtp/client"
	smtpserver "github.com/rest-mail/go-smtp/pkg/smtp/server"
)

func TestPercentilesNearestRank(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	got := percentiles(d)
	want := Latency{P50: 50, P90: 90, P95: 95, P99: 99, Max: 100}
	if got != want {
		t.Fatalf("percentiles = %+v, want %+v", got, want)
	}
	if (percentiles(nil) != Latency{}) {
		t.Fatal("no samples should give zero latencies")
	}
}

func TestRecorderCapsErrorKinds(t *testing.T) {
	r := newRecorder()
	for i := 0; i < maxErrorKinds+5; i++ {
		r.record(0, fmt.Errorf("error %d", i))
	}
	res := r.result("x", 1, "msg/s", time.Second)
	if res.Errors != maxErrorKinds+5 || len(res.ErrorKinds) != maxErrorKinds+1 || res.ErrorKinds["(other errors)"] != 5 {
		t.Fatalf("errors=%d kinds=%d other=%d", res.Errors, len(res.ErrorKinds), res.ErrorKinds["(other errors)"])
	}
}

func TestPacedOffersTheRateAndCountsSaturation(t *testing.T) {
	r := newRecorder()
	paced(context.Background(), 50, 200*time.Millisecond, 100, r, func(context.Context) error { return nil })
	if res := r.result("x", 50, "msg/s", time.Second); res.OK != 10 || res.Errors != 0 {
		t.Fatalf("ok=%d errors=%d, want 10 and 0", res.OK, res.Errors)
	}

	// One slot, operations that outlast the interval: the rest are counted
	// as saturation, never silently not sent.
	r = newRecorder()
	paced(context.Background(), 100, 100*time.Millisecond, 1, r, func(context.Context) error {
		time.Sleep(150 * time.Millisecond)
		return nil
	})
	res := r.result("x", 100, "msg/s", time.Second)
	if res.Attempted != 10 || res.ErrorKinds[errSaturated.Error()] != 9 {
		t.Fatalf("attempted=%d saturated=%d, want 10 and 9", res.Attempted, res.ErrorKinds[errSaturated.Error()])
	}
}

func TestSMTPAgainstSink(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, d := newSink(l.Addr().String(), "")
	go func() { _ = s.Serve(l) }()
	defer func() { _ = s.Close() }()

	cfg := smtpConfig{addr: l.Addr().String(), helo: "t.test", from: "a@t.test", to: "b@t.test", size: 1000, timeout: 5 * time.Second}
	if err := sendOne(cfg); err != nil {
		t.Fatalf("plain send: %v", err)
	}
	cfg.user, cfg.pass = "u", "p"
	if err := sendOne(cfg); err != nil {
		t.Fatalf("authenticated send: %v", err)
	}
	if n := d.delivered(); n != 2 {
		t.Fatalf("sink got %d messages, want 2", n)
	}
}

func TestMessageIsUniqueAndSized(t *testing.T) {
	cfg := smtpConfig{from: "a@t.test", to: "b@t.test", size: 2000}
	a, b := message(cfg), message(cfg)
	if a == b {
		t.Fatal("two messages are identical")
	}
	if len(a) < 2000 || len(a) > 2400 {
		t.Fatalf("message is %d bytes, want about 2000", len(a))
	}
}

// fakeIMAP answers LOGIN, NOOP, SELECT and LOGOUT like a server would, and
// refuses one password.
func fakeIMAP(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				fmt.Fprint(c, "* OK fake ready\r\n")
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					tag, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
					switch {
					case strings.HasPrefix(cmd, `LOGIN "bad"`):
						fmt.Fprintf(c, "%s NO [AUTHENTICATIONFAILED] no\r\n", tag)
					case strings.HasPrefix(cmd, "SELECT"):
						fmt.Fprintf(c, "* 0 EXISTS\r\n%s OK [READ-WRITE] done\r\n", tag)
					case cmd == "LOGOUT":
						fmt.Fprintf(c, "* BYE\r\n%s OK done\r\n", tag)
						return
					default:
						fmt.Fprintf(c, "%s OK done\r\n", tag)
					}
				}
			}(c)
		}
	}()
	return l.Addr().String()
}

func TestIMAPHoldsConnections(t *testing.T) {
	addr := fakeIMAP(t)
	cfg := imapConfig{addr: addr, user: "u@t.test", pass: "p", interval: 20 * time.Millisecond, timeout: 5 * time.Second}
	res := holdIMAP(context.Background(), cfg, 5, 1000, 300*time.Millisecond)
	if res.Extra["peak_open"].(int64) != 5 || res.Extra["connected"].(int64) != 5 {
		t.Fatalf("extra = %+v, want 5 held", res.Extra)
	}
	if res.Errors != 0 || res.OK == 0 {
		t.Fatalf("ok=%d errors=%d kinds=%v", res.OK, res.Errors, res.ErrorKinds)
	}

	if _, err := dialIMAP(imapConfig{addr: addr, user: "bad", pass: "x", timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "AUTHENTICATIONFAILED") {
		t.Fatalf("a refused login gave %v", err)
	}
}

func TestQuote(t *testing.T) {
	if got := quote(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("quote = %s", got)
	}
}

// startSink runs a real sink and returns its address.
func startSink(t *testing.T, run string) (string, *delivery) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, d := newSink(l.Addr().String(), run)
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close() })
	return l.Addr().String(), d
}

// gatewaySession stands in for the install: it answers 250 to every message,
// as the real gateway does when it queues one, then passes on only those keep
// says to. What happens after the 250 is the thing being measured.
type gatewaySession struct {
	forwardTo string
	keep      func() bool
}

func (s *gatewaySession) Reset()                                 {}
func (s *gatewaySession) Logout() error                          { return nil }
func (s *gatewaySession) Mail(string, *gosmtp.MailOptions) error { return nil }
func (s *gatewaySession) Rcpt(string, *gosmtp.RcptOptions) error { return nil }
func (s *gatewaySession) AuthMechanisms() []string               { return nil }
func (s *gatewaySession) Auth(string) (sasl.Server, error)       { return nil, smtpserver.ErrAuthUnsupported }
func (s *gatewaySession) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if !s.keep() {
		return nil
	}
	c, err := client.Dial(s.forwardTo)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("gateway.test"); err != nil {
		return err
	}
	if err := c.Mail("a@t.test", nil); err != nil {
		return err
	}
	if err := c.Rcpt("b@t.test", nil); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func startGateway(t *testing.T, forwardTo string, keep func() bool) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := smtpserver.New(smtpserver.BackendFunc(func(*smtpserver.Conn) (smtpserver.Session, error) {
		return &gatewaySession{forwardTo: forwardTo, keep: keep}, nil
	}))
	s.Addr = l.Addr().String()
	s.Domain = "gateway.test"
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close() })
	return l.Addr().String()
}

// submit sends messages through addr the way runSMTP does, and writes the
// generator's result where the sink looks for it.
func submit(t *testing.T, addr, run string, n int) (string, Result) {
	t.Helper()
	rec := newRecorder()
	cfg := smtpConfig{addr: addr, helo: "t.test", from: "a@t.test", to: "b@t.test", size: 500, timeout: 5 * time.Second, run: run}
	for i := 0; i < n; i++ {
		start := time.Now()
		rec.record(time.Since(start), sendOne(cfg))
	}
	sent := rec.result("smtp_out", 50, "msg/s", time.Second)
	file := filepath.Join(t.TempDir(), "smtp_out.json")
	if err := writeResult(file, sent); err != nil {
		t.Fatal(err)
	}
	return file, sent
}

// The point of the measurement: a gateway that accepts everything and delivers
// half must read as half delivered. Counting accepted mail would read 10 of 10.
func TestDeliveryCountsMailThatArrivedNotMailThatWasAccepted(t *testing.T) {
	sinkAddr, d := startSink(t, "run1")
	var n atomic.Int64
	gateway := startGateway(t, sinkAddr, func() bool { return n.Add(1)%2 == 0 })

	file, sent := submit(t, gateway, "run1", 10)
	if sent.OK != 10 || sent.Errors != 0 {
		t.Fatalf("the gateway should have accepted all 10: ok=%d errors=%d %v", sent.OK, sent.Errors, sent.ErrorKinds)
	}

	got, drained := awaitDelivery(d, file, 200*time.Millisecond, 10*time.Second, 10*time.Millisecond)
	if got == nil {
		t.Fatal("the sink did not read the sender's result")
	}
	if drained {
		t.Fatal("5 of 10 arrived, yet the sink reports all of them delivered")
	}
	res := d.result("smtp_delivery", got, drained)
	if res.OK != 5 {
		t.Fatalf("ok = %d, want the 5 that arrived, not the %d that were accepted", res.OK, sent.OK)
	}
	if res.Errors != 5 || res.ErrorKinds[errUndelivered] != 5 {
		t.Fatalf("errors = %d (%v), want the 5 accepted and never delivered", res.Errors, res.ErrorKinds)
	}
	if res.Attempted != 10 || res.Extra["accepted"] != int64(10) || res.Extra["delivered"] != int64(5) {
		t.Fatalf("attempted=%d extra=%v, want 10 accepted, 5 delivered", res.Attempted, res.Extra)
	}
	if res.AchievedRate != 5 { // 5 messages over the sender's 1 second
		t.Fatalf("achieved rate = %v, want the delivered mail's, 5", res.AchievedRate)
	}
	if res.Target != 50 {
		t.Fatalf("target = %v, want the sender's", res.Target)
	}
	if res.LatencyMS.P50 <= 0 || res.LatencyMS.Max > 60_000 {
		t.Fatalf("latency = %+v, want each message's time from the sender to the sink", res.LatencyMS)
	}
}

func TestDeliveryOfEverythingStopsWaitingAndMeetsTheTarget(t *testing.T) {
	sinkAddr, d := startSink(t, "run1")
	gateway := startGateway(t, sinkAddr, func() bool { return true })
	file, _ := submit(t, gateway, "run1", 4)

	begin := time.Now()
	sent, drained := awaitDelivery(d, file, time.Minute, 2*time.Minute, 10*time.Millisecond)
	if !drained {
		t.Fatal("all 4 arrived, yet the sink kept waiting")
	}
	if waited := time.Since(begin); waited > 30*time.Second {
		t.Fatalf("waited %v with nothing outstanding", waited)
	}
	if res := d.result("smtp_delivery", sent, drained); res.OK != 4 || res.Errors != 0 || res.ErrorKinds != nil {
		t.Fatalf("ok=%d errors=%d kinds=%v, want 4 and none", res.OK, res.Errors, res.ErrorKinds)
	}
}

// Delivery is at-least-once, and the sink is a shared address: neither a retry
// nor somebody else's message is more of this run's mail delivered.
func TestDeliveryCountsEachOfThisRunsMessagesOnce(t *testing.T) {
	sinkAddr, d := startSink(t, "run1")

	cfg := smtpConfig{addr: sinkAddr, helo: "t.test", from: "a@t.test", to: "b@t.test", size: 100, timeout: 5 * time.Second, run: "run1"}
	raw := message(cfg)
	sendRaw := func(raw string) {
		t.Helper()
		if err := d.arrived(strings.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	}
	sendRaw(raw)
	sendRaw(raw) // the queue retried
	sendRaw(message(smtpConfig{from: "a@t.test", to: "b@t.test", size: 100, run: "another-run"}))
	sendRaw(message(smtpConfig{from: "mailer-daemon@t.test", to: "b@t.test", size: 100})) // a bounce: no tag
	sendRaw("not a message")

	res := d.result("smtp_delivery", &Result{OK: 1, Target: 1, DurationSec: 1}, true)
	if res.OK != 1 {
		t.Fatalf("delivered = %d, want 1", res.OK)
	}
	if res.Extra["duplicates"] != int64(1) || res.Extra["other_messages"] != int64(3) {
		t.Fatalf("extra = %v, want 1 duplicate and 3 others", res.Extra)
	}
}

func TestDeliveryWithoutASenderResultIsAnError(t *testing.T) {
	_, d := startSink(t, "run1")
	missing := filepath.Join(t.TempDir(), "smtp_out.json")
	sent, drained := awaitDelivery(d, missing, time.Millisecond, 50*time.Millisecond, 5*time.Millisecond)
	if sent != nil || drained {
		t.Fatalf("sent=%v drained=%v, want nothing known", sent, drained)
	}
	if res := d.result("smtp_delivery", nil, false); res.Errors == 0 {
		t.Fatal("no sender result must not read as a clean run")
	}
}

// Delivery that outruns the sender's window is reported at the rate it reached.
func TestDeliveryRateIsOverTheLongerOfSendingAndArriving(t *testing.T) {
	d := &delivery{seen: map[string]bool{}, count: 100, first: time.Unix(100, 0), last: time.Unix(150, 0)}
	if res := d.result("x", &Result{OK: 100, DurationSec: 10}, true); res.AchievedRate != 2 || res.DurationSec != 50 {
		t.Fatalf("rate=%v over %vs, want 2 over 50", res.AchievedRate, res.DurationSec)
	}
	if res := d.result("x", &Result{OK: 100, DurationSec: 200}, true); res.AchievedRate != 0.5 || res.DurationSec != 200 {
		t.Fatalf("rate=%v over %vs, want 0.5 over 200", res.AchievedRate, res.DurationSec)
	}
}

func TestParseSources(t *testing.T) {
	p, err := parseSources("10.99.201.1-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.addrs) != 3 || p.addrs[0].String() != "10.99.201.1" || p.addrs[2].String() != "10.99.201.3" {
		t.Fatalf("addrs = %v", p.addrs)
	}
	if p, err := parseSources(""); p != nil || err != nil {
		t.Fatalf("no spec: %v, %v", p, err)
	}
	if p, err := parseSources("10.99.201.7"); err != nil || len(p.addrs) != 1 {
		t.Fatalf("one address: %v, %v", p, err)
	}
	for _, bad := range []string{"nope", "10.99.201.9-3", "10.99.201.1-300", "::1", "10.99.201.1-x"} {
		if _, err := parseSources(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// Connections come from each address in turn: the gateways' per-IP limit is what
// this exists for, so it is the addresses the server saw that are checked.
func TestSourcesAreWhatTheServerSees(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	var mu sync.Mutex
	seen := map[string]int{}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			mu.Lock()
			seen[host]++
			mu.Unlock()
			_ = c.Close()
		}
	}()
	p, err := parseSources("127.0.1.1-3")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		c, err := p.dialer(time.Second).Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		total := seen["127.0.1.1"] + seen["127.0.1.2"] + seen["127.0.1.3"]
		mu.Unlock()
		if total == 6 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, addr := range []string{"127.0.1.1", "127.0.1.2", "127.0.1.3"} {
		if seen[addr] != 2 {
			t.Fatalf("seen = %v, want each address twice", seen)
		}
	}
}
