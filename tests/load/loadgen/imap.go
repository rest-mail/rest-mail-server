package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type imapConfig struct {
	addr        string
	implicitTLS bool
	user        string
	pass        string
	interval    time.Duration
	timeout     time.Duration
	sources     *sourcePool
}

// runIMAP opens conns authenticated IMAP connections, ramping at connectRate a
// second, and holds them open until the duration ends, each alternating NOOP
// and SELECT INBOX every interval. IMAP is stateful, so what is measured is how
// many connections the server holds and how fast it answers while holding them.
func runIMAP(args []string) error {
	fs := flag.NewFlagSet("imap", flag.ExitOnError)
	var cfg imapConfig
	conns := fs.Int("conns", 1000, "concurrent authenticated connections to hold")
	connectRate := fs.Float64("connect-rate", 50, "new connections per second while ramping up")
	duration := fs.Duration("duration", time.Minute, "how long to hold the connections (ramp included)")
	out := fs.String("out", "-", "result file (- for stdout)")
	fs.StringVar(&cfg.addr, "addr", "imap.restmail.test:993", "IMAP server host:port")
	fs.BoolVar(&cfg.implicitTLS, "tls", true, "implicit TLS (port 993)")
	fs.StringVar(&cfg.user, "user", "frank@restmail.test", "user to sign in as")
	fs.StringVar(&cfg.pass, "pass", "password123", "password for -user")
	fs.DurationVar(&cfg.interval, "interval", 10*time.Second, "time between commands on one connection")
	fs.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "per-command timeout")
	sources := fs.String("source-ips", "", "connect from these local addresses in turn, as 10.99.203.1-56")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var err error
	if cfg.sources, err = parseSources(*sources); err != nil {
		return err
	}
	res := holdIMAP(context.Background(), cfg, *conns, *connectRate, *duration)
	return writeResult(*out, res)
}

func holdIMAP(ctx context.Context, cfg imapConfig, conns int, connectRate float64, duration time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	connects := newRecorder() // dial + greeting + LOGIN
	commands := newRecorder() // NOOP / SELECT on an open connection
	// Each on its own as well: NOOP is answered by the gateway alone and SELECT
	// lists the folder through the API, so together they describe neither.
	noops, selects := newRecorder(), newRecorder()
	var open, peak, dropped atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	gap := time.Duration(float64(time.Second) / connectRate)
	for i := 0; i < conns; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(gap):
			}
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t0 := time.Now()
			c, err := dialIMAP(cfg)
			connects.record(time.Since(t0), err)
			if err != nil {
				return
			}
			defer c.close()
			n := open.Add(1)
			defer open.Add(-1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			// Spread each connection's commands over the interval.
			tick := time.NewTicker(cfg.interval)
			defer tick.Stop()
			for j := i; ; j++ {
				select {
				case <-ctx.Done():
					_, _ = c.command("LOGOUT")
					return
				case <-tick.C:
				}
				cmd, name, each := "NOOP", "noop", noops
				if j%2 == 1 {
					cmd, name, each = "SELECT INBOX", "select", selects
				}
				t1 := time.Now()
				_, err := c.command(cmd)
				took := time.Since(t1)
				if err != nil {
					err = fmt.Errorf("%s: %w", name, err)
				}
				commands.record(took, err)
				each.record(took, err)
				if err != nil {
					dropped.Add(1)
					return
				}
			}
		}(i)
	}
	<-ctx.Done()
	held := open.Load() // connections still open when the duration ends
	wg.Wait()
	elapsed := time.Since(start)

	res := commands.result("imap", float64(conns), "connections", elapsed)
	cr := connects.result("imap_connect", float64(conns), "connections", elapsed)
	res.Errors += cr.Errors
	res.Attempted += cr.Attempted
	for k, v := range cr.ErrorKinds {
		if res.ErrorKinds == nil {
			res.ErrorKinds = map[string]int64{}
		}
		res.ErrorKinds["connect: "+k] += v
	}
	res.Extra = map[string]any{
		"connected":            cr.OK,
		"connect_errors":       cr.Errors,
		"peak_open":            peak.Load(),
		"open_at_end":          held,
		"dropped_while_held":   dropped.Load(),
		"connect_latency_ms":   cr.LatencyMS,
		"commands_per_second":  res.AchievedRate,
		"command_interval_sec": cfg.interval.Seconds(),
		"noop":                 commandSummary(noops),
		"select":               commandSummary(selects),
	}
	// For IMAP the headline rate is the connections held, not commands.
	res.AchievedRate = float64(peak.Load())
	return res
}

// commandSummary is how one IMAP command fared over a run.
func commandSummary(r *recorder) map[string]any {
	res := r.result("", 0, "", 0)
	return map[string]any{"ok": res.OK, "errors": res.Errors, "latency_ms": res.LatencyMS}
}

type imapConn struct {
	conn    net.Conn
	r       *bufio.Reader
	tag     int
	timeout time.Duration
}

func dialIMAP(cfg imapConfig) (*imapConn, error) {
	d := cfg.sources.dialer(cfg.timeout)
	var (
		conn net.Conn
		err  error
	)
	if cfg.implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", cfg.addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // the testbed's own CA
	} else {
		conn, err = d.Dial("tcp", cfg.addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	c := &imapConn{conn: conn, r: bufio.NewReader(conn), timeout: cfg.timeout}
	_ = conn.SetReadDeadline(time.Now().Add(cfg.timeout))
	greeting, err := c.r.ReadString('\n')
	if err != nil {
		c.close()
		return nil, fmt.Errorf("greeting: %w", err)
	}
	if !strings.HasPrefix(greeting, "* OK") {
		c.close()
		return nil, fmt.Errorf("greeting: %s", strings.TrimSpace(greeting))
	}
	if _, err := c.command("LOGIN " + quote(cfg.user) + " " + quote(cfg.pass)); err != nil {
		c.close()
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

// command sends one tagged command and reads to its tagged reply, returning
// the untagged lines. A NO or BAD reply is an error.
func (c *imapConn) command(cmd string) ([]string, error) {
	c.tag++
	tag := "L" + strconv.Itoa(c.tag)
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	if _, err := fmt.Fprintf(c.conn, "%s %s\r\n", tag, cmd); err != nil {
		return nil, err
	}
	var lines []string
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return lines, err
		}
		line = strings.TrimRight(line, "\r\n")
		if rest, ok := strings.CutPrefix(line, tag+" "); ok {
			if strings.HasPrefix(rest, "OK") {
				return lines, nil
			}
			return lines, fmt.Errorf("%s", rest)
		}
		lines = append(lines, line)
	}
}

func (c *imapConn) close() { _ = c.conn.Close() }

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
