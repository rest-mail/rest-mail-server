package handlers

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/restmail/restmail/internal/metrics"
)

// TestReceivedTLSFlag pins the Envelope.TLS derivation: a nil ReceivedTLS (non
// inbound-MX) is not TLS; an explicit pointer reflects its value.
func TestReceivedTLSFlag(t *testing.T) {
	tru, fls := true, false
	cases := []struct {
		name string
		in   *bool
		want bool
	}{
		{"nil (non inbound-MX)", nil, false},
		{"tls", &tru, true},
		{"plaintext", &fls, false},
	}
	for _, c := range cases {
		if got := receivedTLSFlag(c.in); got != c.want {
			t.Errorf("%s: receivedTLSFlag = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRecordInboundReceived verifies messages_received{transport} increments
// tls vs plaintext correctly and is NOT incremented for a nil (non inbound-MX)
// delivery. The label is strictly bounded to tls|plaintext.
func TestRecordInboundReceived(t *testing.T) {
	tru, fls := true, false

	beforeTLS := testutil.ToFloat64(metrics.MessagesReceived.WithLabelValues("tls"))
	beforePlain := testutil.ToFloat64(metrics.MessagesReceived.WithLabelValues("plaintext"))

	// Inbound-MX over TLS → tls.
	recordInboundReceived(&tru)
	// Inbound-MX plaintext → plaintext.
	recordInboundReceived(&fls)
	// Non inbound-MX (IMAP APPEND / local send) → not counted at all.
	recordInboundReceived(nil)

	if got := testutil.ToFloat64(metrics.MessagesReceived.WithLabelValues("tls")) - beforeTLS; got != 1 {
		t.Errorf("messages_received{tls} delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.MessagesReceived.WithLabelValues("plaintext")) - beforePlain; got != 1 {
		t.Errorf("messages_received{plaintext} delta = %v, want 1", got)
	}

	// The nil delivery must not have created any third series; only tls|plaintext
	// exist. 2 label-pairs × the CounterVec is the full metric family.
	if n := testutil.CollectAndCount(metrics.MessagesReceived); n != 2 {
		t.Errorf("messages_received series count = %d, want 2 (tls, plaintext only)", n)
	}
}
