package queue

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"time"

	mtasts "github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
)

// The TLS-RPT policy types and result types REST Mail reports (RFC 8460
// §4.3, §4.4).
const (
	tlsrptPolicySTS            = "sts"
	tlsrptNoPolicyFound        = "no-policy-found"
	tlsrptStartTLSNotSupported = "starttls-not-supported"
	tlsrptCertHostMismatch     = "certificate-host-mismatch"
	tlsrptCertExpired          = "certificate-expired"
	tlsrptCertNotTrusted       = "certificate-not-trusted"
	tlsrptValidationFailure    = "validation-failure"
	tlsrptSTSPolicyFetchError  = "sts-policy-fetch-error"
	tlsrptSTSPolicyInvalid     = "sts-policy-invalid"
	tlsrptSTSWebPKIInvalid     = "sts-webpki-invalid"
)

// tlsrptDay is the layout of a session's UTC day.
const tlsrptDay = "2006-01-02"

// recordTLSSession stores what one SMTP session's TLS came to, counted into the
// daily TLS-RPT report for the recipient's domain (RFC 8460 §4). stsErr is why
// MTA-STS discovery found no policy, nil when it found one. A domain whose
// policy could not be fetched or read has the session reported as that
// failure, under policy type sts (§4.3.2.2): it is the reason no policy was
// applied, and what the domain needs to hear. A store that fails is logged:
// reporting must never hold up mail.
func (w *Worker) recordTLSSession(recipientDomain, host string, stsPolicy *mtasts.Policy, stsErr error, link *smtpLink, now time.Time) {
	if w.db == nil {
		return
	}
	domain, err := rmail.DomainASCII(recipientDomain)
	if err != nil {
		return
	}
	row := models.TLSRPTSession{
		Day:          now.UTC().Format(tlsrptDay),
		PolicyDomain: domain,
		PolicyType:   tlsrptNoPolicyFound,
		ReceivingMX:  host,
		ReceivingIP:  link.remoteIP,
		SendingIP:    link.localIP,
	}
	row.ResultType, row.FailureReason = tlsOutcome(stsPolicy, host, link)
	if stsPolicy != nil {
		row.PolicyType = tlsrptPolicySTS
		row.PolicyString = jsonList(stsPolicyLines(stsPolicy))
		row.MXHost = jsonList(stsPolicy.MX)
	} else if failure, reason := stsDiscoveryFailure(stsErr); failure != "" {
		row.PolicyType = tlsrptPolicySTS
		row.ResultType, row.FailureReason = failure, reason
	}
	if err := w.db.Create(&row).Error; err != nil {
		slog.Warn("queue: TLS-RPT session not recorded", "domain", domain, "host", host, "error", err)
	}
}

// tlsOutcome is a session's TLS-RPT result: "" when it negotiated TLS the
// policy accepts, and otherwise the result type and the reason (RFC 8460
// §4.3). Without a policy, any TLS is a success; under an MTA-STS policy the
// certificate must verify for an MX the policy names (RFC 8461 §4).
func tlsOutcome(stsPolicy *mtasts.Policy, host string, link *smtpLink) (resultType, reason string) {
	switch {
	case link.tlsErr != nil:
		return classifyTLSError(link.tlsErr)
	case link.security == transportCleartext:
		return tlsrptStartTLSNotSupported, ""
	case stsPolicy == nil:
		return "", ""
	case link.security != transportAuthenticated:
		return classifyTLSError(link.certErr)
	case len(stsPolicy.MX) > 0 && !stsPolicy.MatchesMX(host):
		return tlsrptValidationFailure, "MX host not named by the MTA-STS policy"
	}
	return "", ""
}

// stsDiscoveryFailure is the RFC 8460 §4.3.2.2 result type for an MTA-STS
// discovery error, with the error as the reason code: the domain publishes a
// record, and its policy could not be fetched, could not be authenticated
// (its certificate failed) or did not parse. It is "" when there is nothing
// to report: a policy was found, the domain publishes no record and so does
// not use MTA-STS, or DNS failed and cannot say which.
func stsDiscoveryFailure(err error) (resultType, reason string) {
	switch {
	case errors.Is(err, mtasts.ErrPolicyInvalid):
		resultType = tlsrptSTSPolicyInvalid
	case errors.Is(err, mtasts.ErrPolicyFetch) && isCertificateError(err):
		resultType = tlsrptSTSWebPKIInvalid
	case errors.Is(err, mtasts.ErrPolicyFetch):
		resultType = tlsrptSTSPolicyFetchError
	default:
		return "", ""
	}
	return resultType, reasonCode(err)
}

// isCertificateError reports whether err is, or wraps, a certificate that
// failed verification.
func isCertificateError(err error) bool {
	var verify *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var unknown x509.UnknownAuthorityError
	return errors.As(err, &verify) || errors.As(err, &hostErr) || errors.As(err, &invalid) || errors.As(err, &unknown)
}

// reasonCode is an error as a failure-reason-code: its text, at most 255
// bytes.
func reasonCode(err error) string {
	reason := err.Error()
	if len(reason) > 255 {
		reason = reason[:255]
	}
	return reason
}

// classifyTLSError names a failed handshake or certificate check by its
// RFC 8460 §4.3 result type, with the error as the reason code.
func classifyTLSError(err error) (resultType, reason string) {
	if err == nil {
		return tlsrptValidationFailure, ""
	}
	reason = reasonCode(err)
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.As(err, &hostErr):
		return tlsrptCertHostMismatch, reason
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		return tlsrptCertExpired, reason
	case errors.As(err, &unknown), errors.As(err, &invalid):
		return tlsrptCertNotTrusted, reason
	}
	return tlsrptValidationFailure, reason
}

// stsPolicyLines is an MTA-STS policy as RFC 8460 §4.5 reports it: one string
// for each line of the policy file, an "mx" line for each pattern.
func stsPolicyLines(p *mtasts.Policy) []string {
	lines := []string{"version: " + p.Version, "mode: " + p.Mode}
	for _, mx := range p.MX {
		lines = append(lines, "mx: "+mx)
	}
	return append(lines, "max_age: "+strconv.Itoa(p.MaxAge))
}

// jsonList encodes a list of strings as a JSON array.
func jsonList(list []string) string {
	b, err := json.Marshal(list)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// addrIP is the IP address of a connection's end, "" when it has none.
func addrIP(addr net.Addr) string {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.IP.String()
	}
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return ""
	}
	return host
}
