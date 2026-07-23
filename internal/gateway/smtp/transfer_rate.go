package smtp

import (
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Anti-slowloris defaults, mirrored by internal/config (SMTP_MIN_TRANSFER_RATE,
// SMTP_TRANSFER_GRACE_PERIOD, SMTP_TRANSFER_STALL_TIMEOUT). They apply when the
// deployment does not configure a policy.
const (
	defaultMinTransferRate      int64 = 16 * 1024 // bytes/sec average floor
	defaultTransferGracePeriod        = 60 * time.Second
	defaultTransferStallTimeout       = 300 * time.Second
)

// errTransferRatePolicy is what reads return once the policy dropped the
// connection; go-smtp treats it like any other fatal conn error.
var errTransferRatePolicy = errors.New("smtp: connection dropped: message transfer below minimum-rate policy")

// transferRatePolicy is the anti-slowloris enforcement policy for message-body
// transfers. A client can declare a large message and trickle it a few bytes
// at a time, tying up a connection near-forever; fixed whole-transfer windows
// cannot distinguish that from a legitimate large transfer. Instead the policy
// demands that data actually flows:
//
//   - grace: no enforcement for this long after transfer start, so slow-start
//     senders and slow TLS links are unaffected.
//   - stall: zero bytes for this long → connection dropped.
//   - minRate: after grace, cumulative_bytes/elapsed below this average →
//     connection dropped. This is what kills tricklers, which survive stall
//     timeouts by sending a byte occasionally. 0 disables the floor.
//
// Under the floor a legitimate transfer's allowed time scales with its size
// automatically (e.g. 128 MB gets ~2.3 h worst-case at 16 KiB/s).
type transferRatePolicy struct {
	minRate int64 // bytes/sec; 0 disables the average-rate floor
	grace   time.Duration
	stall   time.Duration
}

// defaultTransferRatePolicy returns the compiled-in policy used when the
// deployment does not configure one.
func defaultTransferRatePolicy() transferRatePolicy {
	return transferRatePolicy{
		minRate: defaultMinTransferRate,
		grace:   defaultTransferGracePeriod,
		stall:   defaultTransferStallTimeout,
	}
}

// transferRateConn wraps every accepted connection (under any TLS layer) and
// enforces transferRatePolicy — but only while armed. The session arms it when
// a message-body transfer begins (DATA/BDAT) and disarms it when the transfer
// ends; between commands clients legitimately idle and go-smtp's per-command
// ReadTimeout already bounds command silence.
//
// While armed the wrapper owns the read deadline: each Read blocks at most
// `stall` (stall detection), and each completed Read checks the cumulative
// average rate against the floor once the grace period has passed. Deadlines
// requested by go-smtp while armed are recorded and honored on disarm; while
// disarmed every call passes through untouched.
//
// On violation the connection is closed and the drop is logged. A graceful 421
// is not possible at this layer — that is how MTAs shed abusive connections.
type transferRateConn struct {
	net.Conn
	policy transferRatePolicy

	mu       sync.Mutex
	armed    bool
	violated bool
	start    time.Time // transfer start (arm time)
	bytes    int64     // cumulative body bytes since arm
	// outerDeadline is the read deadline last requested from outside the
	// wrapper (go-smtp). Swallowed while armed, restored on disarm so
	// post-transfer reads behave exactly as without enforcement.
	outerDeadline time.Time
}

func newTransferRateConn(conn net.Conn, policy transferRatePolicy) *transferRateConn {
	return &transferRateConn{Conn: conn, policy: policy}
}

// arm starts enforcement for a message-body transfer. The stall deadline is
// applied immediately so a read already blocked on the connection (BDAT chunk
// bytes are read on go-smtp's command goroutine) comes under the policy too.
func (c *transferRateConn) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = true
	c.start = time.Now()
	c.bytes = 0
	_ = c.Conn.SetReadDeadline(c.start.Add(c.policy.stall))
}

// disarm ends enforcement and restores the last deadline go-smtp asked for.
func (c *transferRateConn) disarm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.armed {
		return
	}
	c.armed = false
	_ = c.Conn.SetReadDeadline(c.outerDeadline)
}

func (c *transferRateConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	if c.violated {
		c.mu.Unlock()
		return 0, errTransferRatePolicy
	}
	if !c.armed {
		c.mu.Unlock()
		return c.Conn.Read(b)
	}
	// Armed: block at most `stall` waiting for the next bytes. The deadline is
	// refreshed on every read, so any arriving data resets the stall window.
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.policy.stall))
	c.mu.Unlock()

	n, err := c.Conn.Read(b)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytes += int64(n)
	if !c.armed {
		// Disarmed while the read was in flight; the policy no longer applies.
		return n, err
	}
	elapsed := time.Since(c.start)
	if err != nil {
		var nerr net.Error
		if errors.As(err, &nerr) && nerr.Timeout() {
			// Only our own deadline can fire while armed: zero bytes arrived
			// for the whole stall window.
			c.violateLocked("stall", elapsed)
			return n, errTransferRatePolicy
		}
		return n, err
	}
	// Average-rate floor, checked as bytes arrive once the grace period has
	// passed. Cumulative average, not instantaneous: early fast bursts buy
	// slack, steady trickling does not.
	if c.policy.minRate > 0 && elapsed > c.policy.grace &&
		float64(c.bytes) < float64(c.policy.minRate)*elapsed.Seconds() {
		c.violateLocked("below-minimum-rate", elapsed)
		return n, errTransferRatePolicy
	}
	return n, err
}

// violateLocked (mu held) marks the connection dead, logs the drop, and closes
// the underlying connection — releasing the limiter slot via limitedConn.
func (c *transferRateConn) violateLocked(reason string, elapsed time.Duration) {
	c.violated = true
	slog.Warn("smtp: dropping connection: message transfer violates minimum-rate policy",
		"remote", c.Conn.RemoteAddr().String(),
		"reason", reason,
		"bytes", c.bytes,
		"elapsed", elapsed.Round(time.Millisecond).String(),
		"min_rate_bytes_per_sec", c.policy.minRate,
		"grace_period", c.policy.grace.String(),
		"stall_timeout", c.policy.stall.String(),
	)
	_ = c.Conn.Close()
}

// SetReadDeadline records go-smtp's wish; while armed the wrapper's own
// deadlines win and the recorded value is applied on disarm.
func (c *transferRateConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outerDeadline = t
	if c.armed {
		return nil
	}
	return c.Conn.SetReadDeadline(t)
}

// SetDeadline splits into its read and write halves so the read half gets the
// armed handling above; write deadlines always pass through (the wrapper never
// manages writes).
func (c *transferRateConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outerDeadline = t
	if c.armed {
		return c.Conn.SetWriteDeadline(t)
	}
	return c.Conn.SetDeadline(t)
}
