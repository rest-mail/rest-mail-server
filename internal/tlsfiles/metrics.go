package tlsfiles

import "github.com/prometheus/client_golang/prometheus"

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
	// check. Checks happen on new handshakes, at most every few seconds.
	reloadFailing = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_tls_cert_reload_failing",
		Help: "TLS certificate pairs whose replacement files fail to load and that still serve the previous pair",
	})
)

func init() {
	prometheus.MustRegister(reloadFailures, reloadFailing)
}
