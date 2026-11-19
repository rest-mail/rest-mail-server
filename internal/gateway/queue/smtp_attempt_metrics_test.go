package queue

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rest-mail/go-mtasts/pkg/mtasts"

	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
)

// closeConnection, as a scripted reply, drops the connection instead of answering.
const closeConnection = "<close>"

// scriptedMX is one SMTP peer on a real socket. Whatever it is not told answers
// as a healthy MX would.
type scriptedMX struct {
	greeting   string            // default "220 ready"
	replies    map[string]string // by command verb
	finalReply string            // the answer to the message body; default "250 queued"
	extensions []string          // offered in the EHLO reply; default 8BITMIME
}

// mxObservation is what the peer itself saw, independent of the sender's counters.
type mxObservation struct {
	connections int
	commands    []string
	accepted    int    // message bodies it answered with a 2xx
	clientIP    string // the sender's address on the last connection
}

// serve answers connections on listener, one at a time, until finish closes it
// and reports what the peer saw.
func (mx scriptedMX) serve(t *testing.T, listener net.Listener) (finish func() mxObservation) {
	t.Helper()
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan mxObservation, 1)
	go func() {
		var seen mxObservation
		defer func() { done <- seen }()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mx.converse(conn, &seen)
			_ = conn.Close()
		}
	}()
	return func() mxObservation {
		t.Helper()
		_ = listener.Close()
		select {
		case seen := <-done:
			return seen
		case <-time.After(30 * time.Second):
			t.Fatal("the scripted MX did not finish")
			return mxObservation{}
		}
	}
}

// converse runs the script for one connection.
func (mx scriptedMX) converse(conn net.Conn, seen *mxObservation) {
	seen.connections++
	seen.clientIP = addrIP(conn.RemoteAddr())
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	r, wr := bufio.NewReader(conn), bufio.NewWriter(conn)
	reply := func(s string) bool {
		if s == closeConnection {
			return false
		}
		_, _ = wr.WriteString(s + "\r\n")
		_ = wr.Flush()
		return true
	}
	greeting := mx.greeting
	if greeting == "" {
		greeting = "220 ready"
	}
	if !reply(greeting) {
		return
	}
	extensions := mx.extensions
	if extensions == nil {
		extensions = []string{"8BITMIME"}
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		verb, _, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		verb = strings.ToUpper(verb)
		seen.commands = append(seen.commands, verb)
		scripted, isScripted := mx.replies[verb]
		switch {
		case isScripted:
			if !reply(scripted) {
				return
			}
			if verb != "DATA" || !strings.HasPrefix(scripted, "354") {
				continue
			}
		case verb == "EHLO":
			lines := append([]string{"mx.test"}, extensions...)
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				reply("250" + sep + l)
			}
			continue
		case verb == "DATA":
			reply("354 go on")
		case verb == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
			continue
		}
		// The message body, up to the lone dot.
		for {
			body, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimRight(body, "\r\n") == "." {
				break
			}
		}
		final := mx.finalReply
		if final == "" {
			final = "250 queued"
		}
		if strings.HasPrefix(final, "2") {
			seen.accepted++
		}
		if !reply(final) {
			return
		}
	}
}

// addressedConn reports a chosen peer address, so the IPv6 cases run on hosts
// with no IPv6 loopback. TestSMTPAttempt_RealIPv6Socket covers a real one.
type addressedConn struct {
	net.Conn
	remote net.Addr
}

func (c addressedConn) RemoteAddr() net.Addr { return c.remote }

var (
	smtpIPVersions = []string{smtpIPv4, smtpIPv6}
	smtpResults    = []string{smtpAttemptDelivered, smtpAttemptDeferred, smtpAttemptRejected, smtpAttemptFailed}
)

// smtpAttemptCounts reads every series the metric may have.
func smtpAttemptCounts() map[[2]string]float64 {
	counts := map[[2]string]float64{}
	for _, version := range smtpIPVersions {
		for _, result := range smtpResults {
			counts[[2]string{version, result}] = testutil.ToFloat64(metrics.OutboundSMTPAttempts.WithLabelValues(version, result))
		}
	}
	return counts
}

// assertSMTPAttempts fails unless exactly the wanted series rose since before,
// each by the wanted amount. It does not accept a rise anywhere else.
func assertSMTPAttempts(t *testing.T, before map[[2]string]float64, want map[[2]string]float64) {
	t.Helper()
	for series, count := range smtpAttemptCounts() {
		if got := count - before[series]; got != want[series] {
			t.Errorf("attempts{ip_version=%q, result=%q} rose by %v, want %v", series[0], series[1], got, want[series])
		}
	}
}

func attemptWorker() *Worker {
	return &Worker{hostname: "sender.test", allowPrivateDest: true, sendDeadline: defaultSendDeadline}
}

func listenLoopback(t *testing.T) (net.Listener, string, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return listener, host, port
}

// Each way an MX can answer an attempt lands on one result, on the series of
// the IP version the connection used, and on no other series (#1579). The peer's
// own count of accepted bodies is the independent check on "delivered".
func TestSMTPAttempt_CountsEachResultByIPVersion(t *testing.T) {
	cases := []struct {
		name   string
		mx     scriptedMX
		result string
	}{
		{"accepted", scriptedMX{}, smtpAttemptDelivered},
		{"greeting 421", scriptedMX{greeting: "421 too busy"}, smtpAttemptDeferred},
		{"greeting 554", scriptedMX{greeting: "554 no mail from this address"}, smtpAttemptRejected},
		{"connection closed instead of a greeting", scriptedMX{greeting: closeConnection}, smtpAttemptFailed},
		{"EHLO and HELO 550", scriptedMX{replies: map[string]string{"EHLO": "550 go away", "HELO": "550 go away"}}, smtpAttemptRejected},
		{"EHLO and HELO 421", scriptedMX{replies: map[string]string{"EHLO": "421 later", "HELO": "421 later"}}, smtpAttemptDeferred},
		{"MAIL 451", scriptedMX{replies: map[string]string{"MAIL": "451 greylisted"}}, smtpAttemptDeferred},
		{"MAIL 550", scriptedMX{replies: map[string]string{"MAIL": "550 sender refused"}}, smtpAttemptRejected},
		{"MAIL answered with a code that is no verdict", scriptedMX{replies: map[string]string{"MAIL": "354 what"}}, smtpAttemptFailed},
		{"connection closed at MAIL", scriptedMX{replies: map[string]string{"MAIL": closeConnection}}, smtpAttemptFailed},
		{"RCPT 450", scriptedMX{replies: map[string]string{"RCPT": "450 try later"}}, smtpAttemptDeferred},
		{"RCPT 550", scriptedMX{replies: map[string]string{"RCPT": "550 no such user"}}, smtpAttemptRejected},
		{"DATA 451", scriptedMX{replies: map[string]string{"DATA": "451 not now"}}, smtpAttemptDeferred},
		{"DATA 554", scriptedMX{replies: map[string]string{"DATA": "554 refused"}}, smtpAttemptRejected},
		{"body 452", scriptedMX{finalReply: "452 no room"}, smtpAttemptDeferred},
		{"body 550", scriptedMX{finalReply: "550 looks like spam"}, smtpAttemptRejected},
		{"connection closed after the body", scriptedMX{finalReply: closeConnection}, smtpAttemptFailed},
	}
	for _, version := range smtpIPVersions {
		for _, tc := range cases {
			t.Run("IPv"+version+"/"+tc.name, func(t *testing.T) {
				listener, host, port := listenLoopback(t)
				finish := tc.mx.serve(t, listener)
				w := attemptWorker()
				if version == smtpIPv6 {
					w.wrapSMTPConn = func(c net.Conn) net.Conn {
						return addressedConn{Conn: c, remote: &net.TCPAddr{IP: net.ParseIP("2001:db8::25"), Port: 25}}
					}
				}
				before := smtpAttemptCounts()

				err := w.deliverSMTP(context.Background(), net.JoinHostPort(host, port), host, tlsTestItem(), nil, nil)
				seen := finish()

				if (tc.result == smtpAttemptDelivered) != (err == nil) {
					t.Fatalf("deliverSMTP returned %v for a %s attempt", err, tc.result)
				}
				wantAccepted := 0
				if tc.result == smtpAttemptDelivered {
					wantAccepted = 1
				}
				if seen.accepted != wantAccepted {
					t.Fatalf("the MX accepted %d bodies, want %d", seen.accepted, wantAccepted)
				}
				assertSMTPAttempts(t, before, map[[2]string]float64{{version, tc.result}: 1})
			})
		}
	}
}

// A real IPv6 connection counts as IPv6: the peer sees the sender arrive over
// ::1, and the version comes from that socket, not from a test's address.
func TestSMTPAttempt_RealIPv6Socket(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("this host has no IPv6 loopback, TestSMTPAttempt_CountsEachResultByIPVersion still covers the IPv6 series: %v", err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	finish := scriptedMX{}.serve(t, listener)
	before := smtpAttemptCounts()

	if err := attemptWorker().deliverSMTP(context.Background(), net.JoinHostPort("::1", port), "localhost", tlsTestItem(), nil, nil); err != nil {
		t.Fatalf("deliver over IPv6: %v", err)
	}
	seen := finish()
	if seen.clientIP != "::1" || seen.accepted != 1 {
		t.Fatalf("the MX saw client %q and accepted %d bodies, want ::1 and 1", seen.clientIP, seen.accepted)
	}
	assertSMTPAttempts(t, before, map[[2]string]float64{{smtpIPv6, smtpAttemptDelivered}: 1})
}

// Through the worker's own path, the count agrees with the queue row and with the
// MX. The RESTMAIL probe connects first and reports nothing, so an MX that refuses
// at the greeting is met twice and counted once.
func TestSMTPAttempt_ProcessAgreesWithRowAndMX(t *testing.T) {
	gdb := openFailureTestDB(t)
	if err := rmdb.AutoMigrate(gdb); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name            string
		mx              scriptedMX
		result, status  string
		accepted, conns int
	}{
		{"accepted", scriptedMX{}, smtpAttemptDelivered, "delivered", 1, 2},
		{"greylisted at RCPT", scriptedMX{replies: map[string]string{"RCPT": "450 4.7.1 try later"}}, smtpAttemptDeferred, "deferred", 0, 2},
		{"refused at the greeting", scriptedMX{greeting: "554 5.7.1 no mail from this address"}, smtpAttemptRejected, "bounced", 0, 2},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			domain := fmt.Sprintf("attempts-%d-%d.test", now.UnixNano(), i)
			// A null sender gets no DSN, so the row is all a bounce leaves.
			item := models.OutboundQueue{Recipient: "someone@" + domain, Domain: domain,
				RawMessage: "Subject: hi\r\n\r\nhi\r\n", Status: "delivering", Attempts: 1,
				MaxRetries: models.DefaultMaxRetries, NextAttempt: now, ExpiresAt: now.Add(models.QueueLifetime)}
			if err := gdb.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				gdb.Delete(&models.OutboundQueue{}, item.ID)
				gdb.Where("domain = ?", domain).Delete(&models.RESTMAILCapability{})
				gdb.Where("domain = ?", domain).Delete(&models.TLSRPTSession{})
			})
			listener, _, _ := listenLoopback(t)
			finish := tc.mx.serve(t, listener)
			w := NewWorker(gdb, "sender.test", 1, time.Second)
			w.allowPrivateDest = true
			w.resolver = fixedMXResolver{host: "mx." + domain}
			w.smtpAddr = func(string) string { return listener.Addr().String() }
			w.sts.LookupTXT = func(context.Context, string) ([]string, error) { return nil, mtasts.ErrNoPolicy }
			before := smtpAttemptCounts()

			w.process(context.Background(), item, now)
			seen := finish()

			if row := readRow(t, gdb, item.ID); row.Status != tc.status {
				t.Errorf("row status %q (%s), want %q", row.Status, row.LastError, tc.status)
			}
			if seen.accepted != tc.accepted || seen.connections != tc.conns {
				t.Errorf("the MX saw %d connections and accepted %d bodies, want %d and %d", seen.connections, seen.accepted, tc.conns, tc.accepted)
			}
			assertSMTPAttempts(t, before, map[[2]string]float64{{smtpIPv4, tc.result}: 1})
		})
	}
}

// With no connection there is no IP version to report, and no MX answered.
func TestSMTPAttempt_NoConnectionCountsNothing(t *testing.T) {
	listener, host, port := listenLoopback(t)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	before := smtpAttemptCounts()
	if err := attemptWorker().deliverSMTP(context.Background(), net.JoinHostPort(host, port), host, tlsTestItem(), nil, nil); err == nil {
		t.Fatal("delivered to a closed port")
	}
	assertSMTPAttempts(t, before, nil)
}

// REST Mail refusing to send is not the receiver's treatment of the mail, though
// it carries a 550: the MX never sees MAIL, so no result is counted against it.
func TestSMTPAttempt_OwnRefusalCountsNothing(t *testing.T) {
	listener, host, port := listenLoopback(t)
	finish := scriptedMX{extensions: []string{"SIZE 1000000"}}.serve(t, listener)
	item := tlsTestItem()
	item.RawMessage = "Subject: eight bit\r\n\r\ncaf\xc3\xa9\r\n"
	before := smtpAttemptCounts()

	err := attemptWorker().deliverSMTP(context.Background(), net.JoinHostPort(host, port), host, item, nil, nil)
	var refusal *SMTPError
	if !errors.As(err, &refusal) || !refusal.HopLacksExtension {
		t.Fatalf("deliverSMTP returned %v, want the 8BITMIME refusal", err)
	}
	seen := finish()
	for _, command := range seen.commands {
		if command == "MAIL" {
			t.Fatalf("the MX saw MAIL during a refusal: %v", seen.commands)
		}
	}
	assertSMTPAttempts(t, before, nil)
}

// A failed STARTTLS is retried on a new connection, within one attempt: two
// connections, one result.
func TestSMTPAttempt_TLSFallbackCountsOneResult(t *testing.T) {
	host, port, finish := brokenTLSMX(t)
	before := smtpAttemptCounts()
	if err := attemptWorker().deliverSMTP(context.Background(), net.JoinHostPort(host, strconv.Itoa(port)), host, tlsTestItem(), nil, nil); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	finish()
	assertSMTPAttempts(t, before, map[[2]string]float64{{smtpIPv4, smtpAttemptDelivered}: 1})
}

func TestSMTPIPVersion(t *testing.T) {
	for ip, want := range map[string]string{
		"192.0.2.25":        smtpIPv4,
		"::ffff:192.0.2.25": smtpIPv4, // an IPv4 peer on a dual-stack socket
		"2001:db8::25":      smtpIPv6,
		"::1":               smtpIPv6,
		"fe80::1%eth0":      smtpIPv6,
		"":                  "",
		"mx.example.test":   "",
	} {
		if got := smtpIPVersion(ip); got != want {
			t.Errorf("smtpIPVersion(%q) = %q, want %q", ip, got, want)
		}
	}
}

// The metric has one series per IP version and result, present from the start so
// the first failure shows as a rise from zero, and an address that cannot be read
// adds none: the label set cannot grow with the destinations mail goes to.
func TestSMTPAttempt_SeriesAreBounded(t *testing.T) {
	metrics.OutboundSMTPAttempts.Reset()
	NewWorker(nil, "sender.test", 1, time.Second)
	want := len(smtpIPVersions) * len(smtpResults)
	if got := testutil.CollectAndCount(metrics.OutboundSMTPAttempts); got != want {
		t.Fatalf("a new worker exports %d series, want %d", got, want)
	}
	before := smtpAttemptCounts()
	countSMTPAttempt("not an address", nil)
	countSMTPAttempt("", errors.New("broken"))
	assertSMTPAttempts(t, before, nil)
	if got := testutil.CollectAndCount(metrics.OutboundSMTPAttempts); got != want {
		t.Fatalf("unreadable addresses left %d series, want %d", got, want)
	}
}
