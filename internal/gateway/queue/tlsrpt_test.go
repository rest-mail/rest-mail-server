package queue

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	mtasts "github.com/rest-mail/go-mtasts"
	"github.com/restmail/restmail/internal/db/models"
)

// A session's TLS-RPT result (RFC 8460 §4.3): TLS without a policy succeeds
// whether or not its certificate verified; under MTA-STS the certificate must
// verify for an MX the policy names; no STARTTLS, a failed handshake or a
// certificate that fails are named by their result types.
func TestTLSOutcome(t *testing.T) {
	sts := &mtasts.Policy{Version: "STSv1", Mode: mtasts.ModeEnforce, MX: []string{"*.example.test"}, MaxAge: 86400}
	expired := x509.CertificateInvalidError{Reason: x509.Expired}
	cases := []struct {
		name   string
		policy *mtasts.Policy
		host   string
		link   smtpLink
		want   string
	}{
		{"no policy, verified", nil, "mx.example.test", smtpLink{security: transportAuthenticated}, ""},
		{"no policy, not verified", nil, "mx.example.test", smtpLink{security: transportEncrypted, certErr: expired}, ""},
		{"no policy, no STARTTLS", nil, "mx.example.test", smtpLink{security: transportCleartext}, tlsrptStartTLSNotSupported},
		{"sts, verified", sts, "mx.example.test", smtpLink{security: transportAuthenticated}, ""},
		{"sts, no STARTTLS", sts, "mx.example.test", smtpLink{security: transportCleartext}, tlsrptStartTLSNotSupported},
		{"sts, expired (testing)", sts, "mx.example.test", smtpLink{security: transportEncrypted, certErr: expired}, tlsrptCertExpired},
		{"sts, handshake refused the certificate", sts, "mx.example.test",
			smtpLink{tlsErr: &tls.CertificateVerificationError{Err: x509.HostnameError{Host: "mx.example.test", Certificate: &x509.Certificate{}}}}, tlsrptCertHostMismatch},
		{"sts, MX not in the policy", sts, "mx.other.test", smtpLink{security: transportAuthenticated}, tlsrptValidationFailure},
		{"handshake failed otherwise", nil, "mx.example.test", smtpLink{tlsErr: errors.New("remote error: tls: handshake failure")}, tlsrptValidationFailure},
	}
	for _, c := range cases {
		link := c.link
		if got, _ := tlsOutcome(c.policy, c.host, &link); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// classifyTLSError names each certificate failure by its result type, and
// keeps the error, at most 255 bytes, as the reason code.
func TestClassifyTLSError(t *testing.T) {
	for err, want := range map[error]string{
		x509.HostnameError{Host: "a", Certificate: &x509.Certificate{}}: tlsrptCertHostMismatch,
		x509.CertificateInvalidError{Reason: x509.Expired}:              tlsrptCertExpired,
		x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}:  tlsrptCertNotTrusted,
		x509.UnknownAuthorityError{}:                                    tlsrptCertNotTrusted,
		errors.New(strings.Repeat("x", 300)):                            tlsrptValidationFailure,
	} {
		got, reason := classifyTLSError(err)
		if got != want || reason == "" || len(reason) > 255 {
			t.Errorf("%T: %s (%d-byte reason), want %s", err, got, len(reason), want)
		}
	}
	if got, reason := classifyTLSError(nil); got != tlsrptValidationFailure || reason != "" {
		t.Errorf("nil: %s %q", got, reason)
	}
}

// A domain that publishes an MTA-STS record and whose policy could not be
// fetched, authenticated or read is reported as that failure; one with no
// record, or DNS that could not say, is not (RFC 8460 §4.3.2.2).
func TestSTSDiscoveryFailure(t *testing.T) {
	because := func(reason error) error { return fmt.Errorf("%w: %w", mtasts.ErrNoPolicy, reason) }
	fetch := func(cause error) error { return because(fmt.Errorf("%w: %w", mtasts.ErrPolicyFetch, cause)) }
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"policy found", nil, ""},
		{"no record", because(mtasts.ErrNoRecord), ""},
		{"DNS failed", because(&net.DNSError{Err: "server misbehaving", IsTemporary: true}), ""},
		{"policy host unreachable", fetch(errors.New("dial tcp: connection refused")), tlsrptSTSPolicyFetchError},
		{"policy host certificate untrusted", fetch(&url.Error{Op: "Get", URL: "https://mta-sts.example.test/.well-known/mta-sts.txt",
			Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}), tlsrptSTSWebPKIInvalid},
		{"policy host certificate for another name", fetch(x509.HostnameError{Host: "mta-sts.example.test", Certificate: &x509.Certificate{}}), tlsrptSTSWebPKIInvalid},
		{"policy not valid", because(fmt.Errorf("%w: missing version", mtasts.ErrPolicyInvalid)), tlsrptSTSPolicyInvalid},
	}
	for _, c := range cases {
		got, reason := stsDiscoveryFailure(c.err)
		if got != c.want || (got != "" && reason == "") {
			t.Errorf("%s: %q (reason %q), want %q", c.name, got, reason, c.want)
		}
	}
}

// An MTA-STS policy is reported as its lines, one mx line for each pattern
// (RFC 8460 §4.5).
func TestSTSPolicyLines(t *testing.T) {
	got := stsPolicyLines(&mtasts.Policy{Version: "STSv1", Mode: "testing", MX: []string{"mx1.example.com", "*.example.net"}, MaxAge: 604800})
	want := []string{"version: STSv1", "mode: testing", "mx: mx1.example.com", "mx: *.example.net", "max_age: 604800"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q, want %q", got, want)
	}
}

// A domain asks for reports with exactly one TXT record starting v=TLSRPTv1,
// its rua= listing mailto: and https: URIs (RFC 8460 §3).
func TestParseTLSRPTRecord(t *testing.T) {
	cases := []struct {
		txts []string
		want []string
	}{
		{[]string{"v=TLSRPTv1;rua=mailto:reports@example.com"}, []string{"mailto:reports@example.com"}},
		{[]string{"v=TLSRPTv1; rua=https://reporting.example.com/v1/tlsrpt"}, []string{"https://reporting.example.com/v1/tlsrpt"}},
		{[]string{"v=spf1 -all", "v=TLSRPTv1; ext=1; rua=mailto:a@example.com, https://r.example.com/x,ftp://x.example.com/,mailto:nobody;"},
			[]string{"mailto:a@example.com", "https://r.example.com/x"}},
		{[]string{"v=TLSRPTv1; rua=mailto:a@example.com", "v=TLSRPTv1; rua=mailto:b@example.com"}, nil},
		{[]string{"v=TLSRPTv2; rua=mailto:a@example.com"}, nil},
		{[]string{"rua=mailto:a@example.com; v=TLSRPTv1"}, nil},
		{nil, nil},
	}
	for _, c := range cases {
		if got := parseTLSRPTRecord(c.txts); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %q, want %q", c.txts, got, c.want)
		}
	}
}

// A day's sessions make one report for the domain (RFC 8460 §4.4): an entry
// for each policy applied, with its counts, and a failure detail for each
// result type, MX and address.
func TestBuildTLSReport(t *testing.T) {
	policy := jsonList([]string{"version: STSv1", "mode: enforce", "mx: *.example.net", "max_age: 86400"})
	mx := jsonList([]string{"*.example.net"})
	sessions := []models.TLSRPTSession{
		{PolicyType: "sts", PolicyString: policy, MXHost: mx, ReceivingMX: "mx1.example.net"},
		{PolicyType: "sts", PolicyString: policy, MXHost: mx, ReceivingMX: "mx1.example.net"},
		{PolicyType: "sts", PolicyString: policy, MXHost: mx, ReceivingMX: "mx2.example.net", ReceivingIP: "203.0.113.5",
			SendingIP: "198.51.100.1", ResultType: tlsrptCertExpired, FailureReason: "x509: certificate has expired"},
		{PolicyType: "sts", PolicyString: policy, MXHost: mx, ReceivingMX: "mx2.example.net", ReceivingIP: "203.0.113.5",
			SendingIP: "198.51.100.1", ResultType: tlsrptCertExpired, FailureReason: "x509: certificate has expired"},
	}
	r, err := buildTLSReport("2026-10-23", "example.net", "sender.example", sessions)
	if err != nil {
		t.Fatal(err)
	}
	if r.DateRange != (tlsReportRange{"2026-10-23T00:00:00Z", "2026-10-23T23:59:59Z"}) || r.ContactInfo != "tlsrpt@sender.example" ||
		r.OrganizationName != "sender.example" || r.ReportID != "2026-10-23.example.net@sender.example" || len(r.Policies) != 1 {
		t.Fatalf("report %+v", r)
	}
	p := r.Policies[0]
	if p.Policy.PolicyType != "sts" || p.Policy.PolicyDomain != "example.net" || len(p.Policy.PolicyString) != 4 ||
		!reflect.DeepEqual(p.Policy.MXHost, []string{"*.example.net"}) || p.Summary != (tlsReportSummary{Successful: 2, Failed: 2}) {
		t.Errorf("policy %+v", p)
	}
	want := []tlsFailureDetail{{ResultType: tlsrptCertExpired, SendingMTAIP: "198.51.100.1", ReceivingMXHostname: "mx2.example.net",
		ReceivingIP: "203.0.113.5", FailedSessionCount: 2, FailureReasonCode: "x509: certificate has expired"}}
	if !reflect.DeepEqual(p.FailureDetails, want) {
		t.Errorf("failure details %+v, want %+v", p.FailureDetails, want)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"organization-name":"sender.example"`, `"start-datetime":"2026-10-23T00:00:00Z"`,
		`"policy-type":"sts"`, `"mx-host":["*.example.net"]`, `"total-successful-session-count":2`, `"result-type":"certificate-expired"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("report JSON lacks %s:\n%s", want, b)
		}
	}

	// A day without a policy reports no-policy-found, without a policy string
	// or MX patterns.
	r, _ = buildTLSReport("2026-10-23", "example.org", "sender.example", []models.TLSRPTSession{{PolicyType: "no-policy-found", ReceivingMX: "mx.example.org"}})
	if b, _ := json.Marshal(r.Policies[0].Policy); string(b) != `{"policy-type":"no-policy-found","policy-domain":"example.org"}` {
		t.Errorf("no-policy-found policy %s", b)
	}
	if _, err := buildTLSReport("yesterday", "example.org", "sender.example", nil); err == nil {
		t.Error("a day that is not a date was accepted")
	}
}

// The report mail is a multipart/report of report-type tlsrpt with the two
// report fields and the gzipped report attached under its file name (RFC
// 8460 §5.3).
func TestTLSReportMessage(t *testing.T) {
	msg := tlsReportMessage("tlsrpt@sender.example", "reports@example.net", "sender.example", "example.net",
		"2026-10-23.example.net@sender.example", "sender.example!example.net!1792713600!1792799999.json.gz", []byte("gz"), time.Unix(1792800000, 0))
	for _, want := range []string{
		"Subject: Report Domain: example.net Submitter: sender.example Report-ID: <2026-10-23.example.net@sender.example>\r\n",
		"TLS-Report-Domain: example.net\r\n", "TLS-Report-Submitter: sender.example\r\n",
		`Content-Type: multipart/report; report-type="tlsrpt"; boundary=`, "Content-Type: application/tlsrpt+gzip\r\n",
		`filename="sender.example!example.net!1792713600!1792799999.json.gz"`, "Z3o=\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("report mail lacks %q:\n%s", want, msg)
		}
	}
}

// A report that cannot be delivered is tried for a day after its own day
// ends (RFC 8460 §5.5), and then given up.
func TestTLSReportExpired(t *testing.T) {
	end := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	if tlsReportExpired("2026-10-23", end.Add(47*time.Hour)) || !tlsReportExpired("2026-10-23", end.Add(49*time.Hour)) || !tlsReportExpired("bad", end) {
		t.Error("the give-up time is wrong")
	}
}
