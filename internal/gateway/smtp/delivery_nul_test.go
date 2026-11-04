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

func TestSMTPNULDeliveryIsRefused(t *testing.T) {
	for _, field := range []string{"subject", "raw_message"} {
		t.Run(field, func(t *testing.T) {
			router := chi.NewRouter()
			(&handlers.MessageHandler{}).RegisterDelivery(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
			type answer struct {
				status int
				body   string
			}
			answers := make(chan answer, 1)
			api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				answers <- answer{recorder.Code, recorder.Body.String()}
				writer.WriteHeader(recorder.Code)
				_, _ = writer.Write(recorder.Body.Bytes())
			}))
			t.Cleanup(api.Close)
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
			body := "Subject: Hello\r\nContent-Type: application/octet-stream\r\n\r\nbefore\x00after\r\n"
			if field == "subject" {
				body = "Subject: before\x00after\r\n\r\nHello\r\n"
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
