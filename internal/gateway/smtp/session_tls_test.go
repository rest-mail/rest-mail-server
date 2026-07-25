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

// TestSMTP_InboundPlaintext_CapturesReceivedTLS drives a full plaintext inbound
// (port 25) DATA transaction over the transcript harness and asserts the
// DeliverRequest carries ReceivedTLS = false (non-nil) — the connection was not
// encrypted, but the message is still counted as an inbound-MX arrival.
func TestSMTP_InboundPlaintext_CapturesReceivedTLS(t *testing.T) {
	back := newMockBackend()
	back.local["bob@local.test"] = true
	store := newMockStore()

	h := newSMTPHarness(t, back, store, false) // inbound (port 25), plaintext pipe
	h.ehlo()
	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q, want 250", final)
	}

	req := back.lastDeliverReq()
	if req == nil {
		t.Fatal("no DeliverRequest captured")
	}
	if req.ReceivedTLS == nil {
		t.Fatal("ReceivedTLS = nil, want non-nil false for inbound-MX plaintext")
	}
	if *req.ReceivedTLS {
		t.Error("ReceivedTLS = true, want false over a plaintext connection")
	}
	if req.TLSVersion != "" {
		t.Errorf("TLSVersion = %q, want empty for plaintext", req.TLSVersion)
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
