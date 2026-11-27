package smtp

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Port 25 is the public MX. Issue #292 decided that AUTH is never offered there:
// peers deliver to it without credentials, and a password typed into it would
// teach users that the MX is a submission server. The refusal has to hold both
// before and after STARTTLS, because go-smtp only hides AUTH until the upgrade
// on its own, and has to cover a client that sends AUTH without reading EHLO.
func TestSMTP_InboundNeverOffersOrAcceptsAuth(t *testing.T) {
	for _, c := range []struct {
		name    string
		upgrade bool
	}{
		{"cleartext", false},
		{"after STARTTLS", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			back := newMockBackend()
			store := newMockStore()
			h := newSMTPHarnessTLS(t, back, store, false, c.upgrade) // inbound relay, as port 25

			lines, final := h.readReplyAfter("EHLO peer.test")
			if replyCode(final) != "250" {
				t.Fatalf("EHLO = %q", final)
			}
			for _, l := range append(lines, final) {
				if strings.HasPrefix(strings.ToUpper(l[4:]), "AUTH") {
					t.Errorf("port 25 EHLO advertises %q", l)
				}
			}

			enc := base64.StdEncoding.EncodeToString([]byte("\x00alice@local.test\x00secret"))
			r := h.cmd("AUTH PLAIN %s", enc)
			if code := replyCode(r); code[0] != '5' {
				t.Fatalf("AUTH on port 25 = %q, want a permanent refusal", r)
			}
			if got := back.loginCallCount(); got != 0 {
				t.Errorf("AUTH on port 25 reached the sign-in backend %d time(s)", got)
			}
		})
	}
}

// The submission control: the same EHLO after STARTTLS on a submission port does
// offer AUTH, so the test above fails if the listener kind stops mattering.
func TestSMTP_SubmissionOffersAuthAfterSTARTTLS(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	h := newSMTPHarness(t, back, store, true)

	lines, final := h.readReplyAfter("EHLO client.test")
	if replyCode(final) != "250" {
		t.Fatalf("EHLO = %q", final)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "AUTH PLAIN LOGIN") {
		t.Errorf("submission EHLO after STARTTLS does not offer AUTH:\n%s", strings.Join(lines, "\n"))
	}
}
