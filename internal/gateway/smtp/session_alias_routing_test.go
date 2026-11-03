package smtp

import (
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

type lateAliasRefusal struct{ *mockBackend }

func (backend *lateAliasRefusal) DeliverMessage(request *apiclient.DeliverRequest) (*apiclient.DeliverResponse, error) {
	if request.Address == "alias@local.test" {
		return nil, &apiclient.APIError{StatusCode: 550, Body: `{"error":{"code":"rejected","message":"Alias delivery is not available for this recipient."}}`}
	}
	return backend.mockBackend.DeliverMessage(request)
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

func TestAliasRefusalNeverReflectsControlCharacters(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"Alias delivery is not available for this recipient.\r\n250 accepted"}}`, `{"error":{"message":"alias routing refused: alias loop: record not found"}}`, `{"error":{"message":"policy denied"}}`, `not JSON`} {
		if _, refused := aliasRoutingRefusal(&apiclient.APIError{StatusCode: 550, Body: body}); refused {
			t.Fatalf("unsafe or unrelated refusal reflected: %q", body)
		}
	}
}
