package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// HTTPRequestsTotal counts total HTTP requests by method, path, and status.
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_http_requests_total",
			Help: "Total HTTP requests",
		},
		[]string{"method", "path", "status"},
	)

	// HTTPRequestDuration records HTTP request duration in seconds.
	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "restmail_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	// QueueSize tracks the current outbound queue depth.
	QueueSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_queue_size",
		Help: "Current outbound queue size",
	})

	// QueueProcessed counts total messages successfully processed from the queue.
	QueueProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_queue_processed_total",
		Help: "Total messages processed from queue",
	})

	// QueueErrors counts total queue processing errors.
	QueueErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_queue_errors_total",
		Help: "Total queue processing errors",
	})

	// ActiveConnections tracks the number of active protocol connections.
	ActiveConnections = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "restmail_active_connections",
			Help: "Active protocol connections",
		},
		[]string{"protocol"},
	)

	// MessagesReceived counts inbound-MX messages received from the internet,
	// per transport security. `transport` is strictly bounded to tls/plaintext;
	// non inbound-MX deliveries (IMAP APPEND, local webmail send) are not counted.
	MessagesReceived = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_messages_received_total",
			Help: "Total inbound-MX messages received by transport security",
		},
		[]string{"transport"},
	)

	// MessagesSent counts total outbound messages sent.
	MessagesSent = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_messages_sent_total",
		Help: "Total messages sent",
	})

	// PipelineFilterDuration records pipeline filter execution time in seconds,
	// per filter. The `filter` label is bounded to the built-in filter set;
	// custom filter names collapse to "custom" at the observation boundary.
	PipelineFilterDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "restmail_pipeline_filter_duration_seconds",
			Help:    "Pipeline filter execution time in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"filter"},
	)

	// PipelineStageDecisions counts pipeline step outcomes, per filter and
	// action. `filter` is bounded (built-ins + "custom"); `action` is one of
	// continue, reject, quarantine, discard, defer, skipped, error.
	PipelineStageDecisions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_pipeline_stage_decisions_total",
			Help: "Pipeline step decisions by filter and action",
		},
		[]string{"filter", "action"},
	)

	// PipelineTerminal counts terminal message outcomes, per direction and
	// outcome. `direction` is inbound/outbound; `outcome` is one of delivered,
	// queued, rejected, quarantined, discarded, deferred.
	PipelineTerminal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_pipeline_terminal_total",
			Help: "Terminal message outcomes by direction and outcome",
		},
		[]string{"direction", "outcome"},
	)

	// PipelineRejectReason counts non-continue terminal outcomes by a bounded
	// reason_code. The reason is derived once from the terminal step via
	// pipeline.ReasonForStep; the label domain is the fixed ReasonCode enum, so
	// it stays low-cardinality and volume-independent.
	PipelineRejectReason = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_pipeline_reject_reason_total",
			Help: "Non-continue terminal outcomes by bounded reason_code",
		},
		[]string{"reason_code"},
	)

	// AuthVerdict counts email-authentication verdicts, per mechanism and
	// result. `mechanism` is spf/dkim/dmarc/arc; `result` is bounded to
	// pass, fail, none, neutral, softfail, temperror, permerror, other.
	AuthVerdict = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_auth_verdict_total",
			Help: "Email authentication verdicts by mechanism and result",
		},
		[]string{"mechanism", "result"},
	)

	// AuthFailures counts authentication failures by protocol.
	AuthFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_auth_failures_total",
			Help: "Authentication failures",
		},
		[]string{"protocol"},
	)

	// CertExpiryDays tracks days until certificate expiry.
	//
	// The previous per-`domain` label was an unbounded-cardinality violation
	// (one series per certificate domain). PR1 drops the label to restore the
	// bounded-cardinality convention; a future change that needs per-cert
	// visibility should gate it to a small, configured domain set.
	CertExpiryDays = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_cert_expiry_days",
		Help: "Days until certificate expires",
	})

	// TraceDropped counts per-message traces dropped by the async recorder's
	// drop-on-full backpressure (buffer saturated / recorder shut down). It is
	// the honest signal that trace detail was lost while mail kept flowing —
	// aggregate metrics remain exact because they are counted inline, not here.
	// No labels: this measures a single degradation condition.
	TraceDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_trace_dropped_total",
		Help: "Per-message traces dropped by async recorder backpressure",
	})

	// TLSCertMissing counts handshakes refused because a domain this server is
	// responsible for has no usable certificate: none stored, expired, or the
	// stored pair will not load. Serving another name's certificate instead is
	// not an option, so every one of these is a domain that cannot receive mail
	// until someone fixes it. No labels: one degradation condition, and a
	// per-domain label would be unbounded (see CertExpiryDays above).
	TLSCertMissing = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_tls_cert_missing_total",
		Help: "Handshakes refused because a domain served here has no usable certificate",
	})

	// DomainsOutOfService counts domains taken out of service because their
	// certificate stopped being valid — expired without renewal, deleted, or no
	// longer loadable. Each one is a domain that was accepting mail and now is
	// not, so it needs a person. No labels: the domain names are in the log line
	// that accompanies each increment, and a label here would be unbounded.
	DomainsOutOfService = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_domains_out_of_service_total",
		Help: "Domains taken out of service because their certificate stopped being valid",
	})

	// DNSBLChecks, DNSBLListed and DNSBLLastAnswer are the DNSBL monitor's
	// (issue #148). They exist only while an operator has enabled it, and their
	// labels are bounded by that operator's config: the sending IPs and the
	// provider names it lists.
	DNSBLChecks     = NewDNSBLChecks()
	DNSBLListed     = NewDNSBLListed()
	DNSBLLastAnswer = NewDNSBLLastAnswer()
)

// NewDNSBLChecks counts completed DNSBL checks per provider and status
// (listed, not_listed, provider_error, indeterminate, unsupported). Alert on
// provider_error and indeterminate here, apart from reputation: they say the
// list could not answer, not that an IP is listed.
func NewDNSBLChecks() *prometheus.CounterVec {
	return prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_dnsbl_checks_total",
			Help: "Completed DNSBL checks by provider and status",
		},
		[]string{"provider", "status"},
	)
}

// NewDNSBLListed is the last authoritative answer per sending IP and provider:
// 1 listed, 0 not listed. A pair no list has answered for since start is
// absent, not 0: unknown is never reported clean. An error answer leaves the
// value as it was.
func NewDNSBLListed() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "restmail_dnsbl_listed",
			Help: "Last known DNSBL listing per IP and provider (1 listed, 0 not listed; absent when unknown)",
		},
		[]string{"ip", "provider"},
	)
}

// NewDNSBLLastAnswer is the Unix time of the last authoritative answer (listed
// or not listed) per sending IP and provider, so a listing value that has gone
// stale behind provider errors can be alerted on.
func NewDNSBLLastAnswer() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "restmail_dnsbl_last_answer_timestamp_seconds",
			Help: "Unix time of the last authoritative DNSBL answer per IP and provider",
		},
		[]string{"ip", "provider"},
	)
}

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		QueueSize,
		QueueProcessed,
		QueueErrors,
		ActiveConnections,
		MessagesReceived,
		MessagesSent,
		PipelineFilterDuration,
		PipelineStageDecisions,
		PipelineTerminal,
		PipelineRejectReason,
		AuthVerdict,
		AuthFailures,
		CertExpiryDays,
		TraceDropped,
		TLSCertMissing,
		DomainsOutOfService,
		DNSBLChecks,
		DNSBLListed,
		DNSBLLastAnswer,
	)
}
