package middleware

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

// gatewayTLS is the connection state of a gateway whose client certificate
// the internal listener verified.
func gatewayTLS() *tls.ConnectionState {
	cert := &x509.Certificate{}
	return &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
}

// idOf runs r through RequestID and returns the ID the handler saw.
func idOf(t *testing.T, r *http.Request) string {
	t.Helper()
	var got string
	RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = requestid.FromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), r)
	if got == "" {
		t.Fatal("the request has no ID")
	}
	return got
}

func TestRequestID_AdoptsAVerifiedGatewaysID(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", nil)
	r.TLS = gatewayTLS()
	r.Header.Set(requestid.Header, "3f2a9c0d41b7e6a8")
	if got := idOf(t, r); got != "3f2a9c0d41b7e6a8" {
		t.Fatalf("ID = %q, want the gateway's", got)
	}
}

func TestRequestID_IgnoresAPublicClientsID(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
	r.Header.Set(requestid.Header, "chosen-by-the-client")
	if got := idOf(t, r); got == "chosen-by-the-client" {
		t.Fatal("a public client's X-Request-Id was adopted")
	}
	// TLS without a verified client certificate is still a public client.
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	if got := idOf(t, r); got == "chosen-by-the-client" {
		t.Fatal("an unverified certificate's X-Request-Id was adopted")
	}
}

func TestRequestID_ReplacesAnInvalidGatewayID(t *testing.T) {
	for _, bad := range []string{strings.Repeat("a", requestid.MaxLen+1), "has space", "line\nbreak", `quote"`} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", nil)
		r.TLS = gatewayTLS()
		r.Header.Set(requestid.Header, bad)
		if got := idOf(t, r); got == bad || !requestid.Valid(got) {
			t.Fatalf("for %q the ID is %q, want a new valid one", bad, got)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.TLS = gatewayTLS()
	r.Header.Set(requestid.Header, strings.Repeat("a", requestid.MaxLen))
	if got := idOf(t, r); got != strings.Repeat("a", requestid.MaxLen) {
		t.Fatalf("an ID of exactly %d characters was not adopted", requestid.MaxLen)
	}
}

// The internal listener hands a gateway's session calls on to the public
// router, whose RequestID must keep the ID the internal one set.
func TestRequestID_KeepsAnIDAlreadySet(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/messages", nil)
	r.Header.Set(requestid.Header, "chosen-by-the-client")
	r = r.WithContext(requestid.NewContext(r.Context(), "from-the-internal-listener"))
	if got := idOf(t, r); got != "from-the-internal-listener" {
		t.Fatalf("ID = %q, want the one already set", got)
	}
}

// A pipeline line logged for a gateway's request carries the gateway's ID.
func TestRequestID_PipelineLineCarriesTheGatewaysID(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(requestid.NewHandler(slog.NewJSONHandler(&out, nil)))
	engine := pipeline.NewEngine(pipeline.NewRegistry(), logger)
	cfg := &pipeline.PipelineConfig{
		Direction: "inbound",
		Active:    true,
		// No such filter is registered, so the engine logs an error for it.
		Filters: []pipeline.FilterConfig{{Name: "missing", Type: pipeline.FilterTypeAction, Enabled: true}},
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", nil)
	r.TLS = gatewayTLS()
	r.Header.Set(requestid.Header, "smtp-conn-7f3e")
	RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if _, err := engine.Execute(r.Context(), cfg, &pipeline.EmailJSON{}); err != nil {
			t.Errorf("execute: %v", err)
		}
	})).ServeHTTP(httptest.NewRecorder(), r)

	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(bytes.SplitN(out.Bytes(), []byte("\n"), 2)[0]), &line); err != nil {
		t.Fatalf("no pipeline log line: %v (%q)", err, out.String())
	}
	if line["msg"] != "failed to create filter" || line[requestid.Attr] != "smtp-conn-7f3e" {
		t.Fatalf("pipeline line = %v, want the filter error with request_id smtp-conn-7f3e", line)
	}
}

func TestRequestIDHandler_LeavesLinesWithoutAnIDAlone(t *testing.T) {
	var out bytes.Buffer
	slog.New(requestid.NewHandler(slog.NewJSONHandler(&out, nil))).InfoContext(context.Background(), "no request")
	if strings.Contains(out.String(), requestid.Attr) {
		t.Fatalf("line without a request carries an ID: %s", out.String())
	}
}
