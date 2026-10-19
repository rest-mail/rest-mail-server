package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dnsbl"
	"github.com/restmail/restmail/internal/metrics"
)

const dnsblTestConfig = `{
  "enabled": true,
  "ips": ["192.0.2.99"],
  "start_delay": "0s",
  "interval": "1h",
  "timeout": "2s",
  "providers": [{"name": "example", "zone": "bl.example.org", "listed": ["127.0.0.2"]}]
}`

// dnsblResolver answers every A query as listed, or blocks until its context
// is done; it counts what it is asked. Nothing reaches the network.
type dnsblResolver struct {
	mu      sync.Mutex
	queries int
	block   bool
	clean   bool
	asked   chan struct{}
}

func newDNSBLResolver(block bool) *dnsblResolver {
	return &dnsblResolver{block: block, asked: make(chan struct{}, 16)}
}

func (r *dnsblResolver) LookupNetIP(ctx context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	r.queries++
	r.mu.Unlock()
	r.asked <- struct{}{}
	if r.block {
		<-ctx.Done()
		return nil, &net.DNSError{Err: ctx.Err().Error(), Name: host}
	}
	if r.clean {
		return nil, &net.DNSError{Err: "not found", Name: host, IsNotFound: true}
	}
	return []netip.Addr{netip.MustParseAddr("127.0.0.2")}, nil
}

func (r *dnsblResolver) LookupTXT(context.Context, string) ([]string, error) {
	return []string{"listed for testing"}, nil
}

func (r *dnsblResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queries
}

type dnsblTestMetrics struct {
	checks             *prometheus.CounterVec
	listed, lastAnswer *prometheus.GaugeVec
}

func newDNSBLTestObserver(t *testing.T) (dnsbl.Observer, dnsblTestMetrics) {
	t.Helper()
	m := dnsblTestMetrics{metrics.NewDNSBLChecks(), metrics.NewDNSBLListed(), metrics.NewDNSBLLastAnswer()}
	prometheus.NewPedanticRegistry().MustRegister(m.checks, m.listed, m.lastAnswer)
	return dnsbl.NewMetricsObserver(slog.New(slog.DiscardHandler), m.checks, m.listed, m.lastAnswer), m
}

func writeDNSBLConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dnsbl.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDNSBLRuntimeOff: with no config, or a config that is not enabled, there
// is no monitor and no resolver is ever made, so nothing can be sent.
func TestDNSBLRuntimeOff(t *testing.T) {
	for name, path := range map[string]string{
		"no config":       "",
		"not enabled":     writeDNSBLConfig(t, strings.Replace(dnsblTestConfig, `"enabled": true`, `"enabled": false`, 1)),
		"enabled omitted": writeDNSBLConfig(t, `{}`),
	} {
		t.Run(name, func(t *testing.T) {
			obs, _ := newDNSBLTestObserver(t)
			rt, err := newDNSBLRuntime(path, func() dnsbl.Resolver {
				t.Fatal("a resolver was made for a monitor that is off")
				return nil
			}, obs)
			if err != nil || rt != nil {
				t.Fatalf("newDNSBLRuntime = %v, %v; want no monitor and no error", rt, err)
			}
			if err := rt.prepare(nil); err != nil {
				t.Fatalf("disabled monitor accessed database: %v", err)
			}
		})
	}
}

// TestDNSBLRuntimeInvalid: an enabled config that does not validate is an
// error at startup, and nothing is asked of the resolver.
func TestDNSBLRuntimeInvalid(t *testing.T) {
	for name, body := range map[string]string{
		"private ip":         strings.Replace(dnsblTestConfig, `"192.0.2.99"`, `"10.0.0.1"`, 1),
		"no providers":       strings.Replace(dnsblTestConfig, `[{"name": "example", "zone": "bl.example.org", "listed": ["127.0.0.2"]}]`, `[]`, 1),
		"no listing code":    strings.Replace(dnsblTestConfig, `"listed": ["127.0.0.2"]`, `"listed": []`, 1),
		"interval too short": strings.Replace(dnsblTestConfig, `"1h"`, `"10s"`, 1),
		"unknown field":      strings.Replace(dnsblTestConfig, `"enabled": true`, `"enabled": true, "preset": "spamhaus"`, 1),
		"no timeout":         strings.Replace(dnsblTestConfig, `"timeout": "2s",`, ``, 1),
	} {
		t.Run(name, func(t *testing.T) {
			obs, _ := newDNSBLTestObserver(t)
			res := newDNSBLResolver(false)
			rt, err := newDNSBLRuntime(writeDNSBLConfig(t, body), func() dnsbl.Resolver { return res }, obs)
			if err == nil || rt != nil {
				t.Fatalf("newDNSBLRuntime = %v, %v; want an error", rt, err)
			}
			if n := res.count(); n != 0 {
				t.Fatalf("an invalid config sent %d queries", n)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		obs, _ := newDNSBLTestObserver(t)
		if _, err := newDNSBLRuntime(filepath.Join(t.TempDir(), "absent.json"), func() dnsbl.Resolver {
			t.Fatal("a resolver was made for a config that did not load")
			return nil
		}, obs); !errors.Is(err, dnsbl.ErrInvalidConfigFile) {
			t.Fatalf("newDNSBLRuntime = %v, want ErrInvalidConfigFile", err)
		}
	})
}

// TestDNSBLRuntimeRunsAndStops: enabled, the monitor checks at once (start
// delay 0), records the listing, then waits out its interval; stop ends it.
func TestDNSBLRuntimeRunsAndStops(t *testing.T) {
	obs, m := newDNSBLTestObserver(t)
	res := newDNSBLResolver(false)
	rt, err := newDNSBLRuntime(writeDNSBLConfig(t, dnsblTestConfig), func() dnsbl.Resolver { return res }, obs)
	if err != nil || rt == nil {
		t.Fatalf("newDNSBLRuntime = %v, %v", rt, err)
	}
	if n := res.count(); n != 0 {
		t.Fatalf("%d queries before start", n)
	}
	rt.start(context.Background())

	deadline := time.Now().Add(5 * time.Second)
	for testutil.ToFloat64(m.checks.WithLabelValues("example", string(dnsbl.StatusListed))) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first check was never recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := testutil.ToFloat64(m.listed.WithLabelValues("192.0.2.99", "example")); got != 1 {
		t.Fatalf("listed = %v, want 1", got)
	}

	began := time.Now()
	if err := rt.stop(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(began); d > time.Second {
		t.Fatalf("stop took %s while the monitor was waiting", d)
	}
	select {
	case <-rt.done:
	default:
		t.Fatal("the monitor goroutine is still running after stop")
	}
	if n := res.count(); n != 1 {
		t.Fatalf("%d queries, want 1 (one IP, one provider, one pass)", n)
	}
}

// TestDNSBLRuntimeStopDuringCheck: stop abandons a check in flight promptly,
// and the abandoned check is not recorded.
func TestDNSBLRuntimeStopDuringCheck(t *testing.T) {
	obs, m := newDNSBLTestObserver(t)
	res := newDNSBLResolver(true)
	rt, err := newDNSBLRuntime(writeDNSBLConfig(t, dnsblTestConfig), func() dnsbl.Resolver { return res }, obs)
	if err != nil {
		t.Fatal(err)
	}
	rt.start(context.Background())
	select {
	case <-res.asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the monitor never asked")
	}

	began := time.Now()
	if err := rt.stop(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(began); d > time.Second {
		t.Fatalf("stop took %s with a check in flight", d)
	}
	if n := testutil.CollectAndCount(m.checks); n != 0 {
		t.Fatalf("the abandoned check was recorded (%d series)", n)
	}
	if n := testutil.CollectAndCount(m.listed); n != 0 {
		t.Fatalf("listed has %d series after an abandoned check, want none", n)
	}
}

// TestDNSBLRuntimeDurableRestart exercises the API's prepare/start/stop path
// across separate runtimes, using committed events rather than in-memory state.
func TestDNSBLRuntimeDurableRestart(t *testing.T) {
	if os.Getenv("DB_HOST") == "" || os.Getenv("DB_NAME") == "" {
		t.Skip("set DB_HOST and DB_NAME to run the DNSBL database test")
	}
	port, err := strconv.Atoi(os.Getenv("DB_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := rmdb.Connect(&config.Config{
		DBHost: os.Getenv("DB_HOST"), DBPort: port, DBName: os.Getenv("DB_NAME"),
		DBUser: os.Getenv("DB_USER"), DBPass: os.Getenv("DB_PASS"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.AutoMigrate(&models.DNSBLListingEvent{}); err != nil {
		t.Fatal(err)
	}
	provider := "restart-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	path := writeDNSBLConfig(t, strings.Replace(dnsblTestConfig, `"name": "example"`, `"name": "`+provider+`"`, 1))
	runPass := func(clean bool) {
		t.Helper()
		obs, metrics := newDNSBLTestObserver(t)
		resolver := newDNSBLResolver(false)
		resolver.clean = clean
		rt, err := newDNSBLRuntime(path, func() dnsbl.Resolver { return resolver }, obs)
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.prepare(database); err != nil {
			t.Fatal(err)
		}
		if resolver.count() != 0 {
			t.Fatal("prepare sent a DNS query")
		}
		rt.start(context.Background())
		status := dnsbl.StatusListed
		if clean {
			status = dnsbl.StatusNotListed
		}
		deadline := time.Now().Add(5 * time.Second)
		for testutil.ToFloat64(metrics.checks.WithLabelValues(provider, string(status))) != 1 {
			if time.Now().After(deadline) {
				_ = rt.stop()
				t.Fatal("runtime did not complete its first check")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := rt.stop(); err != nil {
			t.Fatal(err)
		}
	}
	rows := func(want int) []models.DNSBLListingEvent {
		t.Helper()
		var events []models.DNSBLListingEvent
		if err := database.Where("provider = ?", provider).Order("id").Find(&events).Error; err != nil {
			t.Fatal(err)
		}
		if len(events) != want {
			t.Fatalf("events = %+v, want %d", events, want)
		}
		return events
	}
	runPass(false)
	first := rows(1)[0]
	runPass(false)
	rows(1)
	runPass(true)
	recovered := rows(2)[1]
	if first.Kind != "listed" || recovered.Kind != "delisted" || !recovered.Since.Equal(first.At) {
		t.Fatalf("listing = %+v, recovery = %+v", first, recovered)
	}
	if err := database.Where("provider = ?", provider).Delete(&models.DNSBLListingEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	obs, _ := newDNSBLTestObserver(t)
	rt, err := newDNSBLRuntime(path, func() dnsbl.Resolver { return newDNSBLResolver(false) }, obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.prepare(database); err == nil {
		t.Fatal("unreadable seed did not fail startup")
	}
}
