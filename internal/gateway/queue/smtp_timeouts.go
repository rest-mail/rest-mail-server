package queue

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

const (
	// smtpDialTimeout bounds opening the TCP connection, which comes before the
	// conversation RFC 5321's timers cover.
	smtpDialTimeout = 10 * time.Second
	// dataBlockSize is how much of a message one block timer covers (RFC 5321
	// §4.5.3.2.5 says "each buffer"; it does not say how large).
	dataBlockSize = 64 << 10
	// defaultHeartbeat is how often the worker holding a row refreshes it. It
	// stays below the reclaim interval's margin, so one missed beat is harmless.
	defaultHeartbeat = time.Minute
	// heartbeatWriteTimeout bounds one refresh, so a stuck database cannot hold up
	// the end of an attempt. stop waits for a refresh still running to finish.
	heartbeatWriteTimeout = 30 * time.Second
)

// smtpAddrOf is the address to dial for an MX host.
func (w *Worker) smtpAddrOf(host string) string {
	if w.smtpAddr != nil {
		return w.smtpAddr(host)
	}
	return net.JoinHostPort(host, "25")
}

// SetSMTPTimeouts sets the timer for each step of an SMTP conversation (#714).
func (w *Worker) SetSMTPTimeouts(t config.OutboundSMTPTimeouts) {
	w.smtpTimeouts = t
}

// timeouts is the worker's timers, a field left unset being its default.
func (w *Worker) timeouts() config.OutboundSMTPTimeouts {
	return w.smtpTimeouts.WithDefaults()
}

// armStep starts the timer for the next step of a conversation. net/smtp sets no
// deadlines of its own. The socket's deadline is absolute, so a peer that trickles
// a reply is dropped at the step's timeout, and it carries across STARTTLS, which
// wraps this same socket.
func armStep(conn net.Conn, d time.Duration) {
	_ = conn.SetDeadline(time.Now().Add(d))
}

// armAfterHandshake gives the EHLO that follows STARTTLS a timer of its own.
// net/smtp's StartTLS does STARTTLS, the handshake and that EHLO in one call, so
// the handshake's end is the only point between them to start it.
func armAfterHandshake(cfg *tls.Config, conn net.Conn, d time.Duration) {
	verify := cfg.VerifyConnection
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if verify != nil {
			if err := verify(cs); err != nil {
				return err
			}
		}
		armStep(conn, d)
		return nil
	}
}

// writeDataBlocks writes a message in blocks, with arm starting a new timer
// before each (RFC 5321 §4.5.3.2.5). flush pushes out what the writer still
// buffers, under the last block's timer, so that the timer for the final reply
// covers only the reply.
func writeDataBlocks(w io.Writer, flush func() error, raw string, arm func()) error {
	for len(raw) > 0 {
		block := raw[:min(dataBlockSize, len(raw))]
		arm()
		if _, err := io.WriteString(w, block); err != nil {
			return err
		}
		raw = raw[len(block):]
	}
	arm()
	return flush()
}

// startHeartbeat refreshes a claimed row's last_attempt until the returned stop
// is called, so a row whose worker is alive is never taken for orphaned, however
// long its attempt runs. Every step of an attempt has its own bound, so the
// attempt ends and the heartbeat with it; a crashed worker's heartbeat dies with
// it, and the row comes back once the reclaim interval has passed.
func (w *Worker) startHeartbeat(item models.OutboundQueue) (stop func()) {
	every := w.heartbeatEvery
	if every <= 0 {
		every = defaultHeartbeat
	}
	ticker := time.NewTicker(every)
	stopHeartbeat := w.startHeartbeatWithTicks(item, ticker.C)
	return func() {
		ticker.Stop()
		stopHeartbeat()
	}
}

func (w *Worker) startHeartbeatWithTicks(item models.OutboundQueue, ticks <-chan time.Time) (stop func()) {
	db := w.heartbeatDB
	if db == nil {
		db = w.db
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				if ctx.Err() != nil {
					return
				}
				if !refreshClaim(context.Background(), db, item) {
					return
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// refreshClaim moves the row's last_attempt to now, and reports whether to keep
// doing so. The claim's attempt number fences it: once another worker has claimed
// the row, or an administrator has moved it on, this worker's heartbeat must not
// keep it alive. A database error is only logged. The next beat tries again, and
// the reclaim margin allows for a missed one.
func refreshClaim(ctx context.Context, db *gorm.DB, item models.OutboundQueue) bool {
	ctx, cancel := context.WithTimeout(ctx, heartbeatWriteTimeout)
	defer cancel()
	result := db.WithContext(ctx).Model(&models.OutboundQueue{}).
		Where("id = ? AND status = ? AND attempts = ?", item.ID, "delivering", item.Attempts).
		UpdateColumn("last_attempt", time.Now())
	if err := result.Error; err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("queue: heartbeat failed", "id", item.ID, "error", err)
		}
		return true
	}
	if result.RowsAffected == 0 {
		slog.Warn("queue: this attempt no longer holds its row, heartbeat stopped", "id", item.ID, "attempt", item.Attempts)
		return false
	}
	return true
}
