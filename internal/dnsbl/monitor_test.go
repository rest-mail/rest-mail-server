package dnsbl

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// No test in this file touches the network or waits on real time for the
// schedule: the clock is fake and the checker is scripted (or a real Checker
// over fakeResolver). Real time is only a safety deadline.

const testWait = 5 * time.Second

// fakeClock hands every timer the Monitor arms to the test, which fires it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers chan *fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	c       chan time.Time
	stopped atomic.Bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }
func (t *fakeTimer) Stop() bool          { return !t.stopped.Swap(true) }

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC), timers: make(chan *fakeTimer, 16)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) newTimer(d time.Duration) monitorTimer {
	t := &fakeTimer{d: d, c: make(chan time.Time, 1)}
	c.timers <- t
	return t
}

// nextTimer waits until the Monitor arms a timer, which it does only between
// passes, so everything the previous pass observed is visible afterwards.
func (c *fakeClock) nextTimer(t *testing.T) *fakeTimer {
	t.Helper()
	select {
	case tm := <-c.timers:
		return tm
	case <-time.After(testWait):
		t.Fatal("monitor armed no timer")
		return nil
	}
}

func (c *fakeClock) fire(tm *fakeTimer) {
	c.mu.Lock()
	c.now = c.now.Add(tm.d)
	now := c.now
	c.mu.Unlock()
	tm.c <- now
}

// checkFunc adapts a function to IPChecker.
type checkFunc func(ctx context.Context, ip netip.Addr, p Provider) (Result, error)

func (f checkFunc) Check(ctx context.Context, ip netip.Addr, p Provider) (Result, error) {
	return f(ctx, ip, p)
}

// scripted answers each check with the next status set by the test.
type scripted struct {
	mu     sync.Mutex
	status Status
	calls  []string
}

func (s *scripted) set(st Status) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}

func (s *scripted) Check(_ context.Context, ip netip.Addr, p Provider) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, ip.String()+" "+p.Name)
	res := Result{IP: ip, Provider: p.Name, Zone: p.Zone, Status: s.status}
	if s.status == StatusListed {
		res.Codes = addrs("127.0.0.2")
		res.Reason = "listed for testing"
	}
	return res, nil
}

type recorder struct {
	mu      sync.Mutex
	checks  []Observation
	changes []Transition
}

func (r *recorder) Checked(o Observation) {
	r.mu.Lock()
	r.checks = append(r.checks, o)
	r.mu.Unlock()
}

func (r *recorder) Changed(tr Transition) error {
	r.mu.Lock()
	r.changes = append(r.changes, tr)
	r.mu.Unlock()
	return nil
}

func (r *recorder) snapshot() ([]Observation, []Transition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Observation(nil), r.checks...), append([]Transition(nil), r.changes...)
}

func otherProvider() Provider {
	return Provider{Name: "other", Zone: "bl.other.example", Listed: []netip.Prefix{netip.MustParsePrefix("127.0.0.2/32")}}
}

func testConfig(obs Observer, ips ...string) MonitorConfig {
	cfg := MonitorConfig{
		Providers: []Provider{spamhausLike()},
		Interval:  time.Hour,
		Timeout:   10 * time.Second,
		Observer:  obs,
	}
	for _, ip := range ips {
		cfg.IPs = append(cfg.IPs, netip.MustParseAddr(ip))
	}
	return cfg
}

func newTestMonitor(t *testing.T, c IPChecker, cfg MonitorConfig) (*Monitor, *fakeClock) {
	t.Helper()
	m, err := NewMonitor(c, cfg)
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	clock := newFakeClock()
	m.now = clock.Now
	m.newTimer = clock.newTimer
	return m, clock
}

// start runs m until the test ends and fails the test if Run does not return
// promptly once cancelled.
func start(t *testing.T, m *Monitor) (cancel func(), done <-chan error) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- m.Run(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancelCtx()
		select {
		case err := <-ch:
			ch <- err
		case <-time.After(testWait):
			t.Fatal("Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return stop, ch
}

func TestNewMonitorRejectsMalformedConfig(t *testing.T) {
	never := checkFunc(func(context.Context, netip.Addr, Provider) (Result, error) {
		t.Error("NewMonitor sent a check")
		return Result{}, nil
	})
	rec := &recorder{}
	bad := map[string]func(*MonitorConfig){
		"no observer":         func(c *MonitorConfig) { c.Observer = nil },
		"zero interval":       func(c *MonitorConfig) { c.Interval = 0 },
		"interval too short":  func(c *MonitorConfig) { c.Interval = time.Second },
		"zero timeout":        func(c *MonitorConfig) { c.Timeout = 0 },
		"negative timeout":    func(c *MonitorConfig) { c.Timeout = -time.Second },
		"timeout >= interval": func(c *MonitorConfig) { c.Timeout = c.Interval },
		"negative delay":      func(c *MonitorConfig) { c.StartDelay = -time.Second },
		"no IPs":              func(c *MonitorConfig) { c.IPs = nil },
		"zero IP":             func(c *MonitorConfig) { c.IPs = []netip.Addr{{}} },
		"private IP":          func(c *MonitorConfig) { c.IPs = addrs("10.0.0.1") },
		"loopback IP":         func(c *MonitorConfig) { c.IPs = addrs("127.0.0.1") },
		"zoned IP":            func(c *MonitorConfig) { c.IPs = addrs("2001:db8::1%eth0") },
		"duplicate IP":        func(c *MonitorConfig) { c.IPs = addrs(sendingIP, "::ffff:"+sendingIP) },
		"no providers":        func(c *MonitorConfig) { c.Providers = nil },
		"invalid provider":    func(c *MonitorConfig) { c.Providers = []Provider{{Name: "x", Zone: "bl.example"}} },
		"duplicate provider": func(c *MonitorConfig) {
			p := otherProvider()
			p.Name = spamhausLike().Name
			c.Providers = append(c.Providers, p)
		},
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(rec, sendingIP)
			mutate(&cfg)
			if _, err := NewMonitor(never, cfg); !errors.Is(err, ErrInvalidMonitorConfig) {
				t.Fatalf("NewMonitor = %v, want ErrInvalidMonitorConfig", err)
			}
		})
	}
	if _, err := NewMonitor(nil, testConfig(rec, sendingIP)); !errors.Is(err, ErrInvalidMonitorConfig) {
		t.Fatalf("nil checker: %v", err)
	}
	if _, err := NewMonitor(never, testConfig(rec, sendingIP)); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestMonitorChecksEveryPairOnSchedule(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var deadlineErr error
	c := checkFunc(func(ctx context.Context, ip netip.Addr, p Provider) (Result, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, ip.String()+" "+p.Name)
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > 10*time.Second {
			deadlineErr = errors.New("check context is not bounded by the per-check timeout")
		}
		return Result{IP: ip, Provider: p.Name, Status: StatusNotListed}, nil
	})
	rec := &recorder{}
	cfg := testConfig(rec, sendingIP, "2001:db8::25")
	cfg.Providers = append(cfg.Providers, otherProvider())
	cfg.StartDelay = 5 * time.Minute
	m, clock := newTestMonitor(t, c, cfg)
	start(t, m)

	first := clock.nextTimer(t)
	if first.d != 5*time.Minute {
		t.Fatalf("first wait = %s, want the start delay", first.d)
	}
	mu.Lock()
	if len(calls) != 0 {
		t.Fatalf("checks before the start delay: %v", calls)
	}
	mu.Unlock()

	want := []string{
		sendingIP + " zen-like", sendingIP + " other",
		"2001:db8::25 zen-like", "2001:db8::25 other",
	}
	for pass := 1; pass <= 3; pass++ {
		clock.fire(first)
		first = clock.nextTimer(t)
		if first.d != time.Hour {
			t.Fatalf("pass %d: next wait = %s, want the interval", pass, first.d)
		}
		mu.Lock()
		got := append([]string(nil), calls...)
		mu.Unlock()
		if len(got) != pass*len(want) || strings.Join(got[(pass-1)*4:], "|") != strings.Join(want, "|") {
			t.Fatalf("pass %d checks = %v, want %v once more", pass, got, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	checks, changes := rec.snapshot()
	if len(checks) != 12 || len(changes) != 0 {
		t.Fatalf("got %d checks and %d transitions, want 12 and none (clean from unknown is not a recovery)", len(checks), len(changes))
	}
}

// TestMonitorTransitions walks one IP/provider pair through a pass per step.
func TestMonitorTransitions(t *testing.T) {
	steps := []struct {
		status Status
		want   TransitionKind // "" for no transition
	}{
		{StatusIndeterminate, ""}, // unknown stays unknown
		{StatusNotListed, ""},     // first clean is not a recovery
		{StatusNotListed, ""},     // still clean
		{StatusListed, TransitionListed},
		{StatusListed, ""},        // still listed: one transition per change
		{StatusProviderError, ""}, // says nothing about the IP
		{StatusIndeterminate, ""},
		{StatusUnsupported, ""},
		{StatusListed, ""}, // the listing was never cleared
		{StatusNotListed, TransitionRecovered},
		{StatusNotListed, ""},
		{StatusListed, TransitionListed}, // relisted
	}
	sc := &scripted{}
	rec := &recorder{}
	m, clock := newTestMonitor(t, sc, testConfig(rec, sendingIP))

	sc.set(steps[0].status)
	start(t, m)
	tm := clock.nextTimer(t)
	for i, step := range steps {
		if i > 0 {
			sc.set(step.status)
			clock.fire(tm)
			tm = clock.nextTimer(t)
		}
		checks, changes := rec.snapshot()
		if len(checks) != i+1 || checks[i].Result.Status != step.status {
			t.Fatalf("step %d: %d checks observed, last %+v; want every check observed", i, len(checks), checks[len(checks)-1])
		}
		var got TransitionKind
		if n := len(changes); n > 0 && changes[n-1].Result.Status == step.status && changes[n-1].At.Equal(checks[i].At) {
			got = changes[n-1].Kind
		}
		if got != step.want {
			t.Fatalf("step %d (%s): transition %q, want %q; all: %+v", i, step.status, got, step.want, changes)
		}
	}

	_, changes := rec.snapshot()
	if len(changes) != 3 {
		t.Fatalf("transitions = %+v, want exactly 3", changes)
	}
	listing, recovery := changes[0], changes[1]
	if listing.Result.Reason != "listed for testing" || len(listing.Result.Codes) != 1 || listing.Result.Codes[0] != netip.MustParseAddr("127.0.0.2") {
		t.Errorf("listing evidence = %+v, want codes and reason", listing.Result)
	}
	if listing.Previous.Status != StatusNotListed || listing.Since.IsZero() {
		t.Errorf("listing previous = %+v since %s, want the clean answer it replaced", listing.Previous, listing.Since)
	}
	if recovery.Previous.Status != StatusListed || len(recovery.Previous.Codes) != 1 || !recovery.Since.Equal(listing.At) {
		t.Errorf("recovery = %+v, want the listing it ended and when it began", recovery)
	}
	if recovery.Result.IP != netip.MustParseAddr(sendingIP) || recovery.Result.Provider != "zen-like" {
		t.Errorf("recovery result = %+v, want the pair", recovery.Result)
	}
}

func TestMonitorFirstListedIsAListing(t *testing.T) {
	sc := &scripted{status: StatusListed}
	rec := &recorder{}
	m, clock := newTestMonitor(t, sc, testConfig(rec, sendingIP))
	start(t, m)
	clock.nextTimer(t)
	_, changes := rec.snapshot()
	if len(changes) != 1 || changes[0].Kind != TransitionListed || changes[0].Previous.Status != "" || !changes[0].Since.IsZero() {
		t.Fatalf("transitions = %+v, want one listing with no previous state", changes)
	}
}

func TestMonitorRefusedCheckIsIndeterminate(t *testing.T) {
	c := checkFunc(func(context.Context, netip.Addr, Provider) (Result, error) {
		return Result{}, ErrInvalidProvider
	})
	rec := &recorder{}
	m, clock := newTestMonitor(t, c, testConfig(rec, sendingIP))
	start(t, m)
	clock.nextTimer(t)
	checks, changes := rec.snapshot()
	if len(checks) != 1 || checks[0].Result.Status != StatusIndeterminate || checks[0].Result.IP != netip.MustParseAddr(sendingIP) ||
		!strings.Contains(checks[0].Result.Detail, "invalid provider") || len(changes) != 0 {
		t.Fatalf("checks = %+v, transitions = %+v; want one indeterminate check and no transition", checks, changes)
	}
}

// TestMonitorNeverOverlaps holds a check open and shows that nothing else
// runs, and no next pass is scheduled, until it finishes.
func TestMonitorNeverOverlaps(t *testing.T) {
	var inFlight, maxInFlight, calls atomic.Int32
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	c := checkFunc(func(ctx context.Context, ip netip.Addr, p Provider) (Result, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		if n > maxInFlight.Load() {
			maxInFlight.Store(n)
		}
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return Result{IP: ip, Provider: p.Name, Status: StatusNotListed}, nil
	})
	rec := &recorder{}
	cfg := testConfig(rec, sendingIP, "198.51.100.7")
	m, clock := newTestMonitor(t, c, cfg)
	start(t, m)

	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(testWait):
			t.Fatal("check did not start")
		}
		select {
		case tm := <-clock.timers:
			t.Fatalf("timer for %s armed while a check was still running", tm.d)
		case <-entered:
			t.Fatal("a second check started while the first was running")
		case <-time.After(50 * time.Millisecond):
		}
		release <- struct{}{}
	}
	if tm := clock.nextTimer(t); tm.d != time.Hour {
		t.Fatalf("next wait = %s, want the interval after the pass", tm.d)
	}
	if maxInFlight.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("max in flight = %d, calls = %d; want 1 and 2", maxInFlight.Load(), calls.Load())
	}
}

// TestMonitorCancelDuringCheck cancels while a check is in flight. The check
// lies "not listed" after cancellation; the listing must survive it.
func TestMonitorCancelDuringCheck(t *testing.T) {
	var listed atomic.Bool
	listed.Store(true)
	entered := make(chan struct{}, 1)
	c := checkFunc(func(ctx context.Context, ip netip.Addr, p Provider) (Result, error) {
		res := Result{IP: ip, Provider: p.Name, Status: StatusListed, Codes: addrs("127.0.0.2")}
		if listed.Load() {
			return res, nil
		}
		entered <- struct{}{}
		<-ctx.Done()
		res.Status, res.Codes = StatusNotListed, nil
		return res, nil
	})
	rec := &recorder{}
	m, clock := newTestMonitor(t, c, testConfig(rec, sendingIP))
	cancel, done := start(t, m)
	tm := clock.nextTimer(t)

	listed.Store(false)
	clock.fire(tm)
	select {
	case <-entered:
	case <-time.After(testWait):
		t.Fatal("second pass did not start")
	}
	began := time.Now()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if time.Since(began) > time.Second {
		t.Fatalf("Run took %s to return after cancel", time.Since(began))
	}
	select {
	case tm := <-clock.timers:
		t.Fatalf("timer for %s armed after cancel", tm.d)
	default:
	}
	checks, changes := rec.snapshot()
	if len(checks) != 1 || len(changes) != 1 || changes[0].Kind != TransitionListed {
		t.Fatalf("checks = %+v, transitions = %+v; want the cancelled check dropped and no recovery", checks, changes)
	}

	// The same Monitor run again still knows the pair is listed.
	listed.Store(true)
	start(t, m)
	clock.nextTimer(t)
	if _, changes := rec.snapshot(); len(changes) != 1 {
		t.Fatalf("transitions after restart = %+v, want the listing not repeated", changes)
	}
}

func TestMonitorCancelDuringWait(t *testing.T) {
	sc := &scripted{status: StatusNotListed}
	rec := &recorder{}
	cfg := testConfig(rec, sendingIP)
	cfg.StartDelay = time.Minute
	m, clock := newTestMonitor(t, sc, cfg)
	cancel, done := start(t, m)
	tm := clock.nextTimer(t)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if !tm.stopped.Load() {
		t.Error("pending timer was not stopped")
	}
	if checks, _ := rec.snapshot(); len(checks) != 0 {
		t.Fatalf("checks = %+v, want none before the start delay", checks)
	}
}

func TestMonitorRunIsExclusive(t *testing.T) {
	sc := &scripted{status: StatusNotListed}
	m, clock := newTestMonitor(t, sc, testConfig(&recorder{}, sendingIP))
	start(t, m)
	clock.nextTimer(t)
	if err := m.Run(context.Background()); !errors.Is(err, ErrMonitorRunning) {
		t.Fatalf("second Run = %v, want ErrMonitorRunning", err)
	}
}

// TestMonitorWithChecker runs the real Checker over fakeResolver.
func TestMonitorWithChecker(t *testing.T) {
	r := &fakeResolver{
		a:   map[string][]netip.Addr{query4: addrs("127.0.0.2")},
		txt: map[string][]string{query4: {"https://dnsbl.example/why"}},
	}
	c, err := NewChecker(r, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	m, clock := newTestMonitor(t, c, testConfig(rec, sendingIP))
	start(t, m)
	tm := clock.nextTimer(t)

	r.mu.Lock()
	r.a = nil
	r.aErr = map[string]error{query4: servfail(query4)}
	r.mu.Unlock()
	clock.fire(tm)
	tm = clock.nextTimer(t)

	r.mu.Lock()
	r.aErr = map[string]error{query4: nxdomain(query4)}
	r.mu.Unlock()
	clock.fire(tm)
	clock.nextTimer(t)

	checks, changes := rec.snapshot()
	var statuses []string
	for _, o := range checks {
		statuses = append(statuses, string(o.Result.Status))
	}
	if strings.Join(statuses, ",") != "listed,indeterminate,not_listed" {
		t.Fatalf("statuses = %v", statuses)
	}
	if len(changes) != 2 || changes[0].Kind != TransitionListed || changes[0].Result.Reason != "https://dnsbl.example/why" ||
		changes[1].Kind != TransitionRecovered || changes[1].Previous.Query != query4 {
		t.Fatalf("transitions = %+v, want a listing with its reason, then a recovery", changes)
	}
}
