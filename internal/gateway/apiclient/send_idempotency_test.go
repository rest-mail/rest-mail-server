package apiclient

import (
	"net/http"
	"testing"
)

func TestSendMessageIdempotencyHeader(t *testing.T) {
	for _, key := range []string{"", "stable-retry-key"} {
		t.Run(key, func(t *testing.T) {
			server, router := newTestServer(t)
			router.HandleFunc("/api/v1/messages/send", func(response http.ResponseWriter, request *http.Request) {
				if got := request.Header.Get("Idempotency-Key"); got != key {
					t.Errorf("key=%q want=%q", got, key)
				}
				if request.Header.Get("Authorization") != "Bearer token" {
					t.Error("credential missing")
				}
				response.WriteHeader(http.StatusCreated)
			})
			client := New(server.URL)
			if err := client.SendMessageWithIdempotencyKey("token", &SendRequest{From: "a@example.test", To: []string{"b@example.test"}}, key); err != nil {
				t.Fatal(err)
			}
		})
	}
}
