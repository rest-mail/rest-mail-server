package tlsfiles

import (
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// The reload metrics live here rather than in internal/metrics, which imports
// configuration that itself loads TLS files through this package. Every process
// that serves the default Prometheus registry (the API and each gateway) and
// loads a pair through this package exposes them.
var (
	// reloadFailures counts replacements of an already loaded TLS
	// certificate/key pair that could not be loaded: missing, malformed, or a
	// certificate that does not match its key. Each is a renewal the process
	// did not pick up; the previous pair keeps serving. A failure that persists
	// across checks counts once, until a later check succeeds. No labels: the
	// file paths are in the log line that accompanies each increment.
	reloadFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_tls_cert_reload_failures_total",
		Help: "Replaced TLS certificate or key files that failed to load while the previous pair kept serving",
	})

	// reloadFailing is the number of loaded TLS certificate/key pairs whose
	// current files fail to load, so the process still serves the previous
	// pair. Zero means every pair loaded from its current files at its last
	// check. Handshakes and expiry scrapes share the same check interval.
	reloadFailing = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_tls_cert_reload_failing",
		Help: "TLS certificate pairs whose replacement files fail to load and that still serve the previous pair",
	})

	servedExpiry = newExpiryCollector()
)

func init() {
	prometheus.MustRegister(reloadFailures, reloadFailing, servedExpiry)
}

// expiryCollector reports restmail_tls_cert_expiry_timestamp_seconds: when the
// soonest-expiring leaf certificate among the pairs this process loaded from
// files expires. restmail_cert_expiry_days reads the certificate table, so it
// cannot see certificates a manager such as Certbot or cert-manager publishes
// as files. A renewal that fails before publishing changes no file, so no reload
// metric moves, and the approaching expiry is what shows it.
//
// It is a timestamp so the value stays right between checks, and one series
// with no labels so that SNI names cannot grow it; the log names each file. With
// no pair loaded the series is absent: unknown never reads as already expired.
type expiryCollector struct {
	mu      sync.Mutex
	loaders []*Loader
	desc    *prometheus.Desc
}

func newExpiryCollector() *expiryCollector {
	return &expiryCollector{desc: prometheus.NewDesc("restmail_tls_cert_expiry_timestamp_seconds",
		"Unix time when the soonest-expiring TLS certificate this process loaded from files expires (absent with none loaded)",
		nil, nil)}
}

// track adds a loader once it holds a pair. A name that never loads is not
// tracked, so requests for unknown SNI names cannot grow the list.
func (c *expiryCollector) track(l *Loader) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaders = append(c.loaders, l)
}

func (c *expiryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

func (c *expiryCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	loaders := slices.Clone(c.loaders)
	c.mu.Unlock()
	var soonest time.Time
	for _, l := range loaders {
		if notAfter := l.checkedExpiry(); soonest.IsZero() || notAfter.Before(soonest) {
			soonest = notAfter
		}
	}
	if soonest.IsZero() {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(soonest.Unix()))
}
