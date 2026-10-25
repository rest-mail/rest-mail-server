package queue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-mtasts"
	"github.com/restmail/restmail/internal/dane"
)

// eeRecordFor is a DANE-EE SPKI SHA-256 record (3 1 1) for a test peer's certificate.
func eeRecordFor(t *testing.T, der []byte) dane.TLSA {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return dane.TLSA{Usage: dane.UsageDANEEE, Selector: 1, MatchingType: 1, Data: sum[:]}
}

func daneTestWorker() *Worker {
	return &Worker{hostname: "sender.test", allowPrivateDest: true, sendDeadline: defaultSendDeadline}
}

func startedMessage(commands []string) bool {
	return slices.ContainsFunc(commands, func(c string) bool { return strings.HasPrefix(c, "MAIL") })
}

// A certificate nothing in the Web PKI vouches for is authenticated by a matching
// DANE-EE record (RFC 7672 §3.1.1), and the delivery is counted as authenticated.
func TestDeliverSMTP_DANEMatchingRecordAuthenticates(t *testing.T) {
	cert, _ := relayTestCertificate(t, false) // self-signed: not in the system roots
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true})
	records := []dane.TLSA{eeRecordFor(t, cert.Certificate[0])}
	authenticated := transportCount(transportAuthenticated)

	err := daneTestWorker().deliverSMTPWith(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, tlsTestItem(), nil, nil, records)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	result := finish()
	if !slices.Contains(result.commands, "STARTTLS") || !strings.Contains(result.data, "hello") {
		t.Fatalf("not delivered over TLS: commands %v", result.commands)
	}
	if got := transportCount(transportAuthenticated) - authenticated; got != 1 {
		t.Errorf("authenticated deliveries rose by %v, want 1", got)
	}
}

// A chain no TLSA record matches is a temporary failure: no message is started, and
// the queue does not retry the host in the clear as opportunistic TLS would.
func TestDeliverSMTP_DANEMismatchDefersWithoutCleartext(t *testing.T) {
	cert, _ := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true})
	wrong := dane.TLSA{Usage: dane.UsageDANEEE, Selector: 1, MatchingType: 1, Data: bytes.Repeat([]byte{7}, 32)}

	err := daneTestWorker().deliverSMTPWith(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, tlsTestItem(), nil, nil, []dane.TLSA{wrong})
	var se *SMTPError
	if !errors.As(err, &se) || se.Code != 451 || se.Enhanced != "4.7.5" {
		t.Fatalf("got %v, want a 451 4.7.5 DANE refusal", err)
	}
	if result := finish(); startedMessage(result.commands) {
		t.Fatalf("a message was started: %v", result.commands)
	}
}

// A DANE host that offers no STARTTLS gets nothing: TLS is mandatory for it.
func TestDeliverSMTP_DANEWithoutSTARTTLSDefers(t *testing.T) {
	cert, _ := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true, noSTARTTLS: true})
	records := []dane.TLSA{eeRecordFor(t, cert.Certificate[0])}

	err := daneTestWorker().deliverSMTPWith(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, tlsTestItem(), nil, nil, records)
	var se *SMTPError
	if !errors.As(err, &se) || se.Code != 451 || !strings.Contains(se.Message, "STARTTLS") {
		t.Fatalf("got %v, want a 451 DANE refusal naming STARTTLS", err)
	}
	if result := finish(); startedMessage(result.commands) {
		t.Fatalf("a message was started in the clear: %v", result.commands)
	}
}

// The TLSA records decide a DANE host's TLS, so an enforce-mode MTA-STS policy that
// would refuse the self-signed certificate does not apply to it (RFC 8461 §2).
func TestDeliverSMTP_DANEHostIgnoresMTASTS(t *testing.T) {
	cert, _ := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true})
	w := daneTestWorker()
	w.stsEnforce = true
	policy := &mtasts.Policy{Mode: mtasts.ModeEnforce, MX: []string{settings.host}, MaxAge: 86400}
	records := []dane.TLSA{eeRecordFor(t, cert.Certificate[0])}

	if err := w.deliverSMTPWith(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, tlsTestItem(), policy, nil, records); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if result := finish(); !strings.Contains(result.data, "hello") {
		t.Fatalf("not delivered: %v", result.commands)
	}
}

// A message that requires TLS may go to a DANE host: DNSSEC authenticates its
// certificate, which REQUIRETLS accepts (RFC 8689 §4.2.1), with no MTA-STS policy.
func TestDeliverSMTP_DANESatisfiesREQUIRETLS(t *testing.T) {
	cert, _ := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true, requireTLS: true})
	item := tlsTestItem()
	item.RequireTLS = true
	records := []dane.TLSA{eeRecordFor(t, cert.Certificate[0])}

	if err := daneTestWorker().deliverSMTPWith(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, item, nil, nil, records); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	result := finish()
	if !slices.ContainsFunc(result.commands, func(c string) bool { return strings.HasPrefix(c, "MAIL") && strings.Contains(c, "REQUIRETLS") }) {
		t.Fatalf("MAIL FROM carried no REQUIRETLS: %v", result.commands)
	}
}

type fakeDANE struct {
	mx      dane.Security
	mxErr   error
	tlsa    []dane.TLSA
	tlsaSec dane.Security
	tlsaErr error
}

func (f fakeDANE) MXSecure(context.Context, string) (dane.Security, error) { return f.mx, f.mxErr }
func (f fakeDANE) TLSA(context.Context, string, int) ([]dane.TLSA, dane.Security, error) {
	return f.tlsa, f.tlsaSec, f.tlsaErr
}

func TestDANEMXSecure(t *testing.T) {
	if secure, err := daneTestWorker().daneMXSecure("example.test"); secure || err != nil {
		t.Fatalf("without a resolver DANE must be off: %v %v", secure, err)
	}
	for name, tc := range map[string]struct {
		lookup     fakeDANE
		secure     bool
		wantDefers bool
	}{
		"secure":    {fakeDANE{mx: dane.Secure}, true, false},
		"insecure":  {fakeDANE{mx: dane.Insecure}, false, false},
		"bogus":     {fakeDANE{mx: dane.Bogus}, false, true},
		"no answer": {fakeDANE{mxErr: errors.New("timeout")}, false, true},
	} {
		w := daneTestWorker()
		w.dane = tc.lookup
		secure, err := w.daneMXSecure("example.test")
		var se *SMTPError
		if secure != tc.secure || tc.wantDefers != (errors.As(err, &se) && se.Code == 451) {
			t.Errorf("%s: secure %v, err %v", name, secure, err)
		}
	}
}

func TestDANERecordsPerHost(t *testing.T) {
	ee := dane.TLSA{Usage: dane.UsageDANEEE, Selector: 1, MatchingType: 1, Data: bytes.Repeat([]byte{1}, 32)}
	pkix := dane.TLSA{Usage: 1, Selector: 1, MatchingType: 1, Data: bytes.Repeat([]byte{2}, 32)}
	for name, tc := range map[string]struct {
		lookup     fakeDANE
		records    int
		wantDefers bool
	}{
		"usable":           {fakeDANE{tlsa: []dane.TLSA{ee, pkix}, tlsaSec: dane.Secure}, 1, false},
		"only PKIX usages": {fakeDANE{tlsa: []dane.TLSA{pkix}, tlsaSec: dane.Secure}, 0, false},
		"validated none":   {fakeDANE{tlsaSec: dane.Secure}, 0, false},
		"insecure":         {fakeDANE{tlsa: []dane.TLSA{ee}, tlsaSec: dane.Insecure}, 0, false},
		"bogus":            {fakeDANE{tlsaSec: dane.Bogus}, 0, true},
		"no answer":        {fakeDANE{tlsaErr: errors.New("timeout")}, 0, true},
	} {
		w := daneTestWorker()
		w.dane = tc.lookup
		records, err := w.daneRecords("mx.example.test")
		var se *SMTPError
		if len(records) != tc.records || tc.wantDefers != (errors.As(err, &se) && se.Code == 451) {
			t.Errorf("%s: %d records, err %v", name, len(records), err)
		}
		if tc.records == 0 && records != nil {
			t.Errorf("%s: DANE must not apply (nil records), got %v", name, records)
		}
	}
}
