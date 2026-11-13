package api

import (
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/sendlimits"
)

func unsubscribeTargetExample(operation, name string) bool {
	return operation == "unsubscribeMailto" && (name == "sent" || name == "held" || name == "discarded") ||
		operation == "unsubscribeOneClick" && (name == "unsubscribed" || name == "target_failed")
}

func TestUnsubscribeTargetExamplesWithVertrag(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range unsubscribeOperations {
		for name, pair := range pairs[operation] {
			if !unsubscribeTargetExample(operation, name) {
				continue
			}
			t.Run(operation+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, operation, name)
				f := newSendLimitFixture(t)
				if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
					t.Fatal(err)
				}
				message := models.Message{MailboxID: f.mailboxID, Folder: "INBOX", Subject: "weekly news",
					Headers: models.JSONB(`{"List-Unsubscribe":["<mailto:leave@news.example.com?subject=unsubscribe%20weekly%20news>"]}`)}
				var hits atomic.Int32
				target := ""
				if operation == "unsubscribeOneClick" {
					server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
						hits.Add(1)
						body, err := io.ReadAll(request.Body)
						if err != nil || string(body) != "List-Unsubscribe=One-Click" || request.Method != http.MethodPost ||
							request.URL.RequestURI() != "/unsubscribe?t=8f2c" || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
							request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
							t.Errorf("unexpected one-click request: %s %s %q, %v", request.Method, request.URL, body, err)
						}
						if name == "target_failed" {
							response.WriteHeader(http.StatusServiceUnavailable)
						}
					}))
					t.Cleanup(server.Close)
					roots := x509.NewCertPool()
					roots.AddCert(server.Certificate())
					f.routers.Messages.SetOneClickTarget(server.Listener.Addr().(*net.TCPAddr).IP, roots)
					target = server.URL + "/unsubscribe?t=8f2c"
					message.Headers = nil
					message.RawMessage = "Authentication-Results: restmail; dkim=pass header.d=news.example.com\r\n" +
						"DKIM-Signature: v=1; a=rsa-sha256; d=news.example.com; s=news; h=From:List-Unsubscribe:List-Unsubscribe-Post; bh=YQ==; b=YQ==\r\n" +
						"From: news@news.example.com\r\nList-Unsubscribe: <" + target + ">\r\n" +
						"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n\r\nweekly news\r\n"
				}
				if err := f.db.Create(&message).Error; err != nil {
					t.Fatal(err)
				}
				if name == "held" {
					spendSendLimit(f, sendlimits.AccountScope(f.accountID))
				}
				if name == "discarded" {
					var sender models.Mailbox
					if err := f.db.First(&sender, f.mailboxID).Error; err != nil {
						t.Fatal(err)
					}
					if err := f.db.Create(&models.Pipeline{DomainID: sender.DomainID, Direction: "outbound", Active: true,
						Filters: json.RawMessage(`[{"name":"sieve","type":"action","enabled":true,"config":{"script":"discard;"}}]`)}).Error; err != nil {
						t.Fatal(err)
					}
				}
				response := sendSendLimitExample(f, requirementRoute{method: route.method, path: route.path}, withPathID(pair, message.ID), f.session)
				if response.Code != pair.Response.Status || response.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s, want %d JSON", response.Code, response.Body.String(), pair.Response.Status)
				}
				var got, want map[string]map[string]any
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				if operation == "unsubscribeOneClick" {
					if hits.Load() != 1 {
						t.Fatalf("one-click target got %d POSTs, want one", hits.Load())
					}
					if name == "unsubscribed" {
						if got["data"]["target"] != target {
							t.Fatalf("answered target differs from the server reached: %v", got)
						}
						got["data"]["target"] = want["data"]["target"]
					}
				}
				var sent []models.Message
				if err := f.db.Where("folder = ?", "Sent").Find(&sent).Error; err != nil {
					t.Fatal(err)
				}
				wantSent := 0
				if name == "sent" {
					wantSent = 1
				}
				if len(sent) != wantSent || outbound(f) != int64(wantSent) {
					t.Fatalf("got %d Sent rows and %d queued deliveries, want %d", len(sent), outbound(f), wantSent)
				}
				if name == "sent" {
					stored, data := sent[0], got["data"]
					if data["id"] != float64(stored.ID) || data["mailbox_id"] != float64(f.mailboxID) || data["message_id"] != stored.MsgID ||
						data["thread_id"] != stored.ThreadID || data["mod_seq"] != float64(stored.ModSeq) || data["raw_size"] != float64(stored.RawSize) || stored.RawSize != len(stored.RawMessage) ||
						data["downgraded_size"] != float64(stored.DowngradedSize) || stored.DowngradedSize == 0 {
						t.Fatalf("response and stored message differ: %s", response.Body.String())
					}
					assertExampleReceipt(t, data, stored.RawMessage, stored.SealedHash)
					for field, size := range map[string]int{"imap_size": stored.IMAPSize, "imap_utf8_size": stored.IMAPUTF8Size, "pop3_utf8_size": stored.POP3UTF8Size} {
						if data[field] != float64(size) {
							t.Fatalf("%s = %v, stored %d", field, data[field], size)
						}
						data[field] = want["data"][field]
					}
					var delivery models.OutboundQueue
					if err := f.db.First(&delivery).Error; err != nil || delivery.MessageID == nil || *delivery.MessageID != stored.ID || delivery.Recipient != "leave@news.example.com" ||
						delivery.Sender != f.address || delivery.RawMessage != stored.RawMessage || !strings.Contains(delivery.RawMessage, "Subject: unsubscribe weekly news") {
						t.Fatalf("unexpected delivery: %+v, %v", delivery, err)
					}
					for _, field := range []string{"received_at", "created_at", "updated_at"} {
						stamp, err := time.Parse(time.RFC3339Nano, data[field].(string))
						if err != nil || time.Since(stamp).Abs() > time.Minute {
							t.Fatalf("invalid generated %s: %v", field, data[field])
						}
					}
					for _, field := range []string{"id", "mailbox_id", "message_id", "thread_id", "received_at", "created_at", "updated_at", "raw_size", "downgraded_size", "sealed_hash"} {
						data[field] = want["data"][field]
					}
				}
				if name == "held" {
					stored := assertHeldExample(t, f, pair, response, sendlimits.AccountScope(f.accountID)+": messages_per_hour")
					if stored.From != f.address || string(stored.To) != `["leave@news.example.com"]` || stored.Subject != "unsubscribe weekly news" {
						t.Fatalf("unexpected held request: %+v", stored)
					}
				} else {
					rewriteBody(t, response, got)
					assertRequirementResponse(t, pair, response)
				}
				if binary := os.Getenv("RESTMAIL_VERTRAG_BINARY"); binary != "" {
					checkNamedPairWithVertrag(t, binary, f.router, namedPairExchange{method: route.method, path: route.path,
						uri: strings.ReplaceAll(route.path, "{id}", strconv.Itoa(int(message.ID))), name: name,
						status: pair.Response.Status, body: string(pair.Request.Body), token: strings.TrimPrefix(f.session, "Bearer ")})
				}
			})
		}
	}
}
