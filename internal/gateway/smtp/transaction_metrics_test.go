package smtp

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/rest-mail/rest-mail-server/internal/metrics"
)

// transactions is how many SMTP transactions restmail_smtp_transaction_duration_seconds
// holds with outcome.
func transactions(t *testing.T, outcome string) uint64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.SMTPTransactionDuration.WithLabelValues(outcome).(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}

func transactionCounts(t *testing.T) map[string]uint64 {
	t.Helper()
	return map[string]uint64{
		txAccepted: transactions(t, txAccepted),
		txRejected: transactions(t, txRejected),
		txAborted:  transactions(t, txAborted),
	}
}

// expectTransactions checks how each outcome moved since before.
func expectTransactions(t *testing.T, before map[string]uint64, accepted, rejected, aborted uint64) {
	t.Helper()
	after := transactionCounts(t)
	for outcome, want := range map[string]uint64{txAccepted: accepted, txRejected: rejected, txAborted: aborted} {
		if got := after[outcome] - before[outcome]; got != want {
			t.Errorf("%s transactions rose by %d, want %d", outcome, got, want)
		}
	}
}

// TestSMTPTransactionDuration: each mail transaction is observed once, from
// MAIL FROM to its end: accepted at a 250 to DATA, rejected at a refusal, and
// aborted by RSET. RSET without a transaction, and
// the reset after DATA, observe nothing.
func TestSMTPTransactionDuration(t *testing.T) {
	back := newMockBackend()
	back.local["receiver@local.test"] = true
	h := newSMTPHarness(t, back, newMockStore(), false)
	h.ehlo()

	transaction := func() {
		t.Helper()
		if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
			t.Fatal(r)
		}
		if r := h.cmd("RCPT TO:<receiver@local.test>"); replyCode(r) != "250" {
			t.Fatal(r)
		}
	}

	before := transactionCounts(t)
	transaction()
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatal(final)
	}
	if r := h.cmd("RSET"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	expectTransactions(t, before, 1, 0, 0)

	before = transactionCounts(t)
	transaction()
	if r := h.cmd("RSET"); replyCode(r) != "250" {
		t.Fatal(r)
	}
	expectTransactions(t, before, 0, 0, 1)

	// The recipient goes away before DATA: the message is refused.
	before = transactionCounts(t)
	transaction()
	back.mu.Lock()
	back.local["receiver@local.test"] = false
	back.mu.Unlock()
	if final := h.dataBody(testBody); replyCode(final) == "250" {
		t.Fatalf("DATA for a revoked recipient: %q", final)
	}
	expectTransactions(t, before, 0, 1, 0)
}

// TestEndTransaction_AbortedOnce: the connection closing ends an open
// transaction as aborted, and only once; with none open it observes nothing.
func TestEndTransaction_AbortedOnce(t *testing.T) {
	s := &session{}
	before := transactionCounts(t)
	s.Reset()
	_ = s.Logout()
	expectTransactions(t, before, 0, 0, 0)

	s.txStart = s.txStart.Add(1) // any non-zero start: a transaction is open
	_ = s.Logout()
	_ = s.Logout()
	expectTransactions(t, before, 0, 0, 1)
}
