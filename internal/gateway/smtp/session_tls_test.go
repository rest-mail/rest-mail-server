package smtp

import (
	"crypto/tls"
	"testing"
)

// TestTLSVersionName maps crypto/tls version constants to stable labels and
// yields "" for anything unrecognized (so an internal constant never leaks into
// stored metrics).
func TestTLSVersionName(t *testing.T) {
	cases := []struct {
		in   uint16
		want string
	}{
		{tls.VersionTLS13, "TLS1.3"},
		{tls.VersionTLS12, "TLS1.2"},
		{tls.VersionTLS11, "TLS1.1"},
		{tls.VersionTLS10, "TLS1.0"},
		{0x0300, ""}, // SSLv3: not represented
		{0, ""},
	}
	for _, c := range cases {
		if got := tlsVersionName(c.in); got != c.want {
			t.Errorf("tlsVersionName(%#x) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestInboundTransportSecurity_TLS verifies the inbound-MX encrypted path
// records a non-nil true flag plus best-effort version/cipher.
func TestInboundTransportSecurity_TLS(t *testing.T) {
	state := tls.ConnectionState{
		Version:     tls.VersionTLS13,
		CipherSuite: tls.TLS_AES_128_GCM_SHA256,
	}
	received, version, cipher := inboundTransportSecurity(false, state, true)
	if received == nil || !*received {
		t.Fatalf("received = %v, want non-nil true", received)
	}
	if version != "TLS1.3" {
		t.Errorf("version = %q, want TLS1.3", version)
	}
	if cipher != "TLS_AES_128_GCM_SHA256" {
		t.Errorf("cipher = %q, want TLS_AES_128_GCM_SHA256", cipher)
	}
}

// TestInboundTransportSecurity_Plaintext verifies the inbound-MX plaintext path
// records a non-nil FALSE flag (so it joins the inbound-MX denominator and is
// counted as plaintext) with empty version/cipher.
func TestInboundTransportSecurity_Plaintext(t *testing.T) {
	received, version, cipher := inboundTransportSecurity(false, tls.ConnectionState{}, false)
	if received == nil {
		t.Fatal("received = nil, want non-nil false (plaintext inbound MX must be counted)")
	}
	if *received {
		t.Error("received = true, want false for a plaintext connection")
	}
	if version != "" || cipher != "" {
		t.Errorf("version/cipher = %q/%q, want empty for plaintext", version, cipher)
	}
}

// TestInboundTransportSecurity_Submission verifies the authenticated submission
// path (587/465) is NOT recorded as inbound-MX: nil flag, persisted as NULL /
// "not applicable", even when the connection is encrypted.
func TestInboundTransportSecurity_Submission(t *testing.T) {
	state := tls.ConnectionState{Version: tls.VersionTLS12, CipherSuite: tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
	received, version, cipher := inboundTransportSecurity(true, state, true)
	if received != nil {
		t.Errorf("received = %v, want nil (submission is not inbound-MX)", *received)
	}
	if version != "" || cipher != "" {
		t.Errorf("version/cipher = %q/%q, want empty on submission", version, cipher)
	}
}

// TestSMTP_InboundPlaintext_DeliversWithPlaintextFlag drives a whole plaintext
// inbound (port 25) transaction and asserts the mail is taken and recorded as having
// arrived in the clear.
//
// Port 25 accepts plaintext because a publicly-referenced SMTP server must (RFC 3207
// §4, issue #292). What matters then is that the arrival is not quietly indistinguishable
// from an encrypted one: ReceivedTLS is non-nil false, so the message joins the
// inbound-MX denominator as plaintext and the pipeline can act on it.
//
// Between #279 and #292 this test asserted the opposite — that nothing was accepted
// before STARTTLS — which made rest-mail unreachable for senders that do not upgrade.
func TestSMTP_InboundPlaintext_DeliversWithPlaintextFlag(t *testing.T) {
	back := newMockBackend()
	back.local["bob@local.test"] = true
	store := newMockStore()

	h := newCleartextSMTPHarness(t, back, store, false) // inbound (port 25), not upgraded
	h.ehlo()
	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q, want 250 on plaintext port 25", r)
	}
	if r := h.cmd("RCPT TO:<bob@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT TO = %q, want 250", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q, want 250", final)
	}

	req := back.lastDeliverReq()
	if req == nil {
		t.Fatal("a plaintext arrival produced no delivery")
	}
	if req.ReceivedTLS == nil {
		t.Fatal("ReceivedTLS = nil, want non-nil false so the arrival counts as plaintext")
	}
	if *req.ReceivedTLS {
		t.Error("ReceivedTLS = true for a connection that never used STARTTLS")
	}
	if req.TLSVersion != "" || req.TLSCipher != "" {
		t.Errorf("version/cipher = %q/%q, want empty for a plaintext arrival", req.TLSVersion, req.TLSCipher)
	}
	if got := back.deliveredTo(); len(got) != 1 || got[0] != "bob@local.test" {
		t.Errorf("delivered = %v, want [bob@local.test]", got)
	}
}

// TestSMTP_Submission_NoReceivedTLS drives an authenticated submission delivery
// to a local recipient and asserts ReceivedTLS is left nil — submission is not
// part of inbound-MX transport monitoring.
func TestSMTP_Submission_NoReceivedTLS(t *testing.T) {
	back := newMockBackend()
	back.local["bob@example.com"] = true
	store := newMockStore()
	// testBody's From: header (sender@remote.test) is an authorized linked
	// address, so the #181 From-header check accepts the submission.
	store.authorized["sender@remote.test"] = true

	h := newSMTPHarness(t, back, store, true) // submission
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" { // self sender
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@example.com>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q, want 250", final)
	}

	req := back.lastDeliverReq()
	if req == nil {
		t.Fatal("no DeliverRequest captured")
	}
	if req.ReceivedTLS != nil {
		t.Errorf("ReceivedTLS = %v, want nil on submission", *req.ReceivedTLS)
	}
}
