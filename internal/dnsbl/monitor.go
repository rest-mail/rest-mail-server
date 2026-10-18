package dnsbl

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"
)

// MinMonitorInterval is the shortest pause a Monitor accepts between passes.
// Blocklists rate-limit (Spamhaus answers 127.255.255.255 for excessive
// queries), so an interval given in the wrong unit is refused rather than run.
const MinMonitorInterval = time.Minute

// IPChecker is the part of *Checker a Monitor needs.
type IPChecker interface {
	Check(ctx context.Context, ip netip.Addr, p Provider) (Result, error)
}

var _ IPChecker = (*Checker)(nil)

// Observer receives what a Monitor sees. Both methods are called on the
// Monitor's goroutine, between checks, so a slow observer delays the pass.
type Observer interface {
	// Checked is called once for every completed check, whatever its status.
	Checked(Observation)
	// Changed is called once per listing or recovery, after Checked for the
	// check that caused it.
	Changed(Transition)
}

// Observation is one completed check.
type Observation struct {
	At     time.Time
	Result Result
}

// TransitionKind says which way a listing changed.
type TransitionKind string

const (
	// TransitionListed: the IP is listed and was not known to be listed.
	TransitionListed TransitionKind = "listed"
	// TransitionRecovered: the provider authoritatively answered not listed
	// for an IP that was known to be listed.
	TransitionRecovered TransitionKind = "recovered"
)

// Transition is a change in what a provider says about an IP.
type Transition struct {
	Kind TransitionKind
	At   time.Time
	// Result is the check that caused the change: the listing codes and
	// reason for a listing, the clean answer for a recovery.
	Result Result
	// Previous is the result that began the state this one ends: the first
	// clean answer or the first listing. Its Status is empty when the state
	// was unknown (a listing on the first authoritative answer).
	Previous Result
	// Since is when Previous was seen; zero when there was none.
	Since time.Time
}

// MonitorConfig is everything a Monitor runs on. Nothing has a default: the
// caller names every IP and provider and both durations.
type MonitorConfig struct {
	// IPs are the sending addresses to check, each a public unicast address.
	IPs []netip.Addr
	// Providers are the blocklists to check each IP against. Names must be
	// unique: they key the listing state.
	Providers []Provider
	// StartDelay is the wait before the first pass; zero starts it at once.
	StartDelay time.Duration
	// Interval is the pause between the end of one pass and the start of the
	// next, so a slow pass never overlaps or queues the next one.
	Interval time.Duration
	// Timeout bounds each check (one IP against one provider).
	Timeout time.Duration
	// Observer receives every check and every transition.
	Observer Observer
}

// ErrInvalidMonitorConfig wraps every reason NewMonitor refuses a config.
var ErrInvalidMonitorConfig = errors.New("dnsbl: invalid monitor config")

// ErrMonitorRunning is returned by Run while another Run is in progress.
var ErrMonitorRunning = errors.New("dnsbl: monitor is already running")

// Monitor checks every configured IP against every configured provider, one
// check at a time, and reports listings and recoveries.
//
// The listing state (last authoritative listed / not listed result per IP and
// provider) is held in memory only: it is not durable, and a new Monitor or a
// restart starts from unknown. An unknown pair that checks clean is not a
// recovery; one that checks listed is a listing. Only an authoritative
// not_listed recovers a listing: provider_error, indeterminate, unsupported
// and checks abandoned by cancellation never change the state.
type Monitor struct {
	checker    IPChecker
	ips        []netip.Addr
	providers  []Provider
	startDelay time.Duration
	interval   time.Duration
	timeout    time.Duration
	observer   Observer

	now      func() time.Time
	newTimer func(time.Duration) monitorTimer

	running atomic.Bool
	state   map[pairKey]pairState // owned by the Run goroutine
}

type pairKey struct {
	ip       netip.Addr
	provider string
}

type pairState struct {
	result Result // last authoritative (listed or not_listed) result
	since  time.Time
}

// monitorTimer is the part of *time.Timer the loop uses; tests fake it.
type monitorTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

// NewMonitor validates cfg and returns a Monitor that is not yet running. It
// sends no queries; Run does.
func NewMonitor(checker IPChecker, cfg MonitorConfig) (*Monitor, error) {
	if checker == nil {
		return nil, fmt.Errorf("%w: a checker is required", ErrInvalidMonitorConfig)
	}
	if cfg.Observer == nil {
		return nil, fmt.Errorf("%w: an observer is required", ErrInvalidMonitorConfig)
	}
	if cfg.Interval < MinMonitorInterval {
		return nil, fmt.Errorf("%w: interval %s is shorter than %s", ErrInvalidMonitorConfig, cfg.Interval, MinMonitorInterval)
	}
	if cfg.Timeout <= 0 || cfg.Timeout >= cfg.Interval {
		return nil, fmt.Errorf("%w: timeout %s must be positive and shorter than the interval", ErrInvalidMonitorConfig, cfg.Timeout)
	}
	if cfg.StartDelay < 0 {
		return nil, fmt.Errorf("%w: start delay %s is negative", ErrInvalidMonitorConfig, cfg.StartDelay)
	}
	if len(cfg.IPs) == 0 {
		return nil, fmt.Errorf("%w: at least one IP is required", ErrInvalidMonitorConfig)
	}
	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("%w: at least one provider is required", ErrInvalidMonitorConfig)
	}

	ips := make([]netip.Addr, 0, len(cfg.IPs))
	seenIP := make(map[netip.Addr]bool, len(cfg.IPs))
	for _, ip := range cfg.IPs {
		if !ip.IsValid() || ip.Zone() != "" {
			return nil, fmt.Errorf("%w: %w: %q", ErrInvalidMonitorConfig, ErrInvalidIP, ip.String())
		}
		ip = ip.Unmap()
		if !isPublicUnicast(ip) {
			return nil, fmt.Errorf("%w: %w: %s", ErrInvalidMonitorConfig, ErrNotPublicIP, ip)
		}
		if seenIP[ip] {
			return nil, fmt.Errorf("%w: IP %s is listed twice", ErrInvalidMonitorConfig, ip)
		}
		seenIP[ip] = true
		ips = append(ips, ip)
	}

	providers := make([]Provider, 0, len(cfg.Providers))
	seenName := make(map[string]bool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidMonitorConfig, err)
		}
		if seenName[p.Name] {
			return nil, fmt.Errorf("%w: provider name %q is used twice", ErrInvalidMonitorConfig, p.Name)
		}
		seenName[p.Name] = true
		p.Listed = append([]netip.Prefix(nil), p.Listed...)
		p.Errors = append([]netip.Prefix(nil), p.Errors...)
		providers = append(providers, p)
	}

	return &Monitor{
		checker:    checker,
		ips:        ips,
		providers:  providers,
		startDelay: cfg.StartDelay,
		interval:   cfg.Interval,
		timeout:    cfg.Timeout,
		observer:   cfg.Observer,
		now:        time.Now,
		newTimer:   func(d time.Duration) monitorTimer { return realTimer{time.NewTimer(d)} },
		state:      make(map[pairKey]pairState),
	}, nil
}

// Run checks every IP against every provider, StartDelay after it is called
// and then Interval after each pass ends, until ctx is done. Cancelling ctx
// abandons the check in flight and the pending wait; Run then returns
// ctx.Err(). Run starts no goroutines of its own. The listing state survives
// between Runs of the same Monitor, but only one Run may be active at a time.
func (m *Monitor) Run(ctx context.Context) error {
	if !m.running.CompareAndSwap(false, true) {
		return ErrMonitorRunning
	}
	defer m.running.Store(false)

	wait := m.startDelay
	for {
		if wait > 0 {
			t := m.newTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C():
			}
		}
		if err := m.pass(ctx); err != nil {
			return err
		}
		wait = m.interval
	}
}

// pass runs one check per IP and provider, in configuration order.
func (m *Monitor) pass(ctx context.Context) error {
	for _, ip := range m.ips {
		for _, p := range m.providers {
			if err := ctx.Err(); err != nil {
				return err
			}
			res := m.check(ctx, ip, p)
			// A check cut short by cancellation is not evidence of anything,
			// whatever it returned: drop it unobserved and stop.
			if err := ctx.Err(); err != nil {
				return err
			}
			at := m.now()
			m.observer.Checked(Observation{At: at, Result: res})
			if tr, ok := m.apply(pairKey{ip: ip, provider: p.Name}, res, at); ok {
				m.observer.Changed(tr)
			}
		}
	}
	return nil
}

func (m *Monitor) check(ctx context.Context, ip netip.Addr, p Provider) Result {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	res, err := m.checker.Check(ctx, ip, p)
	if err != nil {
		// NewMonitor validated the input, so this is a checker that refused
		// it anyway: report it as indeterminate, never as an answer.
		return Result{
			IP:       ip,
			Provider: p.Name,
			Zone:     strings.TrimSuffix(p.Zone, "."),
			Status:   StatusIndeterminate,
			Detail:   "check refused: " + err.Error(),
		}
	}
	return res
}

// apply records an authoritative result for key and reports the transition
// it makes, if any. Every other status leaves the state alone. The key is
// what was asked, not what the checker echoed back.
func (m *Monitor) apply(key pairKey, res Result, at time.Time) (Transition, bool) {
	if res.Status != StatusListed && res.Status != StatusNotListed {
		return Transition{}, false
	}
	prev, known := m.state[key]
	if known && prev.result.Status == res.Status {
		return Transition{}, false
	}
	m.state[key] = pairState{result: res, since: at}

	switch {
	case res.Status == StatusListed:
		return Transition{Kind: TransitionListed, At: at, Result: res, Previous: prev.result, Since: prev.since}, true
	case known: // not_listed after listed
		return Transition{Kind: TransitionRecovered, At: at, Result: res, Previous: prev.result, Since: prev.since}, true
	default: // the first answer is clean: nothing changed
		return Transition{}, false
	}
}
