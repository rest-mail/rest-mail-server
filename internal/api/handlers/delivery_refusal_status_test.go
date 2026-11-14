package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
)

// A refused alias and an arrival past the hop limit are refusals of deliverMessage's own,
// answered before the pipeline runs: 403 rejected to the gateway's real client, never a
// status HTTP does not have.
func TestDeliveryOwnRefusalsAreForbiddenThroughTheGatewayClient(t *testing.T) {
	fixture := newAliasDeliveryFixture(t)
	if err := fixture.db.Model(&fixture.alias).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	stored := func() (count int64) {
		if err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ?", fixture.mailbox.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := stored()
	arrived := true
	for name, tc := range map[string]struct {
		request apiclient.DeliverRequest
		message string
	}{
		"refused alias": {apiclient.DeliverRequest{Address: fixture.alias.SourceAddress, Sender: "sender@outside.test"}, rmail.AliasRoutingRefusal},
		"hop limit": {apiclient.DeliverRequest{
			Address: fixture.mailbox.Address, Sender: "sender@outside.test", ReceivedTLS: &arrived,
			RawMessage: []byte(receivedMessage(rmime.CurrentLimits().MaxReceived+1, fixture.mailbox.Address)),
		}, "Routing loop detected: too many Received headers"},
	} {
		t.Run(name, func(t *testing.T) {
			request := tc.request
			_, err := apiclient.New(fixture.api.URL).DeliverMessage(&request)
			var refusal *apiclient.APIError
			if !errors.As(err, &refusal) {
				t.Fatalf("DeliverMessage = %v, want an API refusal", err)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(refusal.Body), &body); err != nil {
				t.Fatalf("%d %q: %v", refusal.StatusCode, refusal.Body, err)
			}
			if refusal.StatusCode != 403 || body.Error.Code != "rejected" || body.Error.Message != tc.message {
				t.Fatalf("got %d %s, want 403 rejected %q", refusal.StatusCode, refusal.Body, tc.message)
			}
		})
	}
	if after := stored(); after != before {
		t.Fatalf("refused deliveries stored %d messages", after-before)
	}
}

// An alias that stops routing between RCPT and DATA reaches the gateway from the real
// route. The sending server must hear a permanent 550, not the 451 that would have it
// retry: this is the caller of deliverMessage's refused-alias answer.
func TestAliasBrokenAfterRcptIsPermanentThroughTheRealRoute(t *testing.T) {
	fixture := newAliasDeliveryFixture(t)
	client, err := smtp.Dial(fixture.smtpAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Mail("sender@outside.test"); err != nil {
		t.Fatal(err)
	}
	if err := client.Rcpt(fixture.alias.SourceAddress); err != nil {
		t.Fatalf("RCPT while the alias routes: %v", err)
	}
	if err := fixture.db.Model(&fixture.alias).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	writer, err := client.Data()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(writer, "From: sender@outside.test\r\nTo: %s\r\nSubject: late\r\nMessage-ID: <late-%d@outside.test>\r\n\r\nbody\r\n", fixture.alias.SourceAddress, time.Now().UnixNano())
	var reply *textproto.Error
	if err := writer.Close(); !errors.As(err, &reply) {
		t.Fatalf("DATA = %v, want an SMTP refusal", err)
	}
	if reply.Code != 550 || !strings.Contains(reply.Msg, "5.1.1") || !strings.Contains(reply.Msg, rmail.AliasRoutingRefusal) {
		t.Fatalf("DATA = %d %q, want a permanent 550 5.1.1 with the reason", reply.Code, reply.Msg)
	}
}
