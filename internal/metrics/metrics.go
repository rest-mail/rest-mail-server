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
)

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
	)
}
