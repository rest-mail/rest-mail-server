package apiclient

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDeliverRequest_TransportSecurityRoundTrip verifies the inbound
// transport-security fields serialize with the expected JSON keys and survive a
// marshal→unmarshal round trip — the wire contract the SMTP gateway and the API
// deliver handler must agree on.
func TestDeliverRequest_TransportSecurityRoundTrip(t *testing.T) {
	tru := true
	in := &DeliverRequest{
		Address:     "user@mail1.test",
		Sender:      "sender@example.com",
		Subject:     "Hi",
		ReceivedTLS: &tru,
		TLSVersion:  "TLS1.3",
		TLSCipher:   "TLS_AES_128_GCM_SHA256",
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(raw)
	for _, want := range []string{`"received_tls":true`, `"tls_version":"TLS1.3"`, `"tls_cipher":"TLS_AES_128_GCM_SHA256"`} {
		if !strings.Contains(js, want) {
			t.Errorf("marshaled JSON missing %s\n got: %s", want, js)
		}
	}

	var out DeliverRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ReceivedTLS == nil || *out.ReceivedTLS != true {
		t.Errorf("ReceivedTLS round-trip = %v, want true", out.ReceivedTLS)
	}
	if out.TLSVersion != "TLS1.3" {
		t.Errorf("TLSVersion round-trip = %q, want TLS1.3", out.TLSVersion)
	}
	if out.TLSCipher != "TLS_AES_128_GCM_SHA256" {
		t.Errorf("TLSCipher round-trip = %q, want TLS_AES_128_GCM_SHA256", out.TLSCipher)
	}
}

// TestDeliverRequest_TransportSecurityOmitted verifies a caller that never sets
// the fields (nil ReceivedTLS, empty version/cipher) omits them entirely, so
// non inbound-MX callers stay additive on the wire and the API sees "not
// applicable".
func TestDeliverRequest_TransportSecurityOmitted(t *testing.T) {
	raw, err := json.Marshal(&DeliverRequest{Address: "user@mail1.test", Sender: "s@example.com", Subject: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(raw)
	for _, absent := range []string{"received_tls", "tls_version", "tls_cipher"} {
		if strings.Contains(js, absent) {
			t.Errorf("expected %q to be omitted, got: %s", absent, js)
		}
	}

	// A nil ReceivedTLS must decode back to nil (not false), preserving the
	// "not applicable" distinction end to end.
	var out DeliverRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ReceivedTLS != nil {
		t.Errorf("ReceivedTLS = %v, want nil when omitted", *out.ReceivedTLS)
	}
}
