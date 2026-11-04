package smtp

import (
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// aliasRefusedBody is what GET /api/mailboxes answers with its 403 for a reserved alias
// that cannot route.
const aliasRefusedBody = `{"error":{"code":"rejected","message":"` + rmail.AliasRoutingRefusal + `"}}`

// answeredAddressCheck answers the address check for one address with an API error,
// once the first passes calls have gone through to the mock as usual.
type answeredAddressCheck struct {
	*mockBackend
	address string
	passes  int
	status  int
	body    string
	calls   int
}

func (backend *answeredAddressCheck) CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error) {
	if address == backend.address {
		backend.calls++
		if backend.calls > backend.passes {
			return nil, &apiclient.APIError{StatusCode: backend.status, Body: backend.body}
		}
	}
	return backend.mockBackend.CheckMailbox(address)
}

type lateAliasRefusal struct{ *mockBackend }

func (backend *lateAliasRefusal) DeliverMessageFrom(request *apiclient.DeliverRequest, content apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	if request.Address == "alias@local.test" {
		return nil, &apiclient.APIError{StatusCode: 403, Body: aliasRefusedBody}
	}
	return backend.mockBackend.DeliverMessageFrom(request, content)
}

func TestSMTPAliasFailureIsNotHiddenByPartialSuccess(t *testing.T) {
	backend := newMockBackend()
	backend.local["mailbox@local.test"], backend.local["alias@local.test"] = true, true
	harness := newSMTPHarness(t, backend, newMockStore(), false, func(server *Server) { server.api = &lateAliasRefusal{mockBackend: backend} })
	harness.ehlo()
	for _, command := range []string{"MAIL FROM:<sender@remote.test>", "RCPT TO:<mailbox@local.test>", "RCPT TO:<alias@local.test>"} {
		if reply := harness.cmd(command); replyCode(reply) != "250" {
			t.Fatal(reply)
		}
	}
	if reply := harness.dataBody(testBody); replyCode(reply) != "550" || !strings.Contains(reply, "Alias delivery is not available for this recipient.") {
		t.Fatalf("alias failure hidden: %s", reply)
	}
	if delivered := backend.deliveredTo(); len(delivered) != 1 || delivered[0] != "mailbox@local.test" {
		t.Fatalf("prior committed delivery = %v", delivered)
	}
}

// The API refuses with REST semantics and the gateway owns the SMTP answer: the address
// check's 403 reaches a sending server as a permanent 550 5.1.1 with the fixed reason.
func TestSMTPAliasRoutingRefusalIsPermanentAtRCPT(t *testing.T) {
	backend := newMockBackend()
	check := &answeredAddressCheck{mockBackend: backend, address: "alias@local.test", status: 403, body: aliasRefusedBody}
	harness := newSMTPHarness(t, backend, newMockStore(), false, func(server *Server) { server.api = check })
	harness.ehlo()
	if reply := harness.cmd("MAIL FROM:<sender@remote.test>"); replyCode(reply) != "250" {
		t.Fatal(reply)
	}
	reply := harness.cmd("RCPT TO:<alias@local.test>")
	if replyCode(reply) != "550" || !strings.Contains(reply, "5.1.1") || !strings.Contains(reply, rmail.AliasRoutingRefusal) {
		t.Fatalf("RCPT = %q, want a permanent 550 5.1.1 with the routing reason", reply)
	}
}

// An ordinary not-found is a 200 with exists false, a different answer from the refusal.
// A sending server sees the difference, and authenticated submission still routes it out.
func TestSMTPNotFoundStaysDistinctFromAliasRoutingRefusal(t *testing.T) {
	inbound := newSMTPHarness(t, newMockBackend(), newMockStore(), false)
	inbound.ehlo()
	if reply := inbound.cmd("MAIL FROM:<sender@remote.test>"); replyCode(reply) != "250" {
		t.Fatal(reply)
	}
	reply := inbound.cmd("RCPT TO:<nobody@local.test>")
	if replyCode(reply) != "550" || !strings.Contains(reply, "No such user") || strings.Contains(reply, rmail.AliasRoutingRefusal) {
		t.Fatalf("inbound not-found RCPT = %q", reply)
	}

	backend := newMockBackend()
	submission := newSMTPHarness(t, backend, newMockStore(), true, func(server *Server) {
		server.api = &answeredAddressCheck{mockBackend: backend, address: "alias@local.test", status: 403, body: aliasRefusedBody}
	})
	submission.ehlo()
	if reply := submission.authPlain(backend.user, backend.pass); replyCode(reply) != "235" {
		t.Fatal(reply)
	}
	if reply := submission.cmd("MAIL FROM:<%s>", backend.user); replyCode(reply) != "250" {
		t.Fatal(reply)
	}
	if reply := submission.cmd("RCPT TO:<someone@remote.test>"); replyCode(reply) != "250" {
		t.Fatalf("submission to a not-found address must route outbound: %q", reply)
	}
	if reply := submission.cmd("RCPT TO:<alias@local.test>"); replyCode(reply) != "550" || !strings.Contains(reply, rmail.AliasRoutingRefusal) {
		t.Fatalf("submission to a refused alias must never route outbound: %q", reply)
	}
}

// The recheck before DATA hears the same 403 when a route breaks after RCPT: the whole
// transaction is refused and nothing is delivered or queued.
func TestSMTPAliasRoutingRefusalAtDATAStoresNothing(t *testing.T) {
	backend, store := newMockBackend(), newMockStore()
	backend.local["mailbox@local.test"], backend.local["alias@local.test"] = true, true
	check := &answeredAddressCheck{mockBackend: backend, address: "alias@local.test", passes: 1, status: 403, body: aliasRefusedBody}
	harness := newSMTPHarness(t, backend, store, false, func(server *Server) { server.api = check })
	harness.ehlo()
	for _, command := range []string{"MAIL FROM:<sender@remote.test>", "RCPT TO:<mailbox@local.test>", "RCPT TO:<alias@local.test>"} {
		if reply := harness.cmd(command); replyCode(reply) != "250" {
			t.Fatal(reply)
		}
	}
	if reply := harness.dataBody(testBody); replyCode(reply) != "550" || !strings.Contains(reply, rmail.AliasRoutingRefusal) {
		t.Fatalf("DATA = %q, want a permanent 550 with the routing reason", reply)
	}
	if delivered, queued := backend.deliveredTo(), store.queued(); len(delivered) != 0 || len(queued) != 0 {
		t.Fatalf("refused transaction stored something: delivered %v, queued %v", delivered, queued)
	}
}

// Any other 403 says nothing about the recipient, so it stays a temporary failure: a
// sending server retries instead of bouncing mail over a fault on this side.
func TestSMTPOtherForbiddenAddressCheckStaysTemporary(t *testing.T) {
	for _, body := range []string{`{"error":{"code":"forbidden","message":"Forbidden"}}`, `<html>403</html>`, ``} {
		backend := newMockBackend()
		check := &answeredAddressCheck{mockBackend: backend, address: "alias@local.test", status: 403, body: body}
		harness := newSMTPHarness(t, backend, newMockStore(), false, func(server *Server) { server.api = check })
		harness.ehlo()
		if reply := harness.cmd("MAIL FROM:<sender@remote.test>"); replyCode(reply) != "250" {
			t.Fatal(reply)
		}
		if reply := harness.cmd("RCPT TO:<alias@local.test>"); replyCode(reply) != "451" {
			t.Fatalf("403 %q at RCPT = %q, want 451", body, reply)
		}
	}
}

func TestAliasRoutingRefusalRecognisesOnlyTheRefusal(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		want   bool
	}{
		{403, aliasRefusedBody, true},
		// Every route answers a refused alias with 403 now, so a 550 is not this refusal.
		{550, aliasRefusedBody, false},
		{404, aliasRefusedBody, false},
		{422, aliasRefusedBody, false},
		{451, aliasRefusedBody, false},
		{500, aliasRefusedBody, false},
		{403, `{"error":{"code":"forbidden","message":"` + rmail.AliasRoutingRefusal + `"}}`, false},
		{403, `{"error":{"code":"rejected","message":"Message rejected by policy"}}`, false},
	} {
		reason, refused := aliasRoutingRefusal(&apiclient.APIError{StatusCode: test.status, Body: test.body})
		if refused != test.want || (refused && reason != rmail.AliasRoutingRefusal) {
			t.Errorf("%d %s = %q, %v; want refused %v", test.status, test.body, reason, refused, test.want)
		}
	}
}

func TestAliasRefusalNeverReflectsControlCharacters(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"Alias delivery is not available for this recipient.\r\n250 accepted"}}`, `{"error":{"message":"alias routing refused: alias loop: record not found"}}`, `{"error":{"message":"policy denied"}}`, `not JSON`} {
		for _, status := range []int{403, 550} {
			if _, refused := aliasRoutingRefusal(&apiclient.APIError{StatusCode: status, Body: body}); refused {
				t.Fatalf("unsafe or unrelated refusal reflected: %d %q", status, body)
			}
		}
	}
}
