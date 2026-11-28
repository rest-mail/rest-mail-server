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

	// QueueDue counts the queued rows a worker should be taking now: pending
	// or deferred rows whose next attempt has come, plus rows being sent.
	// Rows waiting for a later retry are in QueueSize but not here, so
	// QueueWorkerStalled does not fire on a queue that is correctly idle
	// until its retries come due (#1630). It also counts rows held back by
	// QUEUE_MAX_PER_DESTINATION, so it is not a backlog measure: see
	// QueueWaitingForWorker and QueueHeldByDestination (#2117). No labels.
	QueueDue = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_queue_due",
		Help: "Outbound queue rows due for an attempt now (pending or deferred with next attempt passed, or delivering)",
	})

	// QueueOldestAge is the age in seconds of the oldest pending or deferred
	// outbound row, 0 when there is none. A deep queue that is minutes old and
	// one that is hours old are different problems; QueueOldestMessage reads
	// this (#1579). No labels.
	QueueOldestAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_queue_oldest_age_seconds",
		Help: "Age in seconds of the oldest pending or deferred outbound queue row (0 when none)",
	})

	// QueueWaitingForWorker counts the rows a worker could take now and none
	// has yet: due, and within the places their destination has free under
	// QUEUE_MAX_PER_DESTINATION. Together with QueueWaitingForWorkerOldest it
	// is queue saturation, more mail due than the workers keep up with; the
	// rows the destination cap holds back are QueueHeldByDestination instead,
	// so a destination held back by policy does not read as saturation or
	// backlog (#2117). No labels.
	QueueWaitingForWorker = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_queue_waiting_for_worker",
		Help: "Outbound queue rows due now that a worker could take: their destination has a place free under QUEUE_MAX_PER_DESTINATION",
	})

	// QueueWaitingForWorkerOldest is how long, in seconds, the longest-waiting
	// of QueueWaitingForWorker's rows has been due, 0 when there is none. Free
	// workers take a due row within QUEUE_POLL_INTERVAL, so this stays near
	// zero unless every worker is busy; QueueWorkersSaturated reads it
	// (#2117). No labels.
	QueueWaitingForWorkerOldest = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_queue_waiting_for_worker_oldest_seconds",
		Help: "Seconds the longest-waiting outbound row a worker could take has been due (0 when none)",
	})

	// QueueHeldByDestination counts the rows due now that no worker may take
	// yet because their recipient domain already has QUEUE_MAX_PER_DESTINATION
	// messages in flight (#1966). Holding them is the policy working, not a
	// backlog: QueueBacklogHigh leaves them out (#2117). No labels.
	QueueHeldByDestination = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_queue_held_by_destination",
		Help: "Outbound queue rows due now but held back because their destination has QUEUE_MAX_PER_DESTINATION messages in flight",
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

	// AttachmentCleanupRemoved counts stored attachment objects nothing referred to, removed.
	AttachmentCleanupRemoved = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_attachment_cleanup_removed_total",
		Help: "Stored attachment objects removed because no attachment refers to them",
	})

	// AttachmentCleanupBytes counts the space those removals gave back.
	AttachmentCleanupBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_attachment_cleanup_bytes_total",
		Help: "Bytes freed by removing unreferenced attachment objects",
	})

	// ActiveConnections tracks the number of active protocol connections.
	ActiveConnections = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "restmail_active_connections",
			Help: "Active protocol connections",
		},
		[]string{"protocol"},
	)

	// ConnectionsAccepted counts protocol connections admitted by the
	// connection limiter, per protocol (smtp, imap, pop3, as the gateway names
	// its limiter). restmail_active_connections is a level that is often 0 on
	// a quiet server; this counter's rate falling to zero is what says a
	// listener stopped taking connections (#1579).
	ConnectionsAccepted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_connections_accepted_total",
			Help: "Protocol connections admitted, by protocol",
		},
		[]string{"protocol"},
	)

	// SMTPTransactionDuration records one SMTP mail transaction, from MAIL
	// FROM to its end: the reply to DATA (outcome accepted or rejected), or a
	// RSET or disconnect before that (aborted). The label is bounded to those
	// three (#1579).
	SMTPTransactionDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "restmail_smtp_transaction_duration_seconds",
			Help:    "SMTP mail transaction duration from MAIL FROM to its end, by outcome",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		},
		[]string{"outcome"},
	)

	// IMAPCommandDuration records how long the IMAP gateway took over each
	// command. The command label is bounded to IMAPCommandNames; any other
	// name a client sends is "other" (#1579).
	IMAPCommandDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "restmail_imap_command_duration_seconds",
			Help:    "IMAP command duration, by command",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"command"},
	)

	// DBConnectionErrors counts database operations that failed because the
	// database could not be reached: a connection refused, dropped or timed
	// out, as opposed to a query the database answered with an error (#1579).
	DBConnectionErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_db_connection_errors_total",
		Help: "Database operations that failed because the database could not be reached",
	})

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

	// OutboundTransport counts SMTP deliveries by transport security (RFC 7435):
	// authenticated (TLS with a certificate verified for the MX host),
	// encrypted (TLS whose certificate did not verify, where no policy required
	// it) or cleartext. The label is bounded to those three.
	OutboundTransport = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_outbound_transport_total",
			Help: "SMTP deliveries by transport security",
		},
		[]string{"security"},
	)

	// OutboundSMTPAttempts counts attempts to deliver directly to an MX, by the
	// IP version of the connection and what came of it: delivered, deferred (the
	// MX answered 4xx), rejected (5xx) or failed (the connection broke, or the
	// reply was no verdict). Some receivers treat IPv6 mail more harshly, and
	// this shows it (#1579). An attempt that never connected has no IP version
	// and is not counted, nor is one REST Mail itself refused to send. Both
	// labels are closed sets: 8 series.
	OutboundSMTPAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_outbound_smtp_attempts_total",
			Help: "Direct SMTP delivery attempts that reached an MX, by IP version and result",
		},
		[]string{"ip_version", "result"},
	)

	// OutboundMXDialAttempts counts each TCP dial attempt to an MX address,
	// including each address tried during fallback (#2117). The labels are
	// bounded to two address families and five outcomes.
	OutboundMXDialAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_outbound_mx_dial_attempts_total",
			Help: "Outbound TCP dial attempts to MX addresses, by address family and outcome",
		},
		[]string{"family", "outcome"},
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

	// SignInAccountsUnderAttack counts each time one address reaches many recent
	// sign-in failures. It counts once per episode, and has no address label:
	// addresses are attacker-chosen. The log line of the same moment names it.
	SignInAccountsUnderAttack = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_signin_accounts_under_attack_total",
		Help: "Times one address reached many recent sign-in failures",
	})

	// SignInFailuresWaiting is how many failed sign-ins are waiting out their
	// delay now.
	SignInFailuresWaiting = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "restmail_signin_failures_waiting",
		Help: "Failed sign-ins waiting out their delay",
	})

	// SignInFailuresUnslowed counts failed sign-ins answered at once, because
	// as many as may wait were already waiting.
	SignInFailuresUnslowed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "restmail_signin_failures_unslowed_total",
		Help: "Failed sign-ins answered without their delay because the waiting bound was reached",
	})

	// AuthFailures counts authentication failures by protocol.
	AuthFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "restmail_auth_failures_total",
			Help: "Authentication failures",
		},
		[]string{"protocol"},
	)

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
	// per-domain label would be unbounded (see CertExpiryCollector).
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
		QueueDue,
		QueueOldestAge,
		QueueWaitingForWorker,
		QueueWaitingForWorkerOldest,
		QueueHeldByDestination,
		QueueProcessed,
		AttachmentCleanupRemoved,
		AttachmentCleanupBytes,
		QueueErrors,
		ActiveConnections,
		ConnectionsAccepted,
		SMTPTransactionDuration,
		IMAPCommandDuration,
		DBConnectionErrors,
		dbPool,
		MessagesReceived,
		MessagesSent,
		OutboundTransport,
		OutboundSMTPAttempts,
		OutboundMXDialAttempts,
		PipelineFilterDuration,
		PipelineStageDecisions,
		PipelineTerminal,
		PipelineRejectReason,
		AuthVerdict,
		AuthFailures,
		SignInAccountsUnderAttack,
		SignInFailuresWaiting,
		SignInFailuresUnslowed,
		TraceDropped,
		TLSCertMissing,
		DomainsOutOfService,
		DNSBLChecks,
		DNSBLListed,
		DNSBLLastAnswer,
	)
}
