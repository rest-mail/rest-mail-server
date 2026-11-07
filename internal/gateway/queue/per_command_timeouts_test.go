package queue

import (
	"bufio"
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// These tests run the scenarios of #714 on a compressed clock: one test minute
// is testMinute, so a "5 minute" reply takes a second rather than five minutes.
const testMinute = 200 * time.Millisecond

// oldAttemptDeadline is the whole-attempt deadline #714 replaced, 30 seconds on
// the same clock. Every test sets it as sendDeadline, which no longer bounds SMTP.
func oldAttemptDeadline(int64) time.Duration { return testMinute / 2 }

// rfcTimeoutsOnTestClock is RFC 5321 §4.5.3.2's minimums on the test clock.
func rfcTimeoutsOnTestClock() config.OutboundSMTPTimeouts {
	return config.OutboundSMTPTimeouts{
		Greeting: 5 * testMinute, Mail: 5 * testMinute, Rcpt: 5 * testMinute, Data: 2 * testMinute,
		DataBlock: 3 * testMinute, FinalReply: 10 * testMinute,
		Hello: 5 * testMinute, StartTLS: 5 * testMinute, Auth: 5 * testMinute, Quit: 5 * testMinute,
	}
}

// everyStep sets every timer to d.
func everyStep(d time.Duration) config.OutboundSMTPTimeouts {
	return config.OutboundSMTPTimeouts{Greeting: d, Mail: d, Rcpt: d, Data: d, DataBlock: d, FinalReply: d,
		Hello: d, StartTLS: d, Auth: d, Quit: d}
}

// timedMX is a fake SMTP peer whose answers take as long as the test says.
type timedMX struct {
	t       *testing.T
	ln      net.Listener
	opts    timedMXOptions
	release chan struct{}

	mu     sync.Mutex
	copies int
}

type timedMXOptions struct {
	// delay holds back a step's answer: "greeting", "EHLO", "MAIL", "RCPT",
	// "DATA", "final" (the reply to the final "."), or "QUIT".
	delay map[string]time.Duration
	// stall is the step at which the peer goes quiet, one of the same names or
	// "block" (it stops reading the message).
	stall string
	// readPause slows the peer down by this long per MiB of the message it reads.
	readPause time.Duration
}

func newTimedMX(t *testing.T, opts timedMXOptions) *timedMX {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mx := &timedMX{t: t, ln: ln, opts: opts, release: make(chan struct{})}
	var conns sync.WaitGroup
	conns.Add(1)
	go func() {
		defer conns.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func() {
				defer conns.Done()
				defer conn.Close()
				mx.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		close(mx.release)
		_ = ln.Close()
		conns.Wait()
	})
	return mx
}

func (mx *timedMX) addr(string) string { return mx.ln.Addr().String() }

func (mx *timedMX) copiesReceived() int {
	mx.mu.Lock()
	defer mx.mu.Unlock()
	return mx.copies
}

// answerAt waits out the step's delay, or until the test ends when the step is the
// stall, and reports whether to go on.
func (mx *timedMX) answerAt(step string) bool {
	if mx.opts.stall == step {
		<-mx.release
		return false
	}
	select {
	case <-time.After(mx.opts.delay[step]):
		return true
	case <-mx.release:
		return false
	}
}

func (mx *timedMX) serve(conn net.Conn) {
	reader := bufio.NewReader(conn)
	reply := func(s string) bool {
		_, err := conn.Write([]byte(s + "\r\n"))
		return err == nil
	}
	if !mx.answerAt("greeting") || !reply("220 timed ready") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if !mx.answerAt(command) {
			return
		}
		switch command {
		case "EHLO":
			reply("250-timed\r\n250 8BITMIME")
		case "MAIL", "RCPT":
			reply("250 ok")
		case "DATA":
			reply("354 go on")
			if !mx.readMessage(reader) || !mx.answerAt("final") {
				return
			}
			reply("250 2.0.0 accepted")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("500 what")
			return
		}
	}
}

// readMessage reads up to the final "." and counts the copy: from there the peer
// has the message, whatever the client then makes of the reply.
func (mx *timedMX) readMessage(reader *bufio.Reader) bool {
	if mx.opts.stall == "block" {
		<-mx.release
		return false
	}
	read := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		if line == ".\r\n" {
			mx.mu.Lock()
			mx.copies++
			mx.mu.Unlock()
			return true
		}
		before := read / (1 << 20)
		read += len(line)
		if mx.opts.readPause > 0 && read/(1<<20) != before {
			time.Sleep(mx.opts.readPause)
		}
	}
}

func timedItem() models.OutboundQueue {
	return models.OutboundQueue{Sender: "sender@example.test", Recipient: "user@example.test", Domain: "example.test",
		RawMessage: "Subject: timers\r\n\r\nhello\r\n"}
}

func timedWorker(t config.OutboundSMTPTimeouts) *Worker {
	return &Worker{hostname: "sender.test", allowPrivateDest: true, sendDeadline: oldAttemptDeadline, smtpTimeouts: t}
}

// RFC 5321 §4.5.3.2.1: a server may hold its greeting back until its load permits.
// #714's first "done when": a greeting delayed by 60 seconds, twice what the old
// deadline allowed for the whole attempt, still gets the message.
func TestDeliverSMTP_SlowGreetingWithinItsTimeoutSucceeds(t *testing.T) {
	mx := newTimedMX(t, timedMXOptions{delay: map[string]time.Duration{"greeting": testMinute}})
	if err := timedWorker(rfcTimeoutsOnTestClock()).deliverSMTP(mx.addr(""), "127.0.0.1", timedItem(), nil, nil); err != nil {
		t.Fatalf("a greeting within its timeout must not fail the attempt: %v", err)
	}
	if got := mx.copiesReceived(); got != 1 {
		t.Fatalf("copies = %d, want 1", got)
	}
}

// RFC 5321 §4.5.3.2.6: a final reply that comes within its 10 minutes must not
// time out, or the retry delivers a second copy. #714's second "done when".
func TestDeliverSMTP_SlowFinalReplyGetsOneCopy(t *testing.T) {
	mx := newTimedMX(t, timedMXOptions{delay: map[string]time.Duration{"final": 5 * testMinute}})
	if err := timedWorker(rfcTimeoutsOnTestClock()).deliverSMTP(mx.addr(""), "127.0.0.1", timedItem(), nil, nil); err != nil {
		t.Fatalf("a final reply within its timeout must not fail the attempt: %v", err)
	}
	if got := mx.copiesReceived(); got != 1 {
		t.Fatalf("copies = %d, want 1", got)
	}
}

// Steps that each stay within their timer may together take far longer than any
// one of them: nothing times the attempt as a whole.
func TestDeliverSMTP_TimersDoNotAddUp(t *testing.T) {
	delay := time.Duration(testMinute)
	mx := newTimedMX(t, timedMXOptions{delay: map[string]time.Duration{
		"greeting": delay, "EHLO": delay, "MAIL": delay, "RCPT": delay, "DATA": delay, "final": delay,
	}})
	started := time.Now()
	if err := timedWorker(everyStep(2*testMinute)).deliverSMTP(mx.addr(""), "127.0.0.1", timedItem(), nil, nil); err != nil {
		t.Fatalf("steps within their timers must not fail the attempt: %v", err)
	}
	if took := time.Since(started); took <= 2*testMinute {
		t.Fatalf("the attempt took %v, so it did not outlast any single timer", took)
	}
}

// A stalled peer is dropped at the timeout of the step it stalled in (#714's third
// "done when"), while every other timer is far from expiring.
func TestDeliverSMTP_StalledStepIsDroppedAtItsTimeout(t *testing.T) {
	const timeout = 150 * time.Millisecond
	big := strings.Repeat("synthetic payload line\r\n", 1<<20) // past any socket buffer
	for _, tc := range []struct {
		stall string
		set   func(*config.OutboundSMTPTimeouts)
		raw   string
	}{
		{"greeting", func(s *config.OutboundSMTPTimeouts) { s.Greeting = timeout }, ""},
		{"EHLO", func(s *config.OutboundSMTPTimeouts) { s.Hello = timeout }, ""},
		{"MAIL", func(s *config.OutboundSMTPTimeouts) { s.Mail = timeout }, ""},
		{"RCPT", func(s *config.OutboundSMTPTimeouts) { s.Rcpt = timeout }, ""},
		{"DATA", func(s *config.OutboundSMTPTimeouts) { s.Data = timeout }, ""},
		{"block", func(s *config.OutboundSMTPTimeouts) { s.DataBlock = timeout }, big},
		{"final", func(s *config.OutboundSMTPTimeouts) { s.FinalReply = timeout }, ""},
	} {
		t.Run(tc.stall, func(t *testing.T) {
			mx := newTimedMX(t, timedMXOptions{stall: tc.stall})
			timers := everyStep(time.Hour)
			tc.set(&timers)
			w := timedWorker(timers)
			w.sendDeadline = func(int64) time.Duration { return 10 * time.Second } // only to end an unfixed run
			item := timedItem()
			if tc.raw != "" {
				item.RawMessage += tc.raw
			}
			started := time.Now()
			err := w.deliverSMTP(mx.addr(""), "127.0.0.1", item, nil, nil)
			took := time.Since(started)
			if err == nil {
				t.Fatal("a stalled peer must fail the attempt")
			}
			if took < timeout || took > 5*time.Second {
				t.Fatalf("dropped after %v, want soon after %v: %v", took, timeout, err)
			}
			if got := ClassifyFailure(err); got != models.FailureConnection {
				t.Fatalf("a stall must be retried as a connection failure, classified %s: %v", got, err)
			}
		})
	}
}

// A peer that stalls on QUIT has already taken the message: the attempt succeeds,
// and the stall costs only QUIT's own timer.
func TestDeliverSMTP_StalledQuitStillDelivers(t *testing.T) {
	const timeout = 150 * time.Millisecond
	mx := newTimedMX(t, timedMXOptions{stall: "QUIT"})
	timers := everyStep(time.Hour)
	timers.Quit = timeout
	w := timedWorker(timers)
	w.sendDeadline = func(int64) time.Duration { return 10 * time.Second } // only to end an unfixed run
	started := time.Now()
	if err := w.deliverSMTP(mx.addr(""), "127.0.0.1", timedItem(), nil, nil); err != nil {
		t.Fatalf("a stalled QUIT must not fail an accepted message: %v", err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("QUIT held the worker for %v, want soon after %v", took, timeout)
	}
	if got := mx.copiesReceived(); got != 1 {
		t.Fatalf("copies = %d, want 1", got)
	}
}

// Each block has a timer of its own (RFC 5321 §4.5.3.2.5): a large message taken
// slowly but steadily outlasts the block timer many times over and still arrives.
//
// The pace and the timer are on a clock of the connection's own, which a write
// moves forward by how long the peer takes over those bytes. On the wall clock a
// loaded machine starved the peer past one block's timer (#1942), so the test
// decided nothing about the timer.
func TestDeliverSMTP_EachDataBlockHasItsOwnTimer(t *testing.T) {
	const blockTimeout = 300 * time.Millisecond
	mx := newTimedMX(t, timedMXOptions{})
	timers := everyStep(time.Hour)
	timers.DataBlock = blockTimeout
	w := timedWorker(timers)
	w.sendDeadline = func(int64) time.Duration { return blockTimeout } // the old cap, on the same scale
	var paced *pacedConn
	w.wrapSMTPConn = func(conn net.Conn) net.Conn {
		paced = &pacedConn{Conn: conn, perMiB: 40 * time.Millisecond}
		return paced
	}
	item := timedItem()
	item.RawMessage += strings.Repeat("synthetic payload line\r\n", 700000) // about 16 MiB
	if err := w.deliverSMTP(mx.addr(""), "127.0.0.1", item, nil, nil); err != nil {
		t.Fatalf("a message read steadily must not time out: %v", err)
	}
	if took := paced.elapsed(); took <= blockTimeout {
		t.Fatalf("the transfer took %v, so it did not outlast one block's timer", took)
	}
	if got := mx.copiesReceived(); got != 1 {
		t.Fatalf("copies = %d, want 1", got)
	}
}

// pacedConn keeps a socket's deadline on a clock of its own: each write moves the
// clock on by perMiB for each MiB written, as a peer taking the bytes at that pace
// would. A write past the deadline fails, and from then on the real socket fails
// too, reads included, as a socket past its deadline does. Until then the real
// socket gets no deadline, so the wall clock decides nothing.
type pacedConn struct {
	net.Conn
	perMiB time.Duration

	mu       sync.Mutex
	now      time.Duration
	deadline time.Duration // on the conn's clock; zero is none
}

func (c *pacedConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = 0
	if !t.IsZero() {
		c.deadline = c.now + time.Until(t)
	}
	return nil
}

func (c *pacedConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

func (c *pacedConn) SetReadDeadline(time.Time) error { return nil }

func (c *pacedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.now += time.Duration(len(p)) * c.perMiB / (1 << 20)
	late := c.deadline != 0 && c.now > c.deadline
	c.mu.Unlock()
	if late {
		_ = c.Conn.SetDeadline(time.Unix(1, 0))
		return 0, &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}
	return c.Conn.Write(p)
}

func (c *pacedConn) elapsed() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func TestWriteDataBlocks_ArmsOncePerBlockAndForTheFlush(t *testing.T) {
	var written strings.Builder
	arms, flushes := 0, 0
	raw := strings.Repeat("x", 3*dataBlockSize+1)
	err := writeDataBlocks(&written, func() error { flushes++; return nil }, raw, func() { arms++ })
	if err != nil {
		t.Fatal(err)
	}
	if written.String() != raw {
		t.Fatal("the blocks do not add up to the message")
	}
	if arms != 5 || flushes != 1 { // four blocks and the flush
		t.Fatalf("arms = %d, flushes = %d; want 5 and 1", arms, flushes)
	}
}

// The EHLO probe waits for a slow peer as delivery does, so that peer is not
// treated as unreachable (#714's fourth requirement).
func TestProbeRESTMAIL_SlowGreetingIsStillProbed(t *testing.T) {
	mx := newTimedMX(t, timedMXOptions{delay: map[string]time.Duration{"greeting": testMinute}})
	if probed, _ := timedWorker(rfcTimeoutsOnTestClock()).probeRESTMAIL(mx.addr(""), "127.0.0.1"); !probed {
		t.Fatal("a slow greeting within its timeout must complete the probe")
	}
}

func TestProbeRESTMAIL_StalledEHLOIsDroppedAtItsTimeout(t *testing.T) {
	const timeout = 150 * time.Millisecond
	mx := newTimedMX(t, timedMXOptions{stall: "EHLO"})
	timers := everyStep(time.Hour)
	timers.Hello = timeout
	w := timedWorker(timers)
	w.sendDeadline = func(int64) time.Duration { return 10 * time.Second } // only to end an unfixed run
	started := time.Now()
	if probed, _ := w.probeRESTMAIL(mx.addr(""), "127.0.0.1"); probed {
		t.Fatal("a stalled EHLO must not complete the probe")
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("dropped after %v, want soon after %v", took, timeout)
	}
}

// The defaults cover #714's scenarios at full scale: a 60 second greeting and a 5
// minute final reply are within their timers, and the row outlives the longest step.
func TestDefaultTimersCoverTheIssuesScenarios(t *testing.T) {
	d := config.DefaultOutboundSMTPTimeouts()
	if d.Greeting <= time.Minute {
		t.Errorf("greeting timer %v does not cover a 60 second greeting", d.Greeting)
	}
	if d.FinalReply <= 5*time.Minute {
		t.Errorf("final reply timer %v does not cover a 5 minute reply", d.FinalReply)
	}
	w := NewWorker(nil, "mx.local", 1, time.Second)
	if want := 12 * time.Minute; w.reclaim != want {
		t.Errorf("default reclaim = %v, want %v", w.reclaim, want)
	}
	if w.reclaim <= w.timeouts().Longest() {
		t.Errorf("reclaim %v must exceed the longest step %v", w.reclaim, w.timeouts().Longest())
	}
}

// Whatever an operator sets the timers to, a row survives a missed heartbeat.
func TestReclaimAlwaysOutlastsAHeartbeat(t *testing.T) {
	if got := everyStep(time.Millisecond).Reclaim(); got <= defaultHeartbeat {
		t.Fatalf("reclaim %v must exceed the heartbeat %v", got, defaultHeartbeat)
	}
}

// fixedMXResolver answers every MX lookup with one host.
type fixedMXResolver struct{ host string }

func (r fixedMXResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	return []*net.MX{{Host: r.host, Pref: 10}}, nil
}

func (r fixedMXResolver) LookupHost(context.Context, string) ([]string, error) {
	return []string{r.host}, nil
}

// migrateDeliveryTables adds what a full delivery touches to the scratch database.
func migrateDeliveryTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&models.RESTMAILCapability{}, &models.TLSRPTSession{}); err != nil {
		t.Fatal(err)
	}
}

// claimingWorker is a real worker, as the gateway starts it, that sends to mx.
func claimingWorker(t *testing.T, db *gorm.DB, mx *timedMX, testReset bool) *Worker {
	t.Helper()
	w := NewWorker(db, "sender.test", 1, time.Second)
	w.allowPrivateDest = true
	w.resolver = fixedMXResolver{host: "127.0.0.1"}
	w.smtpAddr = mx.addr
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) { return nil, mtasts.ErrNoPolicy }
	w.SetSuppressionTTL(time.Hour)
	if testReset {
		w.EnableTestReset(&config.Config{Environment: "test"})
	}
	return w
}

func queueTimedRow(t *testing.T, db *gorm.DB, sender string, set func(*models.OutboundQueue)) models.OutboundQueue {
	t.Helper()
	now := time.Now()
	item := models.OutboundQueue{Sender: sender, Recipient: "someone@remote.test", Domain: "remote.test",
		RawMessage: "From: " + sender + "\r\nTo: someone@remote.test\r\nSubject: timers\r\n\r\nhello\r\n",
		Status:     "pending", NextAttempt: now.Add(-time.Second), ExpiresAt: now.Add(time.Hour), MaxRetries: 10}
	if set != nil {
		set(&item)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func readRow(t *testing.T, db *gorm.DB, id uint) models.OutboundQueue {
	t.Helper()
	var row models.OutboundQueue
	if err := db.First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

// An attempt that outlasts the reclaim interval while it is alive is not taken by
// another worker and sent twice, the case the set-once last_attempt got wrong. The
// peer here takes longer over the final reply than reclaim allows, with workers
// polling throughout. A test minute is short enough for one slow database write to
// outlast it, which at full scale is no threat to a 12 minute reclaim, so reclaim
// here is many heartbeats long, and the reply longer still.
func TestLiveAttemptIsNotReclaimedAndDeliveredTwice(t *testing.T) {
	for _, testReset := range []bool{false, true} {
		t.Run("testReset="+strconv.FormatBool(testReset), func(t *testing.T) {
			db := openResetWorkerDB(t)
			migrateDeliveryTables(t, db)
			_, sender := sendingDomain(t, db)
			mx := newTimedMX(t, timedMXOptions{delay: map[string]time.Duration{"final": 15 * testMinute}})
			configure := func(w *Worker) *Worker {
				w.smtpTimeouts = rfcTimeoutsOnTestClock()
				w.smtpTimeouts.FinalReply = 20 * testMinute
				w.sendDeadline = func(int64) time.Duration { return time.Minute }
				w.reclaim = 10 * testMinute
				w.heartbeatEvery = testMinute / 4
				return w
			}
			holder := configure(claimingWorker(t, db, mx, testReset))
			rival := configure(claimingWorker(t, db, mx, testReset))
			item := queueTimedRow(t, db, sender.Address, nil)

			done := make(chan struct{})
			go func() { holder.processOne(0); close(done) }()
			deadline := time.After(30 * time.Second)
			for delivering := false; ; time.Sleep(testMinute / 10) {
				select {
				case <-done:
					row := readRow(t, db, item.ID)
					if !delivering {
						t.Log("the claim was not observed while it ran")
					}
					if row.Status != "delivered" {
						t.Fatalf("status = %s (%s), want delivered", row.Status, row.LastError)
					}
					if row.Attempts != 1 {
						t.Fatalf("attempts = %d: the live attempt's row was reclaimed", row.Attempts)
					}
					if got := mx.copiesReceived(); got != 1 {
						t.Fatalf("the peer got %d copies, want 1", got)
					}
					return
				case <-deadline:
					t.Fatal("the attempt did not finish")
				default:
				}
				delivering = delivering || readRow(t, db, item.ID).Status == "delivering"
				if delivering {
					rival.processOne(1)
				}
			}
		})
	}
}

// A row whose worker died comes back once the reclaim interval has passed since
// its last heartbeat, at the 12 minute default, and is delivered; one still within
// the interval is left alone.
func TestCrashedWorkersRowIsReclaimed(t *testing.T) {
	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	mx := newTimedMX(t, timedMXOptions{})
	w := claimingWorker(t, db, mx, false)
	abandoned := func(age time.Duration) func(*models.OutboundQueue) {
		return func(row *models.OutboundQueue) {
			last := time.Now().Add(-age)
			row.Status, row.Attempts, row.LastAttempt = "delivering", 1, &last
		}
	}
	alive := queueTimedRow(t, db, sender.Address, abandoned(11*time.Minute))
	crashed := queueTimedRow(t, db, sender.Address, abandoned(13*time.Minute))

	w.processOne(0)

	row := readRow(t, db, crashed.ID)
	if row.Status != "delivered" || row.Attempts != 2 {
		t.Fatalf("the crashed worker's row: status %s, attempts %d (%s); want delivered, 2", row.Status, row.Attempts, row.LastError)
	}
	if got := mx.copiesReceived(); got != 1 {
		t.Fatalf("the peer got %d copies, want 1", got)
	}
	if row := readRow(t, db, alive.ID); row.Status != "delivering" || row.Attempts != 1 {
		t.Fatalf("a row within the interval was taken: status %s, attempts %d", row.Status, row.Attempts)
	}
}

// waitForRefresh waits until the row's last_attempt moves past since.
func waitForRefresh(t *testing.T, db *gorm.DB, id uint, since time.Time) time.Time {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if last := readRow(t, db, id).LastAttempt; last != nil && last.After(since) {
			return *last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the heartbeat did not refresh the row")
	return time.Time{}
}

// The heartbeat refreshes the claim it was started for, and stops with its attempt.
func TestHeartbeatRefreshesTheRowUntilStopped(t *testing.T) {
	db := openResetWorkerDB(t)
	_, sender := sendingDomain(t, db)
	claimed := time.Now().Add(-time.Hour)
	item := queueTimedRow(t, db, sender.Address, func(row *models.OutboundQueue) {
		row.Status, row.Attempts, row.LastAttempt = "delivering", 3, &claimed
	})
	refreshed := make(chan error, 1)
	if err := db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("test:heartbeat_refreshed", func(tx *gorm.DB) {
		refreshed <- tx.Error
	}); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time, 1)
	w := &Worker{db: db}
	stop := w.startHeartbeatWithTicks(item, ticks)
	t.Cleanup(stop)
	last := *readRow(t, db, item.ID).LastAttempt
	for beat := 0; beat < 2; beat++ {
		ticks <- time.Now()
		select {
		case err := <-refreshed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the heartbeat did not finish refreshing the row")
		}
		current := *readRow(t, db, item.ID).LastAttempt
		if !current.After(last) {
			t.Fatalf("heartbeat %d did not advance last_attempt: %v, then %v", beat+1, last, current)
		}
		last = current
	}
	stop()
	stopped := *readRow(t, db, item.ID).LastAttempt
	ticks <- time.Now()
	if last := *readRow(t, db, item.ID).LastAttempt; !last.Equal(stopped) {
		t.Fatalf("the row was refreshed after its attempt ended: %v, then %v", stopped, last)
	}
	if len(ticks) != 1 {
		t.Fatal("the stopped heartbeat consumed another tick")
	}
}

func TestHeartbeatStopWaitsForInFlightRefresh(t *testing.T) {
	db := openResetWorkerDB(t)
	_, sender := sendingDomain(t, db)
	claimed := time.Date(1999, time.January, 1, 0, 0, 0, 0, time.UTC)
	item := queueTimedRow(t, db, sender.Address, func(row *models.OutboundQueue) {
		row.Status, row.Attempts, row.LastAttempt = "delivering", 3, &claimed
	})
	synctest.Test(t, func(t *testing.T) {
		updating := make(chan context.Context, 1)
		release := make(chan struct{})
		var finish sync.Once
		defer finish.Do(func() { close(release) })
		if err := db.Callback().Update().Before("gorm:begin_transaction").Register("test:heartbeat_updating", func(tx *gorm.DB) {
			updating <- tx.Statement.Context
			<-release
		}); err != nil {
			t.Fatal(err)
		}
		ticks := make(chan time.Time, 1)
		w := &Worker{db: db}
		stop := w.startHeartbeatWithTicks(item, ticks)
		ticks <- time.Now()
		refreshContext := <-updating
		stopped := make(chan struct{})
		go func() {
			stop()
			close(stopped)
		}()
		synctest.Wait()
		select {
		case <-stopped:
			t.Error("stop returned before the in-flight refresh finished")
		default:
		}
		if err := refreshContext.Err(); err != nil {
			t.Errorf("stop canceled the in-flight database transaction: %v", err)
		}
		ticks <- time.Now()
		finish.Do(func() { close(release) })
		<-stopped
		if last := readRow(t, db, item.ID).LastAttempt; last == nil || !last.After(claimed) {
			t.Fatalf("stop did not drain the in-flight refresh: last_attempt = %v", last)
		}
		if len(ticks) != 1 {
			t.Fatal("the stopped heartbeat consumed a pending tick")
		}
	})
}

// A worker that has lost its row must not keep the new holder's row alive, or
// bring back one an administrator moved on.
func TestHeartbeatLeavesARowItNoLongerHolds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		claim  int // the attempt the stale worker holds; the row is at attempt 2
	}{
		{"another worker reclaimed it", "delivering", 1},
		{"it was moved on", "deferred", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openResetWorkerDB(t)
			_, sender := sendingDomain(t, db)
			last := time.Now().Add(-time.Hour)
			item := queueTimedRow(t, db, sender.Address, func(row *models.OutboundQueue) {
				row.Status, row.Attempts, row.LastAttempt = tc.status, 2, &last
			})
			// Compare with what the database stored. It keeps microseconds, and on
			// Linux the clock that set last gives nanoseconds.
			stored := *readRow(t, db, item.ID).LastAttempt
			stale := item
			stale.Attempts = tc.claim
			w := &Worker{db: db, heartbeatEvery: 5 * time.Millisecond}
			stop := w.startHeartbeat(stale)
			time.Sleep(50 * time.Millisecond)
			stop()
			row := readRow(t, db, item.ID)
			if !row.LastAttempt.Equal(stored) {
				t.Fatalf("a row this worker no longer holds was refreshed: %v, then %v", stored, row.LastAttempt)
			}
			if row.Status != tc.status {
				t.Fatalf("status = %s, want %s", row.Status, tc.status)
			}
		})
	}
}

// The heartbeat covers the whole attempt, steps with no SMTP command in them too,
// such as the DNS lookups before the first dial. Here the lookup holds the attempt.
func TestHeartbeatRunsThroughStepsWithoutCommands(t *testing.T) {
	db := openResetWorkerDB(t)
	migrateDeliveryTables(t, db)
	_, sender := sendingDomain(t, db)
	item := queueTimedRow(t, db, sender.Address, nil)
	resolver := &resetDeliveryResolver{claimed: make(chan struct{}), release: make(chan struct{})}
	var finish sync.Once
	t.Cleanup(func() { finish.Do(func() { close(resolver.release) }) })
	w := NewWorker(db, "sender.test", 1, time.Second)
	w.resolver = resolver
	w.heartbeatEvery = 10 * time.Millisecond
	w.SetSuppressionTTL(time.Hour)
	done := make(chan struct{})
	go func() { w.processOne(0); close(done) }()
	select {
	case <-resolver.claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not claim the row")
	}
	waitForRefresh(t, db, item.ID, *readRow(t, db, item.ID).LastAttempt)
	finish.Do(func() { close(resolver.release) })
	<-done
}
