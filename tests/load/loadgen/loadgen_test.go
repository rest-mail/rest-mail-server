package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
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
	s, count := newSink(l.Addr().String())
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
	if n := count.Load(); n != 2 {
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
