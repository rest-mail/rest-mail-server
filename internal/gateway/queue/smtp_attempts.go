package queue

import (
	"net/netip"

	"github.com/rest-mail/rest-mail-server/internal/metrics"
)

// The ip_version label of restmail_outbound_smtp_attempts_total.
const (
	smtpIPv4 = "4"
	smtpIPv6 = "6"
)

// The result label of restmail_outbound_smtp_attempts_total.
const (
	smtpAttemptDelivered = "delivered"
	smtpAttemptDeferred  = "deferred" // the MX answered 4xx
	smtpAttemptRejected  = "rejected" // the MX answered 5xx
	smtpAttemptFailed    = "failed"   // the connection broke, or the reply was no verdict
)

// initSMTPAttemptSeries creates every series at zero, so a scrape sees the first
// failure of a kind as a rise. It runs where a queue worker does, so only a
// process that delivers mail exports them.
func initSMTPAttemptSeries() {
	for _, version := range []string{smtpIPv4, smtpIPv6} {
		for _, result := range []string{smtpAttemptDelivered, smtpAttemptDeferred, smtpAttemptRejected, smtpAttemptFailed} {
			metrics.OutboundSMTPAttempts.WithLabelValues(version, result)
		}
	}
}

// countSMTPAttempt records how an attempt on a connection to remoteIP ended; err
// is nil when the MX took the message. Only an error from the MX or the
// connection to it belongs here: REST Mail's own refusals are not counted, though
// some carry a 5xx code. An address that cannot be read counts nothing, which
// keeps the label to its two values.
func countSMTPAttempt(remoteIP string, err error) {
	version := smtpIPVersion(remoteIP)
	if version == "" {
		return
	}
	metrics.OutboundSMTPAttempts.WithLabelValues(version, smtpAttemptResult(err)).Inc()
}

// smtpIPVersion is the IP version of an address, empty when it is not one. An
// IPv4 peer seen through a dual-stack socket (::ffff:192.0.2.1) is IPv4.
func smtpIPVersion(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	if addr.Unmap().Is4() {
		return smtpIPv4
	}
	return smtpIPv6
}

// smtpAttemptResult sorts an attempt's outcome by the reply that ended it.
func smtpAttemptResult(err error) string {
	if err == nil {
		return smtpAttemptDelivered
	}
	reply := smtpErrorFrom(err)
	if reply == nil {
		return smtpAttemptFailed
	}
	switch reply.Code / 100 {
	case 4:
		return smtpAttemptDeferred
	case 5:
		return smtpAttemptRejected
	}
	return smtpAttemptFailed
}
