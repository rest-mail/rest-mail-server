package queue

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/dane"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
)

// daneLookup is what DANE for SMTP (RFC 7672) asks of DNS: whether a domain's MX RRset
// is DNSSEC-secure, and an MX host's TLSA records. *dane.Resolver provides it.
type daneLookup interface {
	MXSecure(ctx context.Context, domain string) (dane.Security, error)
	TLSA(ctx context.Context, host string, port int) ([]dane.TLSA, dane.Security, error)
}

// SetDANEResolver turns on DANE for outbound SMTP, asking r. Without one the queue
// does not look up TLSA records and delivers as it did before (#725).
func (w *Worker) SetDANEResolver(r *dane.Resolver) {
	if r != nil {
		w.dane = r
	}
}

// daneLookupTimeout bounds each DANE DNS question.
const daneLookupTimeout = 30 * time.Second

// daneMXSecure reports whether DANE applies to a recipient domain: it does only when
// the domain's MX RRset is DNSSEC-secure (RFC 7672 §2.2.1). A bogus answer, or no
// answer from the validating resolver, defers the message rather than delivering it
// without DANE: an attacker who breaks a signature must not switch DANE off (§2.1.1).
func (w *Worker) daneMXSecure(dnsDomain string) (bool, error) {
	if w.dane == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), daneLookupTimeout)
	defer cancel()
	sec, err := w.dane.MXSecure(ctx, dnsDomain)
	switch {
	case err != nil:
		return false, &SMTPError{Code: 451, Enhanced: "4.4.3", Message: "DANE: the validating resolver gave no answer for the recipient domain's MX records"}
	case sec == dane.Bogus:
		return false, &SMTPError{Code: 451, Enhanced: "4.4.3", Message: "DANE: the recipient domain's MX records failed DNSSEC validation"}
	}
	return sec == dane.Secure, nil
}

// daneRecords is an MX host's usable TLSA records (RFC 7672 §2.2.3), nil when DANE does
// not apply to that host: its TLSA lookup was insecure, or found no record an SMTP
// client can use. A bogus or failed lookup is a temporary failure for this host; the
// next MX is tried, and the message is never sent to this one without DANE.
// A bogus lookup is also recorded for the domain's TLS-RPT report when report is set.
func (w *Worker) daneRecords(domain, host string, report bool) ([]dane.TLSA, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daneLookupTimeout)
	defer cancel()
	records, sec, err := w.dane.TLSA(ctx, host, 25)
	switch {
	case err != nil:
		return nil, &SMTPError{Code: 451, Enhanced: "4.4.3", RemoteMTA: host, Message: "DANE: the validating resolver gave no answer for " + host + "'s TLSA records"}
	case sec == dane.Bogus:
		if report {
			w.recordDANESession(domain, host, nil, nil, time.Now())
		}
		return nil, &SMTPError{Code: 451, Enhanced: "4.4.3", RemoteMTA: host, Message: "DANE: " + host + "'s TLSA records failed DNSSEC validation"}
	case sec != dane.Secure:
		return nil, nil
	}
	usable := dane.Usable(records)
	if len(usable) == 0 {
		return nil, nil
	}
	return usable, nil
}

// daneRefusal is the temporary failure of a DANE host that gave no TLS session its
// TLSA records authenticate: STARTTLS missing or a chain no record matches. The
// message is not sent to it in the clear or over unauthenticated TLS (RFC 7672 §2.2).
func daneRefusal(host string, link *smtpLink) error {
	reason := "it does not offer STARTTLS"
	if link.tlsErr != nil {
		reason = link.tlsErr.Error()
	}
	return &SMTPError{Code: 451, Enhanced: "4.7.5", RemoteMTA: host, Message: "DANE: no TLS session authenticated by " + host + "'s TLSA records: " + reason}
}

// The TLS-RPT policy type and result types of a DANE session (RFC 8460 §4.3).
const (
	tlsrptPolicyTLSA    = "tlsa"
	tlsrptTLSAInvalid   = "tlsa-invalid"
	tlsrptDNSSECInvalid = "dnssec-invalid"
)

// recordDANESession stores what a session with a DANE host came to, for the recipient
// domain's TLS-RPT report: policy type tlsa, whose policy strings are the host's TLSA
// records in presentation form (RFC 8460 §4.2). link is nil when the session never
// started because the host's TLSA records failed DNSSEC validation (dnssec-invalid).
func (w *Worker) recordDANESession(recipientDomain, host string, records []dane.TLSA, link *smtpLink, now time.Time) {
	if w.db == nil {
		return
	}
	domain, err := rmail.DomainASCII(recipientDomain)
	if err != nil {
		return
	}
	lines := make([]string, len(records))
	for i, r := range records {
		lines[i] = fmt.Sprintf("%d %d %d %s", r.Usage, r.Selector, r.MatchingType, strings.ToUpper(hex.EncodeToString(r.Data)))
	}
	row := models.TLSRPTSession{
		Day:          now.UTC().Format(tlsrptDay),
		PolicyDomain: domain,
		PolicyType:   tlsrptPolicyTLSA,
		PolicyString: jsonList(lines),
		ReceivingMX:  host,
	}
	switch {
	case link == nil:
		row.ResultType, row.FailureReason = tlsrptDNSSECInvalid, "the TLSA records failed DNSSEC validation"
	case link.tlsErr != nil && errors.Is(link.tlsErr, dane.ErrNoMatch):
		row.ResultType, row.FailureReason = tlsrptTLSAInvalid, link.tlsErr.Error()
	case link.tlsErr != nil:
		row.ResultType, row.FailureReason = tlsrptValidationFailure, link.tlsErr.Error()
	case link.security == transportCleartext:
		row.ResultType = tlsrptStartTLSNotSupported
	}
	if link != nil {
		row.ReceivingIP, row.SendingIP = link.remoteIP, link.localIP
	}
	if err := w.db.Create(&row).Error; err != nil {
		slog.Warn("queue: TLS-RPT DANE session not recorded", "domain", domain, "host", host, "error", err)
	}
}
