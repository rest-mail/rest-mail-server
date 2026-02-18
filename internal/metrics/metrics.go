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

	// MessagesReceived counts total inbound messages received.
	MessagesReceived = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_messages_received_total",
		Help: "Total messages received",
	})

	// MessagesSent counts total outbound messages sent.
	MessagesSent = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_messages_sent_total",
		Help: "Total messages sent",
	})

	// PipelineFilterDuration records pipeline filter execution time in seconds.
	PipelineFilterDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "restmail_pipeline_filter_duration_seconds",
			Help:    "Pipeline filter execution time in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"filter"},
	)

	// AuthFailures counts authentication failures by protocol.
	AuthFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_auth_failures_total",
			Help: "Authentication failures",
		},
		[]string{"protocol"},
	)

	// CertExpiryDays tracks days until certificate expiry per domain.
	CertExpiryDays = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "restmail_cert_expiry_days",
			Help: "Days until certificate expires",
		},
		[]string{"domain"},
	)
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
		AuthFailures,
		CertExpiryDays,
	)
}
