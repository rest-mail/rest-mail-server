package handlers

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	pop3gateway "github.com/rest-mail/rest-mail-server/internal/gateway/pop3"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

func TestRESTMAILStoredHeadersThroughPOP3RETR(t *testing.T) {
	for _, withRaw := range []bool{false, true} {
		name := "structured"
		if withRaw {
			name = "stored_raw"
		}
		t.Run(name, func(t *testing.T) {
			db, mailbox := restmailStorageFixture(t)
			if err := db.Model(&models.Domain{}).Where("id = ?", mailbox.DomainID).Update("active", true).Error; err != nil {
				t.Fatal(err)
			}
			if err := migrateModels(db, &models.Credential{}, &models.GatewaySession{}, &models.ThreadDue{}, &models.ThreadSnooze{}, &models.VacationConfig{}, &models.Alias{}); err != nil {
				t.Fatal(err)
			}
			registry := pipeline.NewRegistry()
			registry.Register("header_validate", filters.NewHeaderValidate)
			registry.Register("recipient_check", filters.NewRecipientCheck(db))
			registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
			registry.Register("vacation", filters.NewVacation(db))
			steps, err := json.Marshal([]pipeline.FilterConfig{
				{Name: "header_validate", Type: pipeline.FilterTypeAction, Enabled: true},
				{Name: "recipient_check", Type: pipeline.FilterTypeAction, Enabled: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&models.Pipeline{DomainID: mailbox.DomainID, Direction: "inbound", Active: true, Filters: steps}).Error; err != nil {
				t.Fatal(err)
			}
			_, password, err := repositories.NewCredentialRepository(db).CreateAppPassword(mailbox.ID, mailbox.ID, "retrieval test", nil)
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
			NewRestmailHandler(db, pipeline.NewEngine(registry, nil), nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"127.0.0.1/32"}}).Register(rs, passingSignInChecks.Throttle)
			NewGatewaySessionHandler(db, middleware.DefaultGatewayServices).Register(rs, GatewaySessionRouteChecks{
				Credential: endpoint.Middleware{Wrap: middleware.NewSessionPasswords(PasswordVerifier(db), nil, nil).Authenticate()},
				Throttle:   passingSignInChecks.Throttle,
				Audit:      passingSignInChecks.Audit,
			})
			router.Group(func(reads chi.Router) {
				refuse := func(http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
				}
				reads.Use(middleware.GatewaySessions(middleware.DefaultGatewayServices,
					repositories.GatewaySessionTokenPrefix, GatewaySessionVerifier(db))(refuse))
				NewMessageHandler(db, nil, nil, "", nil).RegisterMessages(endpoint.Routes{Router: reads, Registry: rs.Registry})
			})
			// The POP3 gateway reaches the API as the internal listener sees it:
			// with its own verified client certificate (#1586).
			internal := middleware.RequireClientCert(router)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "rest-mail-pop3-gateway"}}
				r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
				internal.ServeHTTP(w, r)
			}))
			t.Cleanup(api.Close)
			messageID := "stored@example.test\r\nX-Stored-Message-ID: extra"
			inReplyTo := "parent@example.test\nX-Stored-In-Reply-To: extra"
			references := "reference@example.test\rX-Stored-References: extra"
			payload := map[string]any{
				"from": "sender@external.test", "to": []string{mailbox.Address},
				"subject": "Stored headers", "body_text": "Original body.\r\n",
				"date":       "Tue, 03 Nov 2026 12:00:00 +0000",
				"message_id": messageID, "in_reply_to": inReplyTo, "references": references,
			}
			original := "From: sender@external.test\r\nSubject: Stored headers\r\nMessage-ID: <original@example.test>\r\n\r\nOriginal body.\r\n"
			if withRaw {
				payload["raw_message"] = original
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			response, err := api.Client().Post(api.URL+restmailMessagesPath, "application/json", bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			result, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != http.StatusCreated {
				t.Fatalf("RESTMAIL delivery: status %d, body %s, err %v", response.StatusCode, result, err)
			}
			var stored models.Message
			if err := db.Where("mailbox_id = ?", mailbox.ID).Take(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.MsgID != messageID || stored.InReplyTo != inReplyTo || stored.References != references {
				t.Fatal("delivery changed the structured header values")
			}
			if (stored.RawMessage != "") != withRaw {
				t.Fatalf("stored raw present = %t, want %t", stored.RawMessage != "", withRaw)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err == nil {
					pop3server.NewSession(conn, pop3gateway.NewBackend(apiclient.New(api.URL), nil), nil, nil).Handle()
				}
			}()
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			client := textproto.NewConn(conn)
			t.Cleanup(func() { _ = client.Close(); <-done })
			if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
				t.Fatal(err)
			}
			status := func() {
				t.Helper()
				line, err := client.ReadLine()
				if err != nil || !strings.HasPrefix(line, "+OK") {
					t.Fatalf("POP3 status %q: %v", line, err)
				}
			}
			status()
			for _, command := range []string{"USER " + mailbox.Address, "PASS " + password, "RETR 1"} {
				if err := client.PrintfLine("%s", command); err != nil {
					t.Fatal(err)
				}
				status()
			}
			raw, err := client.ReadDotBytes()
			if err != nil {
				t.Fatal(err)
			}
			message, err := mail.ReadMessage(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			for _, header := range []string{"X-Stored-Message-ID", "X-Stored-In-Reply-To", "X-Stored-References", "References"} {
				if value := message.Header.Get(header); value != "" {
					t.Errorf("RETR emitted unexpected header %s: %q", header, value)
				}
			}
			if withRaw {
				if message.Header.Get("Message-ID") != "<original@example.test>" {
					t.Error("RETR did not preserve the stored raw Message-ID")
				}
			} else {
				if message.Header.Get("Message-ID") != "<stored@example.testX-Stored-Message-ID: extra>" ||
					message.Header.Get("In-Reply-To") != "<parent@example.testX-Stored-In-Reply-To: extra>" {
					t.Errorf("RETR lost sanitized identifiers: %v", message.Header)
				}
			}
			body, err := io.ReadAll(message.Body)
			if err != nil || string(body) != "Original body.\n" {
				t.Errorf("RETR body = %q, err %v", body, err)
			}
		})
	}
}
