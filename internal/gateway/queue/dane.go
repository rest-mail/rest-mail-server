package queue

import (
	"context"
	"time"

	"github.com/restmail/restmail/internal/dane"
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
func (w *Worker) daneRecords(host string) ([]dane.TLSA, error) {
	ctx, cancel := context.WithTimeout(context.Background(), daneLookupTimeout)
	defer cancel()
	records, sec, err := w.dane.TLSA(ctx, host, 25)
	switch {
	case err != nil:
		return nil, &SMTPError{Code: 451, Enhanced: "4.4.3", RemoteMTA: host, Message: "DANE: the validating resolver gave no answer for " + host + "'s TLSA records"}
	case sec == dane.Bogus:
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
