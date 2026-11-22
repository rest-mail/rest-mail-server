package handlers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAC04OneClickPublicLookupCannotRebindToLoopback(t *testing.T) {
	var connections, requests, lookups atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil || request.Method != http.MethodPost || string(body) != "List-Unsubscribe=One-Click" {
			t.Errorf("unexpected unsubscribe request: %s %q (%v)", request.Method, body, err)
		}
		if request.URL.Path == "/start" {
			http.Redirect(response, request, "/private-unsubscribe", http.StatusTemporaryRedirect)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	poster := defaultOneClickPoster()
	poster.tlsConfig = &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	poster.timeout = 2 * time.Second
	poster.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookups.Add(1)
		if host != "localhost" {
			t.Errorf("unexpected validation hostname %q", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	target := strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/private-unsubscribe"
	status, err := poster.post(context.Background(), target)
	if !errors.Is(err, errUnsubscribeBlock) || status != 0 {
		t.Errorf("public validation followed by loopback resolution must refuse the actual dial: status=%d error=%v", status, err)
	}
	if lookups.Load() != 1 || connections.Load() != 0 || requests.Load() != 0 {
		t.Fatalf("validation lookups=%d, accepted private connections=%d, requests=%d; want one public lookup and no private connection", lookups.Load(), connections.Load(), requests.Load())
	}
	status, err = testOneClickPoster(server, nil).post(t.Context(), server.URL+"/start")
	if err != nil || status != http.StatusNoContent || requests.Load() != 2 {
		t.Fatalf("explicitly permitted TLS fixture must accept both POSTs across a 307 redirect: status=%d requests=%d error=%v", status, requests.Load(), err)
	}
}
