package smtp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

type nulDeliveryBackend struct {
	*mockBackend
	client   *apiclient.Client
	requests chan *apiclient.DeliverRequest
}

func (backend *nulDeliveryBackend) DeliverMessageFrom(request *apiclient.DeliverRequest, content apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	backend.requests <- wholeDelivery(request, content)
	return backend.client.DeliverMessageFrom(request, content)
}

func TestSMTPNULDeliveryIsRefused(t *testing.T) {
	for _, field := range []string{"subject", "body_text", "raw_message"} {
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
			backend := &nulDeliveryBackend{mockBackend: mock, client: apiclient.New(api.URL), requests: make(chan *apiclient.DeliverRequest, 1)}
			store := newMockStore()
			harness := newSMTPHarness(t, mock, store, false, func(server *Server) { server.api = backend })
			harness.ehlo()
			if reply := harness.cmd("MAIL FROM:<sender@remote.test>"); replyCode(reply) != "250" {
				t.Fatal(reply)
			}
			if reply := harness.cmd("RCPT TO:<receiver@local.test>"); replyCode(reply) != "250" {
				t.Fatal(reply)
			}
			body := "Subject: Hello\r\nContent-Type: text/plain\r\n\r\nbefore\x00after\r\n"
			if field == "subject" {
				body = "Subject: before\x00after\r\n\r\nHello\r\n"
			} else if field == "raw_message" {
				body = "Subject: Hello\r\nContent-Type: application/octet-stream\r\n\r\nbefore\x00after\r\n"
			}
			reply := harness.dataBody(body)
			select {
			case request := <-backend.requests:
				if !strings.Contains(string(request.RawMessage), "\x00") {
					t.Fatal("SMTP did not carry NUL to the delivery API")
				}
			default:
				t.Fatal("SMTP did not call the delivery API")
			}
			select {
			case response := <-answers:
				if response.status != 422 || !strings.Contains(response.body, `"validation_failed"`) || !strings.Contains(response.body, `"`+field+`":`) {
					t.Fatalf("API answer = %d %s, want validation refusal naming %s", response.status, response.body, field)
				}
			default:
				t.Fatal("delivery API did not answer")
			}
			if !strings.HasPrefix(reply, "452 4.2.2 ") {
				t.Fatalf("SMTP answer = %q, want existing 452 4.2.2 refusal", reply)
			}
			if len(store.queued()) != 0 || len(mock.deliveredTo()) != 0 {
				t.Fatal("refused mail was queued or reported delivered")
			}
		})
	}
}
