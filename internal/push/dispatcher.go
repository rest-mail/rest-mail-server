package push

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// DefaultCoalesceWindow is the least time between two pushes to one device
// for one account. Mail arriving inside it is told in one push at its end,
// with the unread count then. APNs throttles background pushes anyway.
const DefaultCoalesceWindow = 5 * time.Second

// DefaultSweepInterval is how often the dispatcher deletes the devices of
// sessions that have ended.
const DefaultSweepInterval = time.Minute

// Sender sends one push. *APNs, *WebPush and Senders are each one.
type Sender interface {
	Send(ctx context.Context, n Notification) (Result, error)
}

// Senders sends each push through the sender of its platform.
type Senders map[string]Sender

// Send sends n through the sender of n.Platform.
func (s Senders) Send(ctx context.Context, n Notification) (Result, error) {
	sender, ok := s[n.Platform]
	if !ok {
		return Result{}, fmt.Errorf("no sender for push platform %q", n.Platform)
	}
	return sender.Send(ctx, n)
}

// pushKey is what pushes are coalesced by: a device, and an account its
// session lists.
type pushKey struct {
	device  uint
	account uint
}

// pending is a push waiting for its device and account's window to pass.
type pending struct {
	mailbox uint
	at      time.Time
}

// deviceStore is what the dispatcher needs of the devices: *Store, or a fake
// in a test of the worker itself.
type deviceStore interface {
	targets(mailboxID uint, now time.Time) ([]target, error)
	unread(mailboxID uint) (int64, error)
	RemoveGone(id uint, platform, reason string, now time.Time) error
	Sweep(now time.Time) (int, error)
}

// Dispatcher turns new mail into pushes. NewMail only notes the mailbox; a
// worker of its own finds the devices, sends, and coalesces, so delivering
// mail never waits on APNs.
type Dispatcher struct {
	store  deviceStore
	sender Sender
	window time.Duration
	sweep  time.Duration

	mu      sync.Mutex
	arrived map[uint]struct{}
	started bool

	// The worker's own state: only Step touches it.
	waiting map[pushKey]pending
	last    map[pushKey]time.Time

	wake      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewDispatcher sends through sender to the devices in store, at most one
// push per device and account every window (DefaultCoalesceWindow when not
// positive).
func NewDispatcher(store *Store, sender Sender, window time.Duration) *Dispatcher {
	return newDispatcher(store, sender, window)
}

func newDispatcher(store deviceStore, sender Sender, window time.Duration) *Dispatcher {
	if window <= 0 {
		window = DefaultCoalesceWindow
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		store:   store,
		sender:  sender,
		window:  window,
		sweep:   DefaultSweepInterval,
		arrived: map[uint]struct{}{},
		waiting: map[pushKey]pending{},
		last:    map[pushKey]time.Time{},
		wake:    make(chan struct{}, 1),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
}

// NewMail notes that a message reached the inbox of mailboxID. It never
// blocks: mail arriving before the worker gets to it is one entry.
func (d *Dispatcher) NewMail(mailboxID uint) {
	d.mu.Lock()
	d.arrived[mailboxID] = struct{}{}
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Start runs the worker until Close.
func (d *Dispatcher) Start() {
	d.startOnce.Do(func() {
		d.mu.Lock()
		d.started = true
		d.mu.Unlock()
		go d.run()
	})
}

// Close stops the worker, abandoning a push in flight, and waits for it.
func (d *Dispatcher) Close() {
	d.stopOnce.Do(func() {
		d.cancel()
		d.mu.Lock()
		started := d.started
		d.mu.Unlock()
		if started {
			<-d.done
		}
	})
}

func (d *Dispatcher) run() {
	defer close(d.done)

	timer := time.NewTimer(0)
	defer timer.Stop()
	quiet := time.NewTicker(DefaultCoalesceWindow)
	defer quiet.Stop()
	sweep := time.NewTicker(d.sweep)
	defer sweep.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.wake:
		case <-timer.C:
		case <-quiet.C:
		case <-sweep.C:
			if n, err := d.store.Sweep(time.Now()); err != nil {
				slog.Warn("push: failed to remove the devices of ended sessions", "error", err)
			} else if n > 0 {
				slog.Info("push: removed the devices of ended sessions", "count", n)
			}
		}
		next := d.Step(d.ctx, time.Now())
		timer.Stop()
		if !next.IsZero() {
			timer.Reset(time.Until(next))
		}
	}
}

// Step is one turn of the worker at now: it finds the devices for the mail
// that has arrived, sends every push whose window has passed, and answers
// when the next waiting push is due (zero for none). The worker calls it; a
// test may instead, with a clock of its own, but never both at once.
func (d *Dispatcher) Step(ctx context.Context, now time.Time) time.Time {
	d.mu.Lock()
	arrived := d.arrived
	d.arrived = map[uint]struct{}{}
	d.mu.Unlock()

	// The devices each mailbox goes to, looked up once per step: they are
	// looked up again just before a push, so a device whose session ended
	// while its push waited gets none.
	found := map[uint][]target{}
	for mailbox := range arrived {
		if d.hold(mailbox, now) {
			continue
		}
		targets, err := d.store.targets(mailbox, now)
		if err != nil {
			slog.Warn("push: failed to find the devices for new mail", "mailbox_id", mailbox, "error", err)
			continue
		}
		found[mailbox] = targets
		for _, t := range targets {
			k := pushKey{t.DeviceID, t.Account}
			if _, ok := d.waiting[k]; ok {
				continue // its push will carry the unread count then
			}
			at := now
			if sent, ok := d.last[k]; ok && now.Sub(sent) < d.window {
				at = sent.Add(d.window)
			}
			if at.After(now) && d.hold(mailbox, at) {
				continue
			}
			d.waiting[k] = pending{mailbox: mailbox, at: at}
		}
	}

	var next time.Time
	due := map[uint]map[pushKey]bool{}
	for k, p := range d.waiting {
		if p.at.After(now) {
			if next.IsZero() || p.at.Before(next) {
				next = p.at
			}
			continue
		}
		if due[p.mailbox] == nil {
			due[p.mailbox] = map[pushKey]bool{}
		}
		due[p.mailbox][k] = true
		delete(d.waiting, k)
	}

	for mailbox, keys := range due {
		if d.hold(mailbox, now) {
			continue
		}
		targets, ok := found[mailbox]
		if !ok {
			var err error
			if targets, err = d.store.targets(mailbox, now); err != nil {
				slog.Warn("push: failed to find the devices for new mail", "mailbox_id", mailbox, "error", err)
				continue
			}
		}
		unread, err := d.store.unread(mailbox)
		if err != nil {
			slog.Warn("push: failed to count unread mail", "mailbox_id", mailbox, "error", err)
			continue
		}
		for _, t := range targets {
			k := pushKey{t.DeviceID, t.Account}
			if !keys[k] {
				continue
			}
			d.last[k] = now
			d.send(ctx, t, unread, now)
		}
	}

	// A push sent a window ago no longer holds the next one back.
	for k, sent := range d.last {
		if now.Sub(sent) >= d.window {
			delete(d.last, k)
		}
	}
	if store, ok := d.store.(*Store); ok {
		quietNext, err := store.dispatchQuiet(ctx, now, func(ctx context.Context, device target, unread, arrived int64) {
			payload := Payload(strconv.FormatUint(uint64(device.DeviceID), 10), device.Account, unread)
			if arrived > 0 {
				payload = SummaryPayload(strconv.FormatUint(uint64(device.DeviceID), 10), device.Account, unread, arrived)
			}
			d.sendPayload(ctx, device, payload, now)
		})
		if err != nil {
			slog.Warn("push: could not dispatch quiet-hours summaries", "error", err)
		}
		if !quietNext.IsZero() && (next.IsZero() || quietNext.Before(next)) {
			next = quietNext
		}
	}
	return next
}

func (d *Dispatcher) hold(mailbox uint, now time.Time) bool {
	if store, ok := d.store.(*Store); ok {
		held, err := store.hold(mailbox, now)
		if err != nil {
			slog.Warn("push: could not read quiet hours", "mailbox_id", mailbox, "error", err)
			return true
		}
		return held
	}
	return false
}

// send pushes one signal, and deletes the device when APNs says its token is
// gone.
func (d *Dispatcher) send(ctx context.Context, t target, unread int64, now time.Time) {
	device := strconv.FormatUint(uint64(t.DeviceID), 10)
	d.sendPayload(ctx, t, Payload(device, t.Account, unread), now)
}

func (d *Dispatcher) sendPayload(ctx context.Context, t target, payload []byte, now time.Time) {
	res, err := d.sender.Send(ctx, Notification{
		Platform:    t.Platform,
		Token:       t.Token,
		Environment: t.Environment,
		Payload:     payload,
		P256dh:      t.P256dh,
		Auth:        t.Auth,
	})
	if err != nil {
		slog.Warn("push: push service not reached", "device_id", t.DeviceID, "platform", t.Platform, "error", err)
		return
	}
	switch {
	case res.Status == 200:
	case res.Gone():
		if err := d.store.RemoveGone(t.DeviceID, t.Platform, res.Reason, now); err != nil {
			slog.Warn("push: failed to remove a device its push service refused", "device_id", t.DeviceID, "error", err)
		}
	default:
		slog.Warn("push: push service refused a push", "device_id", t.DeviceID, "platform", t.Platform, "status", res.Status, "reason", res.Reason)
	}
}
