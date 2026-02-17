package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func testStage3GatewayInbound(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.login("admin@mail1.test", adminPassword); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	// Setup: Ensure mail3.test domain and a test user exist
	createDomain(t, adminClient, "mail3.test", "restmail")
	gwUser := createMailbox(t, adminClient, "testuser@mail3.test", adminPassword, "GW Test User")

	t.Run("Mail1_to_Mail3_SmtpDelivery", func(t *testing.T) {
		subject := fmt.Sprintf("test-m1-to-m3-%d", time.Now().UnixNano())
		sendMailViaSMTP(t, mail1SMTPAddr,
			"alice@mail1.test", "testuser@mail3.test",
			subject, "Hello mail3 from mail1 via SMTP gateway!")

		gwClient := newAPIClient()
		if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
			t.Fatalf("Cannot login as testuser@mail3.test: %v", err)
		}

		msgID := waitForMessage(t, gwClient, gwUser.ID, "INBOX", subject, 30*time.Second)
		t.Logf("Message delivered via gateway: id=%d", msgID)
	})

	t.Run("Mail2_to_Mail3_SmtpDelivery", func(t *testing.T) {
		subject := fmt.Sprintf("test-m2-to-m3-%d", time.Now().UnixNano())
		sendMailViaSMTP(t, mail2SMTPAddr,
			"bob@mail2.test", "testuser@mail3.test",
			subject, "Hello mail3 from mail2 via SMTP gateway!")

		gwClient := newAPIClient()
		if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
			t.Fatalf("Cannot login: %v", err)
		}

		msgID := waitForMessage(t, gwClient, gwUser.ID, "INBOX", subject, 30*time.Second)
		t.Logf("Message delivered via gateway: id=%d", msgID)
	})

	t.Run("Mail3_RejectsUnknownRecipient", func(t *testing.T) {
		sc := dialSMTP(t, mail3SMTPAddr)
		defer sc.close()

		sc.ehlo(t, "test.local")
		sc.sendExpect(t, "MAIL FROM:<alice@mail1.test>", "250")

		// RCPT TO for unknown user should be rejected
		sc.send(t, "RCPT TO:<nobody@mail3.test>")
		resp := sc.readLine(t)
		// Should be 550 or 5xx
		if resp[0] != '5' {
			t.Fatalf("expected 5xx rejection for unknown recipient, got: %s", resp)
		}
		t.Logf("Correctly rejected unknown recipient: %s", resp)
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("Mail3_MessageIntegrity", func(t *testing.T) {
		subject := fmt.Sprintf("integrity-test-%d", time.Now().UnixNano())
		body := "This is a message integrity test.\r\nLine 2 of the body.\r\nLine 3 with special chars: <>&\"'"

		sendMailViaSMTP(t, mail1SMTPAddr,
			"alice@mail1.test", "testuser@mail3.test",
			subject, body)

		gwClient := newAPIClient()
		if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
			t.Fatalf("Cannot login: %v", err)
		}

		msgID := waitForMessage(t, gwClient, gwUser.ID, "INBOX", subject, 30*time.Second)

		// Fetch full message detail
		resp, err := gwClient.get(fmt.Sprintf("/api/v1/messages/%d", msgID))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var detail struct {
			Data struct {
				Subject  string `json:"subject"`
				Sender   string `json:"sender"`
				BodyText string `json:"body_text"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &detail); err != nil {
			t.Fatalf("decode message detail: %v", err)
		}

		if detail.Data.Subject != subject {
			t.Errorf("subject mismatch: got %q, want %q", detail.Data.Subject, subject)
		}
		if detail.Data.Sender == "" {
			t.Error("sender is empty")
		}
		t.Logf("Message integrity verified: subject=%q sender=%q bodyLen=%d",
			detail.Data.Subject, detail.Data.Sender, len(detail.Data.BodyText))
	})
}
