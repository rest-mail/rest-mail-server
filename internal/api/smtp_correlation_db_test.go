package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/mtls"
	"github.com/rest-mail/rest-mail-server/internal/mtls/mtlstest"
	"github.com/rest-mail/rest-mail-server/internal/tracing"
	"github.com/rest-mail/rest-mail-server/internal/tracing/tracingtest"
	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
	"github.com/rest-mail/rest-mail-server/pkg/requestid"
)

// TestSMTPDeliveryCarriesOneRequestID covers the correlation path in #2117 AC02.
func TestSMTPDeliveryCarriesOneRequestID(t *testing.T) {
	f := newCredentialFixtureWith(t, &config.Config{
		Environment: "test", InternalMTLSEnabled: true,
		SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize, TraceSampleRate: 1,
	})
	var mailbox models.Mailbox
	if err := f.db.First(&mailbox, f.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.Pipeline{
		DomainID: mailbox.DomainID, Direction: "inbound", Filters: json.RawMessage(`[]`), Active: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	spans := tracingtest.Record(t)
	var logs correlationLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(requestid.NewHandler(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { slog.SetDefault(previous) })

	paths, err := mtlstest.Write(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := mtls.ServerTLSConfig(paths.CACert, paths.ServerCert, paths.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := mtls.ClientTLSConfig(paths.CACert, paths.ClientCert, paths.ClientKey)
	if err != nil {
		t.Fatal(err)
	}
	const deliveryPath = "/api/v1/messages/deliver"
	wireIDs := make(chan string, 1)
	internal := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == deliveryPath {
			select {
			case wireIDs <- r.Header.Get(requestid.Header):
			default:
			}
		}
		f.routers.Internal.ServeHTTP(w, r)
	}))
	internal.TLS = serverTLS
	internal.StartTLS()
	t.Cleanup(internal.Close)
	backend := smtpgw.Clients{Delivery: apiclient.New(internal.URL,
		apiclient.WithInternalMTLS(internal.URL, clientTLS), apiclient.WithDoer(tracing.Do))}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	gateway := smtpgw.NewServer("mail.example.test", backend, nil, smtpgw.NewStore(f.db, ""),
		connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.Serve(smtpgw.SMTPListeners{Inbound: listener}); err != nil {
		t.Fatalf("start SMTP: %v", err)
	}
	t.Cleanup(gateway.Shutdown)

	conn, err := net.DialTimeout("tcp", listener.Addr().String(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	client, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	const sender = "sender@remote.test"
	const messageID = "<correlation@remote.test>"
	mail := "From: " + sender + "\r\nTo: " + f.address + "\r\nSubject: Correlation test\r\n" +
		"Date: Thu, 02 Jan 2020 03:04:05 +0000\r\nMessage-ID: " + messageID + "\r\n\r\nHello.\r\n"
	if err := send(client, sender, f.address, mail); err != nil {
		t.Fatalf("deliver over SMTP: %v", err)
	}
	if err := client.Quit(); err != nil {
		t.Fatalf("SMTP QUIT: %v", err)
	}

	session := tracingtest.WaitFor(t, spans, "smtp.session")
	requestID := tracingtest.Attr(session, requestid.Attr)
	if !requestid.Valid(requestID) {
		t.Fatalf("SMTP session request_id = %q", requestID)
	}
	select {
	case id := <-wireIDs:
		if id != requestID {
			t.Errorf("delivery X-Request-Id = %q, want %q", id, requestID)
		}
	default:
		t.Error("no HTTP delivery request reached the API")
	}
	var call, serve sdktrace.ReadOnlySpan
	for _, span := range spans.Ended() {
		if span.Name() != http.MethodPost+" "+deliveryPath {
			continue
		}
		switch span.SpanKind() {
		case trace.SpanKindClient:
			call = span
		case trace.SpanKindServer:
			serve = span
		}
	}
	local := tracingtest.WaitFor(t, spans, "message.deliver_local")
	run := tracingtest.WaitFor(t, spans, "pipeline.run")
	for _, hop := range []struct {
		name         string
		span, parent sdktrace.ReadOnlySpan
	}{
		{"gateway API call", call, session}, {"API request", serve, call},
		{"local delivery", local, serve}, {"pipeline run", run, local},
	} {
		if hop.span == nil {
			t.Errorf("missing %s span", hop.name)
			continue
		}
		if id := tracingtest.Attr(hop.span, requestid.Attr); id != requestID {
			t.Errorf("%s request_id = %q, want %q", hop.name, id, requestID)
		}
		if hop.parent != nil && (hop.span.Parent().SpanID() != hop.parent.SpanContext().SpanID() ||
			hop.span.SpanContext().TraceID() != session.SpanContext().TraceID()) {
			t.Errorf("%s is outside the SMTP delivery trace or has the wrong parent", hop.name)
		}
	}

	var stored models.Message
	if err := f.db.Where("mailbox_id = ? AND message_id = ?", f.mailboxID, messageID).Take(&stored).Error; err != nil {
		t.Fatalf("read stored message: %v", err)
	}
	if stored.RequestID == nil || *stored.RequestID != requestID {
		t.Errorf("stored message %d has a different or missing request_id, want %q", stored.ID, requestID)
	}
	f.routers.Recorder.Shutdown() // Flush the production recorder before reading its rows.
	var persistedTrace models.MessageTrace
	if err := f.db.Where("message_id = ? AND outcome = ?", stored.ID, "delivered").Take(&persistedTrace).Error; err != nil {
		t.Fatalf("read stored pipeline trace for message %d: %v", stored.ID, err)
	}
	if persistedTrace.RequestID == nil || *persistedTrace.RequestID != requestID {
		t.Errorf("stored pipeline trace has a different or missing request_id, want %q", requestID)
	}

	for _, raw := range bytes.Split(bytes.TrimSpace(logs.snapshot()), []byte("\n")) {
		var line struct {
			Message   string `json:"msg"`
			Event     string `json:"event"`
			MailboxID uint   `json:"mailbox_id"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("decode structured log: %v", err)
		}
		if line.Message == "sse: event published" && line.Event == "new_message" && line.MailboxID == f.mailboxID {
			if line.RequestID != requestID {
				t.Errorf("new_message publish request_id = %q, want %q", line.RequestID, requestID)
			}
			return
		}
	}
	t.Fatal("no new_message publish log for the delivered message's mailbox")
}

type correlationLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *correlationLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *correlationLogBuffer) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}
