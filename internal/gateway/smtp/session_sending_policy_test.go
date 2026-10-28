package smtp

import (
	"errors"
	"testing"
)

// An authenticated submission follows the domain sending policy the send API
// enforces (#144): SMTP is a way into the same system, not a way round its
// rules. A bulk domain's mail needs a List-Unsubscribe header, a transactional
// domain's must not carry one, and a failed policy lookup is a temporary
// refusal. A refused message is neither queued nor delivered.
func TestSMTP_SubmissionFollowsTheDomainSendingPolicy(t *testing.T) {
	const unsubscribe = "List-Unsubscribe: <https://example.com/unsubscribe>\r\n"
	for _, tc := range []struct {
		name, policy, headers, code string
		lookupErr                   error
	}{
		{"bulk without List-Unsubscribe", "bulk", "", "550", nil},
		{"bulk with List-Unsubscribe", "bulk", unsubscribe, "250", nil},
		{"transactional with List-Unsubscribe", "transactional", unsubscribe, "550", nil},
		{"transactional without", "transactional", "", "250", nil},
		{"no policy", "", "", "250", nil},
		{"policy unreadable", "", "", "451", errors.New("database down")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			back := newMockBackend()
			store := newMockStore()
			store.policies = map[string]string{"example.com": tc.policy}
			store.policyErr = tc.lookupErr

			h := newSMTPHarness(t, back, store, true) // submission
			h.ehlo()
			if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
				t.Fatalf("AUTH = %q", r)
			}
			if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
				t.Fatalf("MAIL FROM = %q", r)
			}
			if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
				t.Fatalf("RCPT = %q", r)
			}
			body := "Subject: Hi\r\nFrom: Alice <alice@example.com>\r\n" + tc.headers + "\r\nHello there.\r\n"
			if final := h.dataBody(body); replyCode(final) != tc.code {
				t.Fatalf("want %s, got %q", tc.code, final)
			}
			want := 0
			if tc.code == "250" {
				want = 1
			}
			if got := len(store.queued()); got != want {
				t.Errorf("queued %d, want %d", got, want)
			}
		})
	}
}
