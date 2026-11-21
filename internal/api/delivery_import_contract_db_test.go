package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
)

func TestAC03DeliveryAndImportContractsAreSeparate(t *testing.T) {
	const deliveryPath = "/api/v1/messages/deliver"
	legacy := []string{"folder", "is_answered", "is_read", "is_flagged", "is_starred", "is_draft", "received_at", "mailbox_id"}
	importValues := map[string]string{
		"flags": `["\\Seen"]`, "internal_date": `"2021-01-02T03:04:05Z"`, "always_store": "true", "match_submission": "true",
	}
	importFields := []string{"flags", "internal_date", "always_store", "match_submission"}
	t.Run("types and descriptions", func(t *testing.T) {
		fieldsOf := func(body any) map[string]bool {
			fields := map[string]bool{}
			for _, field := range reflect.VisibleFields(reflect.TypeOf(body)) {
				name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				fields[name] = true
			}
			return fields
		}
		for _, body := range []any{apitypes.DeliverMessageBody{}, apiclient.DeliverRequest{}} {
			fields := fieldsOf(body)
			if !fields["raw_message"] || !fields["address"] {
				t.Errorf("%T is missing delivery fields", body)
			}
			for _, name := range slices.Concat(legacy, importFields) {
				if fields[name] {
					t.Errorf("%T declares import-only or obsolete field %q", body, name)
				}
			}
		}
		for _, body := range []any{apitypes.ImportMessageBody{}, apiclient.ImportRequest{}} {
			for _, name := range slices.Concat([]string{"folder", "raw_message"}, importFields) {
				if !fieldsOf(body)[name] {
					t.Errorf("%T is missing import field %q", body, name)
				}
			}
		}
		for name, doc := range map[string]map[string]any{"generated": generatedDocument(t), "served YAML": oracleDocument(t)} {
			var found bool
			for _, op := range operationsOf(doc) {
				if op.id != "deliverMessage" {
					continue
				}
				found = true
				description, _ := op.operation["description"].(string)
				if !strings.Contains(description, "legacy") || !strings.Contains(description, "flags and internal_date are rejected") {
					t.Errorf("%s: description does not distinguish legacy ignored fields from refused import fields", name)
				}
				body := resolved(doc, op.operation["requestBody"]).(map[string]any)
				media := body["content"].(map[string]any)["application/json"].(map[string]any)
				schema := resolved(doc, media["schema"]).(map[string]any)
				properties := schema["properties"].(map[string]any)
				for _, field := range slices.Concat(legacy, importFields) {
					if _, present := properties[field]; present {
						t.Errorf("%s: delivery describes %q", name, field)
					}
				}
			}
			if !found {
				t.Errorf("%s: missing delivery operation", name)
			}
		}
	})

	t.Run("persisted wire behavior", func(t *testing.T) {
		gateway := newGatewayFixture(t)
		f := gateway.credentialFixture
		var domain models.Domain
		if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(`[]`), Active: true}).Error; err != nil {
			t.Fatal(err)
		}
		other := f.seedMailbox(domain.ID, "other")
		arrivals := func(subject string) []models.Message {
			t.Helper()
			var stored []models.Message
			if err := f.db.Where("subject = ?", subject).Find(&stored).Error; err != nil {
				t.Fatal(err)
			}
			return stored
		}
		assertArrival := func(subject string, before, after time.Time) {
			t.Helper()
			stored := arrivals(subject)
			if len(stored) != 1 {
				t.Fatalf("%s: want one arrival, got %d", subject, len(stored))
			}
			msg := stored[0]
			if msg.MailboxID != f.mailboxID || msg.Folder != "INBOX" || msg.IsAnswered || msg.IsRead || msg.IsFlagged || msg.IsStarred || msg.IsDraft || msg.IsDeleted || len(msg.Keywords) != 0 {
				t.Errorf("%s: wrong mailbox, folder or flags on message %d", subject, msg.ID)
			}
			if msg.ReceivedAt.Before(before.Truncate(time.Microsecond)) || msg.ReceivedAt.After(after) {
				t.Errorf("%s: received at %v outside arrival window [%v, %v]", subject, msg.ReceivedAt, before, after)
			}
		}
		t.Run("ignored legacy fields", func(t *testing.T) {
			before := time.Now()
			body := fmt.Sprintf(`{"address":%q,"mailbox_id":%d,"sender":"sender@remote.test","subject":"legacy arrival","body_text":"hello","folder":"Archive","is_answered":true,"is_read":true,"is_flagged":true,"is_starred":true,"is_draft":true,"received_at":"2021-01-02T03:04:05Z"}`, f.address, other)
			rr := gateway.as(deliveryCN, http.MethodPost, deliveryPath, "", body)
			if rr.Code != http.StatusCreated {
				t.Fatalf("legacy fields: %d %s", rr.Code, rr.Body)
			}
			assertArrival("legacy arrival", before, time.Now())
		})
		for _, field := range importFields {
			t.Run("refused "+field, func(t *testing.T) {
				body := fmt.Sprintf(`{"address":%q,"sender":"sender@remote.test","subject":"refused import metadata","body_text":"hello",%q:%s}`, f.address, field, importValues[field])
				rr := gateway.as(deliveryCN, http.MethodPost, deliveryPath, "", body)
				if rr.Code != http.StatusUnprocessableEntity {
					t.Fatalf("import field %s: %d %s", field, rr.Code, rr.Body)
				}
				var refused respond.ValidationErrorResponse
				decodeInto(t, rr, &refused)
				if refused.Error.Code != "validation_failed" || refused.Error.Details.Fields[field] != "not allowed" {
					t.Fatalf("wrong refusal for %s: %+v", field, refused.Error)
				}
				if count := len(arrivals("refused import metadata")); count != 0 {
					t.Errorf("refused request stored %d messages", count)
				}
			})
		}
		t.Run("SMTP arrival", func(t *testing.T) {
			api := gatewayAPI(t, f.router, gateway.internal, deliveryCN)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			server := smtpgw.NewServer("mail.example.test", api, nil, smtpgw.NewStore(f.db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 10, MaxGlobal: 10}))
			if err := server.Serve(smtpgw.SMTPListeners{Inbound: listener}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(server.Shutdown)
			client, err := smtp.Dial(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			const raw = "From: sender@remote.test\r\nTo: chris@example.test\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMessage-ID: <arrival@remote.test>\r\nSubject: SMTP arrival\r\n\r\nhello\r\n"
			before := time.Now()
			if err := send(client, "sender@remote.test", f.address, raw); err != nil {
				t.Fatal(err)
			}
			assertArrival("SMTP arrival", before, time.Now())
			if msg := arrivals("SMTP arrival")[0]; !strings.HasSuffix(msg.RawMessage, raw) {
				t.Errorf("SMTP arrival lost its original octets")
			}
		})
		t.Run("IMAP APPEND and COPY", func(t *testing.T) {
			api := gatewayAPI(t, f.router, gateway.internal, imapCN)
			session := openIMAPZoneSession(t, f, api, gateway.app.Password)
			for _, command := range []string{"CREATE Archive", "CREATE Copies"} {
				if _, status := session.command(command); !strings.Contains(status, " OK") {
					t.Fatalf("%s: %s", command, status)
				}
			}
			const raw = "From: sender@remote.test\r\nMessage-ID: <import@remote.test>\r\nSubject: IMAP import\r\n\r\nhello\r\n"
			tag := session.nextTag()
			session.write(fmt.Sprintf("%s APPEND Archive (\\Seen \\Flagged $Work) \"02-Jan-2020 03:04:05 +0230\" {%d}\r\n", tag, len(raw)))
			if line := session.line(); !strings.HasPrefix(line, "+") {
				t.Fatalf("APPEND continuation: %q", line)
			}
			session.write(raw + "\r\n")
			if _, status := session.responses(tag); !strings.Contains(status, " OK") {
				t.Fatalf("APPEND: %s", status)
			}
			for _, command := range []string{"SELECT Archive", "COPY 1 Copies"} {
				if _, status := session.command(command); !strings.Contains(status, " OK") {
					t.Fatalf("%s: %s", command, status)
				}
			}
			stored := arrivals("IMAP import")
			if len(stored) != 2 {
				t.Fatalf("APPEND and COPY stored %d messages, want 2", len(stored))
			}
			var folders []string
			for _, msg := range stored {
				folders = append(folders, msg.Folder)
				if msg.MailboxID != f.mailboxID || !msg.IsRead || !msg.IsFlagged || !slices.Equal(msg.Keywords, []string{"$Work"}) || !msg.ReceivedAt.Equal(time.Date(2020, 1, 2, 0, 34, 5, 0, time.UTC)) || msg.RawMessage != raw {
					t.Errorf("imported message %d did not preserve flags, date or original octets: read=%t flagged=%t keywords=%q date=%v", msg.ID, msg.IsRead, msg.IsFlagged, msg.Keywords, msg.ReceivedAt)
				}
			}
			slices.Sort(folders)
			if !slices.Equal(folders, []string{"Archive", "Copies"}) {
				t.Errorf("APPEND/COPY folders: %q", folders)
			}
		})
	})
}
