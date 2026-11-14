package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

func TestRecorderNamesWhatPassesTheCap(t *testing.T) {
	r := newRecorder()
	for i := 0; i < maxErrorKinds+2; i++ {
		r.record(0, fmt.Errorf("error %d", i))
	}
	if res := r.result("x", 1, "msg/s", time.Second); res.OtherExample != fmt.Sprintf("error %d", maxErrorKinds) {
		t.Fatalf("example = %q", res.OtherExample)
	}
}

// A timeout's message differs on every connection by the client's port. In the
// second real run that spent the cap on 20 of them and left 979 errors unnamed.
func TestRecorderCountsTimeoutsFromAnyClientAddressAsOneKind(t *testing.T) {
	r := newRecorder()
	for i := 0; i < 3*maxErrorKinds; i++ {
		r.record(0, fmt.Errorf("select: read tcp 10.99.203.%d:%d->10.99.0.15:993: i/o timeout", i%50, 40000+i))
	}
	r.record(0, errors.New("dial: read tcp [fd00::1]:5->10.99.0.13:465: read: connection reset by peer"))
	r.record(0, errors.New("ehlo: EOF"))
	res := r.result("x", 1, "msg/s", time.Second)
	want := map[string]int64{
		"select: read tcp client->10.99.0.15:993: i/o timeout":                  3 * maxErrorKinds,
		"dial: read tcp client->10.99.0.13:465: read: connection reset by peer": 1,
		"ehlo: EOF": 1,
	}
	if !reflect.DeepEqual(res.ErrorKinds, want) {
		t.Fatalf("kinds = %v", res.ErrorKinds)
	}
}

func TestPacedOffersTheRateAndCountsSaturation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
	})
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

	for _, name := range []string{"noop", "select"} {
		if each := res.Extra[name].(map[string]any); each["ok"].(int64) == 0 || each["errors"].(int64) != 0 {
			t.Fatalf("%s = %+v, want some answered and none failed", name, each)
		}
	}

	if _, err := dialIMAP(imapConfig{addr: addr, user: "bad", pass: "x", timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "AUTHENTICATIONFAILED") {
		t.Fatalf("a refused login gave %v", err)
	}
}

// A command that fails says which it was, and SELECT is held apart from NOOP: in
// the second real run NOOP answered in under a millisecond, so the percentiles over
// both hid that nearly every SELECT timed out.
func TestIMAPSaysWhichCommandFailed(t *testing.T) {
	addr := imapThatHangsOnSelect(t)
	cfg := imapConfig{addr: addr, user: "u@t.test", pass: "p", interval: 10 * time.Millisecond, timeout: 100 * time.Millisecond}
	res := holdIMAP(context.Background(), cfg, 4, 1000, 600*time.Millisecond)
	if res.Extra["dropped_while_held"].(int64) != 4 {
		t.Fatalf("extra = %+v, want all 4 dropped", res.Extra)
	}
	const kind = "select: read tcp client->"
	var counted int64
	for k, v := range res.ErrorKinds {
		if strings.HasPrefix(k, kind) && strings.HasSuffix(k, "i/o timeout") {
			counted += v
		}
	}
	if counted != 4 || len(res.ErrorKinds) != 1 {
		t.Fatalf("kinds = %v", res.ErrorKinds)
	}
	if sel := res.Extra["select"].(map[string]any); sel["errors"].(int64) != 4 {
		t.Fatalf("select = %+v", sel)
	}
	if noop := res.Extra["noop"].(map[string]any); noop["errors"].(int64) != 0 || noop["ok"].(int64) == 0 {
		t.Fatalf("noop = %+v", noop)
	}
}

// imapThatHangsOnSelect answers everything but SELECT, as a gateway does when the
// API behind it is what is slow.
func imapThatHangsOnSelect(t *testing.T) string {
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
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = fmt.Fprint(c, "* OK ready\r\n")
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					tag, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
					if !strings.HasPrefix(cmd, "SELECT") {
						_, _ = fmt.Fprintf(c, "%s OK done\r\n", tag)
					}
				}
			}()
		}
	}()
	return l.Addr().String()
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

	got, drained := awaitDelivery(d, file, 200*time.Millisecond, 30*time.Second, 10*time.Millisecond)
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
	accepted := make(chan string, 6)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			accepted <- host
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
	seen := map[string]int{}
	deadline := time.After(30 * time.Second)
	for range 6 {
		select {
		case host := <-accepted:
			seen[host]++
		case <-deadline:
			t.Fatalf("seen = %v, want six accepted connections", seen)
		}
	}
	for _, addr := range []string{"127.0.1.1", "127.0.1.2", "127.0.1.3"} {
		if seen[addr] != 2 {
			t.Fatalf("seen = %v, want each address twice", seen)
		}
	}
}

// deferringSession refuses every message at the end of DATA, as a greylist does,
// and records the sender each came from.
type deferringSession struct {
	gatewaySession
	senders *sync.Map
	reply   error
}

func (s *deferringSession) Mail(from string, _ *gosmtp.MailOptions) error {
	s.senders.Store(from, true)
	return nil
}

func (s *deferringSession) Data(r io.Reader) error {
	_, _ = io.Copy(io.Discard, r)
	return s.reply
}

func startDeferring(t *testing.T, reply error) (string, *sync.Map) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	senders := &sync.Map{}
	s := smtpserver.New(smtpserver.BackendFunc(func(*smtpserver.Conn) (smtpserver.Session, error) {
		return &deferringSession{senders: senders, reply: reply}, nil
	}))
	s.Addr = l.Addr().String()
	s.Domain = "gateway.test"
	go func() { _ = s.Serve(l) }()
	t.Cleanup(func() { _ = s.Close() })
	return l.Addr().String(), senders
}

// The greylist's cost is measured as its own load: the 451 is that load's success,
// and each message is a first attempt because each has a sender of its own.
func TestExpectDeferCountsA451AndNothingElse(t *testing.T) {
	deferred := &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "Try again later"}
	rejected := &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "Rejected by policy"}
	cfg := smtpConfig{helo: "t.test", from: "greylisted@t.test", to: "b@t.test", size: 100, timeout: 5 * time.Second, expectDefer: true, uniqueFrom: true}

	addr, senders := startDeferring(t, deferred)
	cfg.addr = addr
	for i := 0; i < 3; i++ {
		if err := sendOne(cfg); err != nil {
			t.Fatalf("a 451 is what this load expects: %v", err)
		}
	}
	count := 0
	senders.Range(func(k, _ any) bool {
		count++
		if addr := k.(string); !strings.HasPrefix(addr, "greylisted+") || !strings.HasSuffix(addr, "@t.test") {
			t.Errorf("sender %q is not -from with a tag", addr)
		}
		return true
	})
	if count != 3 {
		t.Fatalf("%d senders for 3 messages, want one each", count)
	}

	cfg.addr, _ = startDeferring(t, rejected)
	if err := sendOne(cfg); err == nil || !strings.Contains(err.Error(), "550") {
		t.Fatalf("a 550 is not a deferral, got %v", err)
	}
	cfg.addr, _ = startDeferring(t, nil)
	if err := sendOne(cfg); !errors.Is(err, errNotDeferred) {
		t.Fatalf("an accepted message is not a deferral, got %v", err)
	}
	// Without the flag a 451 stays the error it was.
	cfg.addr, _ = startDeferring(t, deferred)
	cfg.expectDefer = false
	if err := sendOne(cfg); err == nil || !strings.Contains(err.Error(), "451") {
		t.Fatalf("a 451 without -expect-defer should be an error, got %v", err)
	}
}

// fakeAdminAPI answers what runAllow calls, requires the token sign-in gave, and
// keeps the allowlist it was asked to.
func fakeAdminAPI(t *testing.T) (*httptest.Server, map[string]uint) {
	t.Helper()
	rules := map[string]uint{"@other.test": 1}
	next := uint(2)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "admin@t.test" || body["password"] != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: accessCookie, Value: "tok"})
	})
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/v1/admin/domains", authed(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":3,"name":"other.test"},{"id":7,"name":"t.test"}]}`)
	}))
	mux.HandleFunc("POST /api/v1/admin/domains/7/allowlist", authed(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, seen := rules[body["pattern"]]; seen {
			w.WriteHeader(http.StatusConflict)
			return
		}
		rules[body["pattern"]] = next
		next++
		w.WriteHeader(http.StatusCreated)
	}))
	mux.HandleFunc("GET /api/v1/admin/domains/7/allowlist", authed(func(w http.ResponseWriter, _ *http.Request) {
		var data []map[string]any
		for pattern, id := range rules {
			data = append(data, map[string]any{"id": id, "pattern": pattern})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	mux.HandleFunc("DELETE /api/v1/admin/domains/7/allowlist/{eid}", authed(func(w http.ResponseWriter, r *http.Request) {
		for pattern, id := range rules {
			if fmt.Sprint(id) == r.PathValue("eid") {
				delete(rules, pattern)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rules
}

func TestAllowAddsTheSenderOnceAndRemovesOnlyIt(t *testing.T) {
	srv, rules := fakeAdminAPI(t)
	args := []string{"-api", srv.URL, "-user", "admin@t.test", "-pass", "secret", "-domain", "t.test", "-sender", "load@t.test"}
	for i := 0; i < 2; i++ { // a rule left by an earlier run is not an error
		if err := runAllow(args); err != nil {
			t.Fatalf("allow, time %d: %v", i+1, err)
		}
	}
	if _, ok := rules["load@t.test"]; !ok || len(rules) != 2 {
		t.Fatalf("rules after allow: %v", rules)
	}
	remove := append(args, "-remove")
	for i := 0; i < 2; i++ { // nor is a rule already gone
		if err := runAllow(remove); err != nil {
			t.Fatalf("remove, time %d: %v", i+1, err)
		}
	}
	if _, ok := rules["@other.test"]; !ok || len(rules) != 1 {
		t.Fatalf("rules after remove: %v", rules)
	}
}

func TestAllowSaysWhyItFailed(t *testing.T) {
	srv, _ := fakeAdminAPI(t)
	if err := runAllow([]string{"-api", srv.URL, "-user", "admin@t.test", "-pass", "wrong", "-domain", "t.test"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a refused sign-in: %v", err)
	}
	if err := runAllow([]string{"-api", srv.URL, "-user", "admin@t.test", "-pass", "secret", "-domain", "missing.test"}); err == nil || !strings.Contains(err.Error(), "missing.test") {
		t.Fatalf("an unknown domain: %v", err)
	}
}
