package smtp

import (
	"strings"
	"testing"
)

// REQUIRETLS is offered on a TLS session only (RFC 8689 §2), and a message that arrives with
// it is tagged (§4.1): queued for a remote recipient with RequireTLS, delivered locally with
// require_tls, so a copy sent on later goes with TLS required too. The tag is the
// transaction's: the next MAIL without it is untagged (#724).
func TestSMTP_REQUIRETLSTagsTheMessage(t *testing.T) {
	back, store := newMockBackend(), newMockStore()
	back.local["bob@example.com"] = true
	h := newSMTPHarness(t, back, store, true)
	if caps := ehloCapabilities(h); !strings.Contains(caps, "REQUIRETLS") {
		t.Fatalf("EHLO over TLS does not offer REQUIRETLS:\n%s", caps)
	}
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatal(r)
	}
	send := func(param string) {
		t.Helper()
		if r := h.cmd("MAIL FROM:<%s>%s", back.user, param); replyCode(r) != "250" {
			t.Fatalf("MAIL%s: %q", param, r)
		}
		for _, rcpt := range []string{"bob@example.com", "carol@elsewhere.test"} {
			if r := h.cmd("RCPT TO:<%s>", rcpt); replyCode(r) != "250" {
				t.Fatalf("RCPT %s: %q", rcpt, r)
			}
		}
		if r := h.dataBody("From: " + back.user + "\r\nTo: bob@example.com\r\nSubject: tagged\r\n\r\nhello\r\n"); replyCode(r) != "250" {
			t.Fatalf("DATA: %q", r)
		}
	}
	send(" REQUIRETLS")
	send("")

	queued := store.queued()
	if len(queued) != 2 || !queued[0].RequireTLS || queued[1].RequireTLS {
		t.Fatalf("queued %+v, want the first tagged and the second not", queued)
	}
	back.mu.Lock()
	reqs := back.deliverReqs
	back.mu.Unlock()
	if len(reqs) != 2 || !reqs[0].RequireTLS || reqs[1].RequireTLS {
		t.Fatalf("local deliveries %d, tagged %v, want the first tagged and the second not", len(reqs), []bool{len(reqs) > 0 && reqs[0].RequireTLS, len(reqs) > 1 && reqs[1].RequireTLS})
	}
}

// On a cleartext session REQUIRETLS is neither offered nor taken (RFC 8689 §2).
func TestSMTP_REQUIRETLSNeedsTLS(t *testing.T) {
	back, store := newMockBackend(), newMockStore()
	back.local["bob@example.com"] = true
	h := newCleartextSMTPHarness(t, back, store, false)
	if caps := ehloCapabilities(h); strings.Contains(caps, "REQUIRETLS") {
		t.Fatalf("EHLO in cleartext offers REQUIRETLS:\n%s", caps)
	}
	if r := h.cmd("MAIL FROM:<alice@remote.test> REQUIRETLS"); !strings.HasPrefix(r, "530 ") {
		t.Fatalf("MAIL ... REQUIRETLS in cleartext: %q, want 530", r)
	}
}

// ehloCapabilities is the server's EHLO reply, every line.
func ehloCapabilities(h *smtpHarness) string {
	h.t.Helper()
	lines, final := h.readReplyAfter("EHLO client.test")
	if replyCode(final) != "250" {
		h.t.Fatalf("EHLO = %q", final)
	}
	return strings.Join(append(lines, final), "\n")
}
