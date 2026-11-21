package apiproxy

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	keyring "github.com/zalando/go-keyring"
)

// TestProxyStreamsEventsWhileStreamIsOpen proves an event reaches the webview
// before the upstream stream ends, and that credentials in it are stripped.
func TestProxyStreamsEventsWhileStreamIsOpen(t *testing.T) {
	keyring.MockInit()
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/auth/login" {
			_, _ = fmt.Fprintf(writer, `{"data":{"access_token":%q,"refresh_token":%q,"expires_in":3600,"user":{"id":42}}}`, accessSecret, refreshSecret)
			return
		}
		if request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("proxy dropped the event-stream Accept header: %q", request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(writer, "event: message.new\ndata: {\"folder\":\"INBOX\",\"token\":%q,\"note\":%q}\n\n", accessSecret, "seen "+accessSecret)
		writer.(http.Flusher).Flush()
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(release)
	manager := session.New()
	if _, err := manager.SignIn(strings.Replace(upstream.URL, "127.0.0.1", "localhost", 1), "admin@example.test", "password"); err != nil {
		t.Fatal(err)
	}
	assets := httptest.NewServer(New(manager)(http.NotFoundHandler()))
	defer assets.Close()

	request, err := http.NewRequest(http.MethodGet, assets.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type %q", response.Header.Get("Content-Type"))
	}
	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	var event []string
	deadline := time.After(5 * time.Second)
	for len(event) < 2 {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("stream closed early after %q", event)
			}
			event = append(event, line)
		case <-deadline:
			t.Fatalf("event did not arrive while the stream was open; got %q", event)
		}
	}
	if event[0] != "event: message.new" || !strings.Contains(event[1], `"folder":"INBOX"`) {
		t.Fatalf("event changed in transit: %q", event)
	}
	for _, secret := range []string{accessSecret, refreshSecret, `"token"`} {
		if strings.Contains(event[1], secret) {
			t.Fatalf("event exposed %s: %q", secret, event[1])
		}
	}
}
