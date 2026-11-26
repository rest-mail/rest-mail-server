package smtp

import (
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// retiredDomainBody is the address check's 503 for a recipient on a retired domain.
const retiredDomainBody = `{"error":{"code":"deferred","message":"The recipient's domain is out of service, retry later"}}`

func retiredDomainCheck(backend *mockBackend, passes int) *answeredAddressCheck {
	return &answeredAddressCheck{mockBackend: backend, address: "user@retired.test", passes: passes, status: 503, body: retiredDomainBody}
}

func wantTemporaryReply(t *testing.T, step, reply string) {
	t.Helper()
	if replyCode(reply) != "451" || !strings.Contains(reply, "4.3.0") || !strings.Contains(reply, "Try again later") {
		t.Fatalf("%s = %q, want 451 4.3.0 Try again later", step, reply)
	}
}

// A sending server hears that the mail must wait, so it keeps the message (#1870).
func TestSMTPRetiredDomainRecipientIsDeferredAtRCPT(t *testing.T) {
	backend, store := newMockBackend(), newMockStore()
	harness := newSMTPHarness(t, backend, store, false, func(server *Server) { server.api = retiredDomainCheck(backend, 0) })
	harness.ehlo()
	if reply := harness.cmd("MAIL FROM:<sender@remote.test>"); replyCode(reply) != "250" {
		t.Fatal(reply)
	}
	wantTemporaryReply(t, "RCPT", harness.cmd("RCPT TO:<user@retired.test>"))
	if delivered, queued := backend.deliveredTo(), store.queued(); len(delivered) != 0 || len(queued) != 0 {
		t.Fatalf("delivered %v, queued %v", delivered, queued)
	}
}

// A signed-in sender's message is outbound mail wherever it is addressed: it goes on the
// queue, which retries it, rather than failing in the sender's mail client.
func TestSMTPSubmissionToARetiredDomainIsQueued(t *testing.T) {
	backend, store := newMockBackend(), newMockStore()
	harness := newSMTPHarness(t, backend, store, true, func(server *Server) { server.api = retiredDomainCheck(backend, 0) })
	harness.ehlo()
	if reply := harness.authPlain(backend.user, backend.pass); replyCode(reply) != "235" {
		t.Fatal(reply)
	}
	if reply := harness.cmd("MAIL FROM:<%s>", backend.user); replyCode(reply) != "250" {
		t.Fatal(reply)
	}
	if reply := harness.cmd("RCPT TO:<user@retired.test>"); replyCode(reply) != "250" {
		t.Fatalf("RCPT = %q", reply)
	}
	if reply := harness.dataBody(localSubmissionBody); replyCode(reply) != "250" {
		t.Fatalf("DATA = %q", reply)
	}
	if queued := store.queued(); len(queued) != 1 || queued[0].Recipient != "user@retired.test" {
		t.Fatalf("queued = %+v", queued)
	}
}

// goneAfterRCPT stops being a recipient once RCPT has accepted it.
type goneAfterRCPT struct {
	*answeredAddressCheck
	address string
	seen    int
}

func (backend *goneAfterRCPT) CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error) {
	if address == backend.address {
		backend.seen++
		if backend.seen > 1 {
			response := &apiclient.MailboxCheckResponse{}
			response.Data.Address = address
			return response, nil
		}
	}
	return backend.answeredAddressCheck.CheckMailbox(address)
}

// A domain retired between RCPT and DATA still gets a temporary reply, even beside a
// recipient refused for good: the transaction has one reply, and a 5xx would bounce the
// message that is meant to wait.
func TestSMTPDomainRetiredAfterRCPTKeepsTheReplyTemporary(t *testing.T) {
	for _, order := range [][]string{
		{"user@retired.test", "gone@local.test"},
		{"gone@local.test", "user@retired.test"},
	} {
		t.Run(order[0], func(t *testing.T) {
			backend, store := newMockBackend(), newMockStore()
			backend.local["user@retired.test"], backend.local["gone@local.test"] = true, true
			check := &goneAfterRCPT{answeredAddressCheck: retiredDomainCheck(backend, 1), address: "gone@local.test"}
			harness := newSMTPHarness(t, backend, store, false, func(server *Server) { server.api = check })
			harness.ehlo()
			if reply := harness.cmd("MAIL FROM:<sender@remote.test>"); replyCode(reply) != "250" {
				t.Fatal(reply)
			}
			for _, recipient := range order {
				if reply := harness.cmd("RCPT TO:<%s>", recipient); replyCode(reply) != "250" {
					t.Fatalf("RCPT %s = %q", recipient, reply)
				}
			}
			wantTemporaryReply(t, "DATA", harness.dataBody(testBody))
			if delivered := backend.deliveredTo(); len(delivered) != 0 {
				t.Fatalf("delivered %v", delivered)
			}
		})
	}
}

// A 503 that is not the API's deferral stays the unexpected failure it was.
func TestSMTPUnexplained503IsNotARecipientDeferral(t *testing.T) {
	if recipientDeferred(&apiclient.APIError{StatusCode: 503, Body: "service unavailable"}) {
		t.Fatal("a 503 with no deferred code was read as a deferral")
	}
	if !recipientDeferred(&apiclient.APIError{StatusCode: 503, Body: retiredDomainBody}) {
		t.Fatal("the retired-domain answer was not read as a deferral")
	}
}
