package smtp

import (
	"strings"
	"testing"
)

// TestSMTP_DeliveryRefusalBecomesSMTPReply proves the gateway rewrites the REST statuses
// deliverMessage answers into SMTP replies (#1664): 403 rejected is refused for good as
// 550 5.7.1, and 503 deferred asks the sender to retry with 451 4.3.0. The API never
// answers with an SMTP code, so a 550 or 451 from it is no refusal the gateway knows and
// gets the generic temporary failure.
func TestSMTP_DeliveryRefusalBecomesSMTPReply(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		code     string
		enhanced string
		text     string
	}{
		{"rejected", 403, "550", "5.7.1", "Rejected by policy"},
		{"deferred", 503, "451", "4.3.0", "Try again later"},
		{"smtp 550 is not a REST status", 550, "451", "4.3.0", "Temporary delivery failure"},
		{"smtp 451 is not a REST status", 451, "451", "4.3.0", "Temporary delivery failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			back := newMockBackend()
			back.local["rcpt@local.test"] = true
			back.deliverStatus["rcpt@local.test"] = tc.status
			h := newSMTPHarness(t, back, newMockStore(), false) // inbound (port 25)
			h.ehlo()
			if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
				t.Fatalf("MAIL FROM = %q", r)
			}
			if r := h.cmd("RCPT TO:<rcpt@local.test>"); replyCode(r) != "250" {
				t.Fatalf("RCPT = %q", r)
			}
			final := h.dataBody(testBody)
			if replyCode(final) != tc.code || !strings.Contains(final, tc.enhanced) || !strings.Contains(final, tc.text) {
				t.Errorf("HTTP %d: DATA reply = %q, want %s %s %s", tc.status, final, tc.code, tc.enhanced, tc.text)
			}
			if d := back.deliveredTo(); len(d) != 0 {
				t.Errorf("delivered = %v, want none", d)
			}
		})
	}
}
