package smtp

import (
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

type greylistDelivery struct {
	*mockBackend
	status int
	body   string
}

func (backend *greylistDelivery) DeliverMessageFrom(*apiclient.DeliverRequest, apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	return nil, &apiclient.APIError{StatusCode: backend.status, Body: backend.body}
}

// The API's greylisted and rate limited refusals reach a sending server in words its
// queue can read; every other temporary refusal keeps its own reply.
func TestSMTPDeferralReplyNamesItsReason(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"greylisted", `{"error":{"code":"greylisted","message":"Greylisted, try again later"}}`, 503, "450 4.7.1 Greylisted, try again later"},
		{"rate limited", `{"error":{"code":"rate_limited","message":"Too many messages, try again later"}}`, 503, "450 4.7.1 Too many messages, try again later"},
		{"deferred", `{"error":{"code":"deferred","message":"Try again later"}}`, 503, "451 4.3.0 Try again later"},
		{"not JSON", `greylisted`, 503, "451 4.3.0 Try again later"},
		{"another status", `{"error":{"code":"greylisted"}}`, 451, "451 4.3.0 Temporary delivery failure"},
		{"rate limit with another status", `{"error":{"code":"rate_limited"}}`, 451, "451 4.3.0 Temporary delivery failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newMockBackend()
			backend.local["mailbox@local.test"] = true
			harness := newSMTPHarness(t, backend, newMockStore(), false, func(server *Server) {
				server.api = &greylistDelivery{mockBackend: backend, status: test.status, body: test.body}
			})
			harness.ehlo()
			for _, command := range []string{"MAIL FROM:<sender@remote.test>", "RCPT TO:<mailbox@local.test>"} {
				if reply := harness.cmd(command); replyCode(reply) != "250" {
					t.Fatal(reply)
				}
			}
			if reply := harness.dataBody(testBody); !strings.HasPrefix(reply, test.want) {
				t.Fatalf("DATA = %q, want %q", reply, test.want)
			}
		})
	}
}
