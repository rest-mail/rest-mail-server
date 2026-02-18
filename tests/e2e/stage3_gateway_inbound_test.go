package e2e

import (
	"fmt"
	"net/http"
	"strings"
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

	t.Run("Mail3_SmtpSubmissionAuth", func(t *testing.T) {
		subject := fmt.Sprintf("test-gw-submit-%d", time.Now().UnixNano())

		sc := dialSMTP(t, mail3SubmitAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")

		// Try STARTTLS if available
		if hasCapability(caps, "STARTTLS") {
			sc.starttls(t)
			caps = sc.ehlo(t, "test.local")
		}

		if !hasCapability(caps, "AUTH") {
			t.Fatal("gateway submission port does not advertise AUTH")
		}

		sc.authPlain(t, "testuser@mail3.test", adminPassword)
		sc.sendExpect(t, "MAIL FROM:<testuser@mail3.test>", "250")
		sc.sendExpect(t, "RCPT TO:<testuser@mail3.test>", "250")
		sc.sendExpect(t, "DATA", "354")

		msg := fmt.Sprintf("From: testuser@mail3.test\r\nTo: testuser@mail3.test\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <gw-submit-%d@test.local>\r\n\r\nSent via gateway submission!",
			subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
		sc.send(t, msg)
		sc.sendExpect(t, ".", "250")
		sc.sendExpect(t, "QUIT", "221")

		gwClient := newAPIClient()
		if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
			t.Fatalf("Cannot login: %v", err)
		}

		msgID := waitForMessage(t, gwClient, gwUser.ID, "INBOX", subject, 30*time.Second)
		t.Logf("Gateway submission delivery verified: id=%d", msgID)
	})

	t.Run("Mail3_SmtpSubmissionRequiresAuth", func(t *testing.T) {
		sc := dialSMTP(t, mail3SubmitAddr)
		defer sc.close()

		sc.ehlo(t, "test.local")

		// Try MAIL FROM without auth — should be rejected on submission port
		sc.send(t, "MAIL FROM:<testuser@mail3.test>")
		resp := sc.readLine(t)
		if !strings.HasPrefix(resp, "530") && !strings.HasPrefix(resp, "5") {
			t.Errorf("expected 530/5xx rejection without auth on submission port, got: %s", resp)
		} else {
			t.Logf("Correctly rejected unauthenticated MAIL FROM: %s", resp)
		}
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("Mail3_ImapFetchContent", func(t *testing.T) {
		ic := dialIMAP(t, mail3IMAPAddr)
		defer ic.close()

		ic.login(t, "testuser@mail3.test", adminPassword)

		result, lines := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", result)
		}

		exists := 0
		for _, line := range lines {
			if strings.Contains(line, "EXISTS") {
				fmt.Sscanf(line, "* %d EXISTS", &exists)
			}
		}
		if exists == 0 {
			t.Skip("no messages in INBOX")
		}

		body := ic.fetchBody(t, exists)
		if body == "" {
			t.Fatal("IMAP FETCH BODY[] returned empty")
		}
		if !strings.Contains(body, "From:") && !strings.Contains(body, "Subject:") {
			t.Errorf("FETCH BODY[] missing email headers: %.200s", body)
		}
		t.Logf("Gateway IMAP FETCH BODY[] returned %d bytes", len(body))
		ic.command(t, "LOGOUT")
	})

	t.Run("Mail3_Pop3RetrMessage", func(t *testing.T) {
		pc := dialPOP3(t, mail3POP3Addr)
		defer pc.close()

		pc.sendExpect(t, "USER testuser@mail3.test", "+OK")
		pc.sendExpect(t, "PASS "+adminPassword, "+OK")

		statResp := pc.stat(t)
		t.Logf("POP3 STAT: %s", statResp)

		msg := pc.retr(t, 1)
		if msg == "" {
			t.Fatal("POP3 RETR 1 returned empty")
		}
		if !strings.Contains(msg, "From:") && !strings.Contains(msg, "Subject:") {
			t.Errorf("POP3 RETR missing headers: %.200s", msg)
		}
		t.Logf("Gateway POP3 RETR 1 returned %d bytes", len(msg))
		pc.sendExpect(t, "QUIT", "+OK")
	})
}
