package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/mail"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	smtpserver "github.com/rest-mail/go-smtp/pkg/smtp/server"

	gosmtp "github.com/rest-mail/go-smtp/pkg/smtp"
)

// Headers a tagged message carries from the generator to the sink. Time is
// measured across them, so the two must share a clock: on one machine they do.
const (
	headerRun  = "X-Load-Run"
	headerSent = "X-Load-Sent"
)

const errUndelivered = "accepted by the gateway, never reached the sink"

// runSink is an SMTP server that discards what it receives and counts it: the
// remote MX of outbound load, so the count is of mail that left the install and
// arrived, whatever the generator was told when it submitted.
//
// With -sent-file it waits for the generator's own result, then until everything
// that was accepted has arrived or -drain has passed, and writes the delivery
// result. Without it, it listens for -duration and writes what it counted.
func runSink(args []string) error {
	fs := flag.NewFlagSet("sink", flag.ExitOnError)
	addr := fs.String("addr", ":2525", "listen address")
	duration := fs.Duration("duration", time.Minute, "the longest to listen")
	kind := fs.String("kind", "smtp_delivery", "name of this load in the results, with -sent-file")
	run := fs.String("run", "", "count only messages tagged with this run id")
	sentFile := fs.String("sent-file", "", "the sending generator's result file")
	drain := fs.Duration("drain", 5*time.Minute, "after the sender finishes, how long to wait for what it was accepted")
	ready := fs.String("ready-file", "", "created once the sink is listening")
	out := fs.String("out", "-", "result file (- for stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	l, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	s, d := newSink(*addr, *run)
	go func() { _ = s.Serve(l) }()
	if *ready != "" {
		if err := os.WriteFile(*ready, nil, 0o644); err != nil {
			_ = s.Close()
			return err
		}
	}
	if *sentFile == "" {
		time.Sleep(*duration)
		_ = s.Close()
		n := d.delivered()
		return writeResult(*out, Result{
			Kind: "sink", DurationSec: duration.Seconds(), OK: n, Attempted: n,
			AchievedRate: round(float64(n) / duration.Seconds()), TargetUnit: "msg/s",
		})
	}
	sent, drained := awaitDelivery(d, *sentFile, *drain, *duration, 200*time.Millisecond)
	_ = s.Close()
	return writeResult(*out, d.result(*kind, sent, drained))
}

// awaitDelivery returns the sender's result, and whether everything it was
// accepted arrived, once that is known or the waits run out.
func awaitDelivery(d *delivery, sentFile string, drain, limit, tick time.Duration) (*Result, bool) {
	stop := time.Now().Add(limit)
	var sent *Result
	for time.Now().Before(stop) {
		if sent == nil {
			if sent = readSent(sentFile); sent != nil {
				if until := time.Now().Add(drain); until.Before(stop) {
					stop = until
				}
			}
		}
		if sent != nil && d.delivered() >= sent.OK {
			return sent, true
		}
		time.Sleep(tick)
	}
	if sent == nil {
		sent = readSent(sentFile)
	}
	return sent, sent != nil && d.delivered() >= sent.OK
}

// readSent is nil until the file holds a whole result: the generator writes it
// once, at the end, and a partly written file does not parse.
func readSent(path string) *Result {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var r Result
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

func newSink(addr, run string) (*smtpserver.Server, *delivery) {
	d := &delivery{run: run, seen: map[string]bool{}}
	s := smtpserver.New(smtpserver.BackendFunc(func(*smtpserver.Conn) (smtpserver.Session, error) {
		return &sinkSession{d: d}, nil
	}))
	s.Addr = addr
	s.Domain = "sink.loadgen.test"
	s.ReadTimeout = time.Minute
	s.WriteTimeout = time.Minute
	s.MaxMessageBytes = 64 << 20
	s.AllowInsecureAuth = true
	return s, d
}

// delivery is what has reached the sink. A message counts once however many
// times it comes: delivery is at-least-once, and a retry is not more mail.
type delivery struct {
	run string

	mu         sync.Mutex
	seen       map[string]bool
	count      int64
	duplicates int64
	other      int64
	latencies  []time.Duration
	first      time.Time
	last       time.Time
}

// arrived reads one message off the wire.
func (d *delivery) arrived(r io.Reader) error {
	br := bufio.NewReader(r)
	msg, parseErr := mail.ReadMessage(br)
	if _, err := io.Copy(io.Discard, br); err != nil {
		return err
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	var header mail.Header
	if parseErr == nil {
		header = msg.Header
	}
	if d.run != "" && header.Get(headerRun) != d.run {
		d.other++
		return nil
	}
	if id := header.Get("Message-Id"); id != "" {
		if d.seen[id] {
			d.duplicates++
			return nil
		}
		d.seen[id] = true
	}
	d.count++
	if d.first.IsZero() {
		d.first = now
	}
	d.last = now
	if sent, err := strconv.ParseInt(header.Get(headerSent), 10, 64); err == nil {
		d.latencies = append(d.latencies, max(0, now.Sub(time.Unix(0, sent))))
	}
	return nil
}

func (d *delivery) delivered() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

// result is the delivered mail in the shape every load writes: ok is what
// arrived, errors what was accepted and did not. The rate is over the time the
// sender took or, when the queue fell behind, the time the mail took to arrive.
func (d *delivery) result(kind string, sent *Result, drained bool) Result {
	d.mu.Lock()
	defer d.mu.Unlock()
	res := Result{Kind: kind, TargetUnit: "msg/s", OK: d.count, LatencyMS: percentiles(d.latencies)}
	spread := d.last.Sub(d.first).Seconds()
	res.DurationSec = round(spread)
	var accepted int64
	if sent == nil {
		res.Errors = 1
		res.ErrorKinds = map[string]int64{"the sender wrote no result": 1}
	} else {
		accepted = sent.OK
		res.Target = sent.Target
		res.Attempted = accepted
		res.DurationSec = round(max(spread, sent.DurationSec))
		if missing := accepted - d.count; missing > 0 {
			res.Errors = missing
			res.ErrorKinds = map[string]int64{errUndelivered: missing}
		}
	}
	if res.DurationSec > 0 {
		res.AchievedRate = round(float64(d.count) / res.DurationSec)
	}
	res.Extra = map[string]any{
		"accepted":              accepted,
		"delivered":             d.count,
		"duplicates":            d.duplicates,
		"other_messages":        d.other,
		"first_to_last_s":       round(spread),
		"all_delivered_in_time": drained,
	}
	return res
}

type sinkSession struct{ d *delivery }

func (s *sinkSession) Reset()                                 {}
func (s *sinkSession) Logout() error                          { return nil }
func (s *sinkSession) Mail(string, *gosmtp.MailOptions) error { return nil }
func (s *sinkSession) Rcpt(string, *gosmtp.RcptOptions) error { return nil }
func (s *sinkSession) AuthMechanisms() []string               { return []string{sasl.Plain} }
func (s *sinkSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(string, string, string) error { return nil }), nil
}
func (s *sinkSession) Data(r io.Reader) error { return s.d.arrived(r) }
