package dnsbl

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// MetricsObserver is the Observer the server runs: it logs each listing and
// recovery and keeps the DNSBL metrics. Its state is the Monitor's, so it is
// in memory only and starts from unknown on every restart.
type MetricsObserver struct {
	log        *slog.Logger
	checks     *prometheus.CounterVec // provider, status
	listed     *prometheus.GaugeVec   // ip, provider
	lastAnswer *prometheus.GaugeVec   // ip, provider
}

var _ Observer = (*MetricsObserver)(nil)

// NewMetricsObserver returns an Observer that writes to log and to the given
// vectors (see metrics.NewDNSBLChecks, NewDNSBLListed and NewDNSBLLastAnswer).
func NewMetricsObserver(log *slog.Logger, checks *prometheus.CounterVec, listed, lastAnswer *prometheus.GaugeVec) *MetricsObserver {
	return &MetricsObserver{log: log, checks: checks, listed: listed, lastAnswer: lastAnswer}
}

// Checked counts every check. Only an authoritative answer moves the listing
// gauge: provider_error, indeterminate and unsupported say nothing about the
// IP, so they leave a listed pair listed and an unknown pair absent.
func (o *MetricsObserver) Checked(obs Observation) {
	r := obs.Result
	o.checks.WithLabelValues(r.Provider, string(r.Status)).Inc()
	var v float64
	switch r.Status {
	case StatusListed:
		v = 1
	case StatusNotListed:
		v = 0
	default:
		o.log.Debug("dnsbl check had no answer", "result", r)
		return
	}
	ip := r.IP.String()
	o.listed.WithLabelValues(ip, r.Provider).Set(v)
	o.lastAnswer.WithLabelValues(ip, r.Provider).Set(float64(obs.At.Unix()))
}

// Changed logs a listing as a warning and a recovery as info, each with the
// evidence that caused it.
func (o *MetricsObserver) Changed(tr Transition) {
	switch tr.Kind {
	case TransitionListed:
		o.log.Warn("dnsbl: sending IP listed", "result", tr.Result, "at", tr.At)
	case TransitionRecovered:
		o.log.Info("dnsbl: sending IP no longer listed", "result", tr.Result, "at", tr.At,
			"listing", tr.Previous, "listed_since", tr.Since)
	}
}
