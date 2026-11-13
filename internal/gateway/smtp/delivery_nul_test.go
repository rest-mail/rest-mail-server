package smtp

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

type nulDeliveryBackend struct {
	*mockBackend
	requests chan *apiclient.DeliverRequest
}

func (backend *nulDeliveryBackend) DeliverMessageFrom(request *apiclient.DeliverRequest, content apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	backend.requests <- wholeDelivery(request, content)
	return &apiclient.DeliverResponse{}, nil
}

// A message may hold a NUL or any other octet. The source goes to the API exactly, and what the
// gateway read from it goes in the API's text fields, which can't hold them (#1921).
func TestSMTPDeliveryCarriesAnyOctetsInTheSourceOnly(t *testing.T) {
	for name, body := range map[string]string{
		"subject":    "Subject: before\x00after\r\n\r\nHello\r\n",
		"body_text":  "Subject: Hello\r\nContent-Type: text/plain\r\n\r\nbefore\x00after\r\n",
		"8-bit body": "Subject: Hello\r\nContent-Type: text/plain\r\n\r\nbefore\xe9after\r\n",
		"attachment": "Subject: Hello\r\nContent-Type: application/octet-stream\r\n\r\nbefore\x00\xe9after\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			mock := newMockBackend()
			mock.local["receiver@local.test"] = true
			backend := &nulDeliveryBackend{mockBackend: mock, requests: make(chan *apiclient.DeliverRequest, 1)}
			harness := newSMTPHarness(t, mock, newMockStore(), false, func(server *Server) { server.api = backend })
			harness.ehlo()
			if reply := harness.cmd("MAIL FROM:<sender@remote.test>"); replyCode(reply) != "250" {
				t.Fatal(reply)
			}
			if reply := harness.cmd("RCPT TO:<receiver@local.test>"); replyCode(reply) != "250" {
				t.Fatal(reply)
			}
			if reply := harness.dataBody(body); replyCode(reply) != "250" {
				t.Fatalf("SMTP answer = %q", reply)
			}
			request := <-backend.requests
			if !strings.Contains(string(request.RawMessage), body) {
				t.Fatalf("source changed: %q", request.RawMessage)
			}
			for field, text := range map[string]string{"subject": request.Subject, "body_text": request.BodyText, "body_html": request.BodyHTML} {
				if !utf8.ValidString(text) || strings.Contains(text, "\x00") {
					t.Fatalf("%s = %q", field, text)
				}
			}
		})
	}
}
