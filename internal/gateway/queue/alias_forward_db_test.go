package queue

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	arc "github.com/rest-mail/go-arc/pkg/arc"
	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgateway "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

type aliasForwardPass struct{}

func (aliasForwardPass) Name() string              { return "vacation" }
func (aliasForwardPass) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (aliasForwardPass) Execute(context.Context, *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionContinue}, nil
}

func TestAliasForwardDeliveryAndSigning(t *testing.T) {
	for _, route := range []string{"SMTP", "RESTMAIL"} {
		for _, senderKind := range []string{"external", "hosted", "null", "already signed"} {
			t.Run(route+"/"+senderKind, func(t *testing.T) {
				worker, database, domain := srsForwardWorker(t)
				if err := database.AutoMigrate(&models.Mailbox{}, &models.Alias{}, &models.Pipeline{}, &models.OutboundQueue{}); err != nil {
					t.Fatal(err)
				}
				if err := database.Model(&models.Domain{}).Where("name = ?", domain).Update("active", true).Error; err != nil {
					t.Fatal(err)
				}
				var forwarder models.Domain
				if err := database.Where("name = ?", domain).First(&forwarder).Error; err != nil {
					t.Fatal(err)
				}
				source, target := "alias@"+domain, "recipient@elsewhere.test"
				if err := database.Create(&models.Alias{DomainID: forwarder.ID, SourceAddress: source, DestinationAddress: target, Active: true}).Error; err != nil {
					t.Fatal(err)
				}
				storedFilters, _ := json.Marshal([]pipeline.FilterConfig{{Name: "recipient_check", Type: pipeline.FilterTypeAction, Enabled: true}})
				if err := database.Create(&models.Pipeline{DomainID: forwarder.ID, Direction: "inbound", Active: true, Filters: storedFilters}).Error; err != nil {
					t.Fatal(err)
				}
				registry := pipeline.NewRegistry()
				registry.Register("recipient_check", filters.NewRecipientCheck(database))
				registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
				registry.Register("vacation", func([]byte) (pipeline.Filter, error) { return aliasForwardPass{}, nil })
				engine := pipeline.NewEngine(registry, nil)
				router := chi.NewRouter()
				routes := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
				middleware := endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
				messages := handlers.NewMessageHandler(database, nil, engine, relayRouteMasterKey, nil)
				messages.RegisterDelivery(routes, middleware)
				handlers.NewMailboxHandler(database, nil, nil).RegisterAddressCheck(routes)
				rest := handlers.NewRestmailHandler(database, engine, nil, handlers.RestmailTarpitConfig{}, handlers.RestmailDeliverAuthConfig{TrustedCIDRs: []string{"127.0.0.0/8"}})
				rest.SetLocalDelivery(messages)
				rest.Register(routes, middleware)
				apiServer := httptest.NewServer(router)
				t.Cleanup(apiServer.Close)
				sender := "author@original.test"
				if senderKind == "hosted" {
					sender = "author@" + domain
				}
				if senderKind == "null" {
					sender = ""
				}
				raw := "From: author@original.test\r\nTo: " + source + "\r\nSubject: alias forward\r\nDate: Tue, 03 Nov 2026 05:00:00 +0000\r\nMessage-ID: <alias@original.test>\r\n\r\nalias body\r\n"
				if senderKind == "already signed" {
					keyPEM, _, err := dkim.GenerateKey(2048)
					if err != nil {
						t.Fatal(err)
					}
					originalKey, err := dkim.ParsePrivateKey(keyPEM)
					if err != nil {
						t.Fatal(err)
					}
					signature, err := dkim.Sign([]byte(raw), dkim.SignOptions{Domain: "original.test", Selector: "original", PrivateKey: originalKey, Time: time.Now().Unix()})
					if err != nil {
						t.Fatal(err)
					}
					raw = "DKIM-Signature: " + signature + "\r\n" + raw
				}
				if route == "SMTP" {
					listener, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					port := listener.Addr().(*net.TCPAddr).Port
					listener.Close()
					gateway := smtpgateway.NewServer("mx."+domain, apiclient.New(apiServer.URL), nil, smtpgateway.NewStore(database, relayRouteMasterKey), connlimiter.New(connlimiter.Config{}))
					if err := gateway.ListenAndServe(smtpgateway.SMTPPorts{Inbound: port}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(gateway.Shutdown)
					client, err := smtp.Dial(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = client.Close() }()
					if err := client.Mail(sender); err != nil {
						t.Fatal(err)
					}
					if err := client.Rcpt(source); err != nil {
						t.Fatal(err)
					}
					writer, err := client.Data()
					if err != nil {
						t.Fatal(err)
					}
					fmt.Fprint(writer, raw)
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					if err := client.Quit(); err != nil {
						t.Fatal(err)
					}
				} else {
					payload, _ := json.Marshal(map[string]any{"from": sender, "to": []string{source}, "raw_message": []byte(raw)})
					response, err := http.Post(apiServer.URL+"/restmail/messages", "application/json", strings.NewReader(string(payload)))
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					if response.StatusCode != http.StatusCreated {
						t.Fatalf("RESTMAIL status = %d", response.StatusCode)
					}
				}
				var queued []models.OutboundQueue
				if err := database.Where("sending_domain = ?", domain).Find(&queued).Error; err != nil {
					t.Fatal(err)
				}
				if len(queued) != 1 {
					t.Fatalf("queue rows = %d, want 1", len(queued))
				}
				row := queued[0]
				if row.Sender != sender || row.Recipient != target || row.ARCResults == "" {
					t.Fatalf("forward row = %+v", row)
				}
				certificate, roots := relayTestCertificate(t, false)
				peer, finish := relayTestPeer(t, certificate, relayPeerOptions{})
				worker.relayRoots = roots
				seedRelay(t, database, domain, models.DomainRelay{Transport: models.OutboundTransportRelay, Host: peer.host, Port: peer.port, TLSMode: models.RelayTLSStartTLS})
				if err := worker.deliver(row); err != nil {
					t.Fatal(err)
				}
				result := finish()
				mailFrom := mailCommand(result.commands)
				if senderKind == "external" || senderKind == "already signed" {
					if !strings.HasPrefix(mailFrom, "MAIL FROM:<SRS0=") {
						t.Fatalf("not SRS: %s", mailFrom)
					}
					address := strings.SplitN(strings.TrimPrefix(mailFrom, "MAIL FROM:<"), ">", 2)[0]
					if original, err := worker.srs.Reverse(address, time.Now()); err != nil || original != sender {
						t.Fatalf("SRS reverse = %s, %v", original, err)
					}
				} else if !strings.HasPrefix(mailFrom, "MAIL FROM:<"+sender+">") {
					t.Fatalf("sender rewritten: %s", mailFrom)
				}
				if !strings.Contains(result.data, "alias body") || !strings.Contains(result.data, "Delivered-To: "+source) {
					t.Fatalf("forward lost content or alias provenance: %s", result.data)
				}
				privateKey, err := models.LoadDKIMPrivateKey(forwarder.DKIMPrivateKey, relayRouteMasterKey)
				if err != nil {
					t.Fatal(err)
				}
				key, err := dkim.ParsePrivateKey(privateKey)
				if err != nil {
					t.Fatal(err)
				}
				publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
				if err != nil {
					t.Fatal(err)
				}
				resolver := func(_ context.Context, name string) ([]string, error) {
					if strings.TrimSuffix(name, ".") == "sel._domainkey."+domain {
						return []string{"v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(publicKey)}, nil
					}
					return nil, nil
				}
				signatures := dkim.Verify(context.Background(), []byte(result.data), resolver)
				if len(signatures) == 0 || signatures[0].Result != "pass" || signatures[0].Domain != domain {
					t.Fatalf("DKIM = %+v", signatures)
				}
				if verdict, reason := arc.Verify(context.Background(), []byte(result.data), resolver); verdict != "pass" {
					t.Fatalf("ARC = %s (%s)", verdict, reason)
				}
			})
		}
	}
}
