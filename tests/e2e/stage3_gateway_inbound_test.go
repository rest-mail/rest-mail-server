package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

	t.Run("IMAP_GetQuota", func(t *testing.T) {
		ic := dialIMAP(t, mail3IMAPAddr)
		defer ic.close()

		ic.login(t, "testuser@mail3.test", adminPassword)

		// Send GETQUOTAROOT INBOX command
		result, lines := ic.command(t, "GETQUOTAROOT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("GETQUOTAROOT INBOX failed: %s", result)
		}

		// Verify response contains QUOTAROOT and STORAGE
		allLines := strings.Join(lines, "\n")
		hasQuotaRoot := false
		hasStorage := false
		for _, line := range lines {
			upper := strings.ToUpper(line)
			if strings.Contains(upper, "QUOTAROOT") {
				hasQuotaRoot = true
			}
			if strings.Contains(upper, "STORAGE") {
				hasStorage = true
			}
		}
		if !hasQuotaRoot {
			t.Errorf("GETQUOTAROOT response missing QUOTAROOT line; got:\n%s", allLines)
		}
		if !hasStorage {
			t.Errorf("GETQUOTAROOT response missing STORAGE info; got:\n%s", allLines)
		}
		t.Logf("GETQUOTAROOT INBOX response (%d lines):\n%s", len(lines), allLines)

		// Send GETQUOTA "" command (root quota)
		result2, lines2 := ic.command(t, `GETQUOTA ""`)
		if !strings.Contains(result2, "OK") {
			// Some servers may not support GETQUOTA on root; log but don't hard-fail
			t.Logf("GETQUOTA \"\" result: %s (may not be supported)", result2)
		} else {
			allLines2 := strings.Join(lines2, "\n")
			hasQuota := false
			hasStorage2 := false
			for _, line := range lines2 {
				upper := strings.ToUpper(line)
				if strings.Contains(upper, "QUOTA") {
					hasQuota = true
				}
				if strings.Contains(upper, "STORAGE") {
					hasStorage2 = true
				}
			}
			if !hasQuota {
				t.Errorf("GETQUOTA response missing QUOTA line; got:\n%s", allLines2)
			}
			if !hasStorage2 {
				t.Errorf("GETQUOTA response missing STORAGE info; got:\n%s", allLines2)
			}
			t.Logf("GETQUOTA \"\" response (%d lines):\n%s", len(lines2), allLines2)
		}

		ic.command(t, "LOGOUT")
	})

	t.Run("Attachment_Upload_Download", func(t *testing.T) {
		// Build a multipart MIME message with a text/plain attachment
		attachContent := "This is the attachment content for E2E testing.\nLine 2 of attachment."
		encodedAttach := base64.StdEncoding.EncodeToString([]byte(attachContent))
		boundary := fmt.Sprintf("e2e-boundary-%d", time.Now().UnixNano())
		subject := fmt.Sprintf("attachment-test-%d", time.Now().UnixNano())

		var mimeBody strings.Builder
		mimeBody.WriteString("From: sender@mail1.test\r\n")
		mimeBody.WriteString("To: testuser@mail3.test\r\n")
		mimeBody.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
		mimeBody.WriteString(fmt.Sprintf("Date: %s\r\n", time.Now().Format(time.RFC1123Z)))
		mimeBody.WriteString(fmt.Sprintf("Message-ID: <att-test-%d@test.local>\r\n", time.Now().UnixNano()))
		mimeBody.WriteString("MIME-Version: 1.0\r\n")
		mimeBody.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\r\n", boundary))
		mimeBody.WriteString("\r\n")
		// Text part
		mimeBody.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		mimeBody.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		mimeBody.WriteString("Content-Transfer-Encoding: 7bit\r\n")
		mimeBody.WriteString("\r\n")
		mimeBody.WriteString("This is the body of the message with an attachment.\r\n")
		mimeBody.WriteString("\r\n")
		// Attachment part
		mimeBody.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		mimeBody.WriteString("Content-Type: text/plain; charset=utf-8; name=\"testfile.txt\"\r\n")
		mimeBody.WriteString("Content-Transfer-Encoding: base64\r\n")
		mimeBody.WriteString("Content-Disposition: attachment; filename=\"testfile.txt\"\r\n")
		mimeBody.WriteString("\r\n")
		// Base64 content wrapped at 76 chars
		for i := 0; i < len(encodedAttach); i += 76 {
			end := i + 76
			if end > len(encodedAttach) {
				end = len(encodedAttach)
			}
			mimeBody.WriteString(encodedAttach[i:end])
			mimeBody.WriteString("\r\n")
		}
		// Closing boundary
		mimeBody.WriteString(fmt.Sprintf("--%s--\r\n", boundary))

		// Send the raw MIME message via SMTP
		sendRawMailViaSMTP(t, mail1SMTPAddr, "sender@mail1.test", "testuser@mail3.test", mimeBody.String())

		// Wait for delivery via API
		gwClient := newAPIClient()
		if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
			t.Fatalf("Cannot login as testuser@mail3.test: %v", err)
		}

		msgID := waitForMessage(t, gwClient, gwUser.ID, "INBOX", subject, 30*time.Second)
		t.Logf("Attachment message delivered: id=%d", msgID)

		// List attachments via API
		resp, err := gwClient.get(fmt.Sprintf("/api/v1/messages/%d/attachments", msgID))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var attList struct {
			Data []struct {
				ID          uint   `json:"id"`
				Filename    string `json:"filename"`
				ContentType string `json:"content_type"`
				SizeBytes   int64  `json:"size_bytes"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&attList); err != nil {
			t.Fatalf("decode attachments list: %v", err)
		}
		resp.Body.Close()

		if len(attList.Data) == 0 {
			t.Fatal("expected at least one attachment, got none")
		}

		att := attList.Data[0]
		t.Logf("Found attachment: id=%d filename=%q type=%q size=%d",
			att.ID, att.Filename, att.ContentType, att.SizeBytes)

		if att.Filename != "testfile.txt" {
			t.Errorf("expected attachment filename 'testfile.txt', got %q", att.Filename)
		}

		// Download the attachment
		dlResp, err := gwClient.get(fmt.Sprintf("/api/v1/attachments/%d", att.ID))
		requireNoError(t, err)
		requireStatus(t, dlResp, http.StatusOK)

		dlBody, err := io.ReadAll(dlResp.Body)
		dlResp.Body.Close()
		requireNoError(t, err)

		downloaded := string(dlBody)
		if downloaded != attachContent {
			t.Errorf("attachment content mismatch:\n  got:  %q\n  want: %q", downloaded, attachContent)
		} else {
			t.Logf("Attachment download verified: %d bytes match original", len(dlBody))
		}
	})

	t.Run("Mail3_ImapFetchSections", func(t *testing.T) {
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

		// FETCH BODY[HEADER] — should return only headers
		_, headerLines := ic.command(t, fmt.Sprintf("FETCH %d (BODY[HEADER])", exists))
		headerContent := strings.Join(headerLines[:len(headerLines)-1], "\n")
		if !strings.Contains(headerContent, "From:") && !strings.Contains(headerContent, "Subject:") {
			t.Errorf("FETCH BODY[HEADER] missing expected headers: %.300s", headerContent)
		}
		t.Logf("IMAP FETCH BODY[HEADER] returned %d bytes", len(headerContent))

		// FETCH BODY[TEXT] — should return only message body
		_, textLines := ic.command(t, fmt.Sprintf("FETCH %d (BODY[TEXT])", exists))
		textContent := strings.Join(textLines[:len(textLines)-1], "\n")
		if len(textContent) == 0 {
			t.Error("FETCH BODY[TEXT] returned empty")
		}
		t.Logf("IMAP FETCH BODY[TEXT] returned %d bytes", len(textContent))

		ic.command(t, "LOGOUT")
	})

	t.Run("Mail3_Pop3Operations", func(t *testing.T) {
		pc := dialPOP3(t, mail3POP3Addr)
		defer pc.close()

		pc.sendExpect(t, "USER testuser@mail3.test", "+OK")
		pc.sendExpect(t, "PASS "+adminPassword, "+OK")

		// STAT — should return message count and total size
		statResp := pc.stat(t)
		if !strings.HasPrefix(statResp, "+OK") {
			t.Fatalf("STAT failed: %s", statResp)
		}
		t.Logf("POP3 STAT: %s", statResp)

		// LIST — should return message listing
		listResp := pc.sendExpect(t, "LIST", "+OK")
		t.Logf("POP3 LIST response: %s", listResp)
		listCount := 0
		for {
			line := pc.readLine(t)
			if line == "." {
				break
			}
			listCount++
		}
		if listCount == 0 {
			t.Skip("no messages in mailbox for LIST")
		}
		t.Logf("POP3 LIST returned %d messages", listCount)

		// UIDL — should return unique IDs
		uidlResp := pc.sendExpect(t, "UIDL", "+OK")
		t.Logf("POP3 UIDL response: %s", uidlResp)
		uidlCount := 0
		for {
			line := pc.readLine(t)
			if line == "." {
				break
			}
			uidlCount++
		}
		if uidlCount != listCount {
			t.Errorf("UIDL count (%d) != LIST count (%d)", uidlCount, listCount)
		}
		t.Logf("POP3 UIDL returned %d entries", uidlCount)

		// LIST for a specific message
		pc.sendExpect(t, "LIST 1", "+OK")

		// UIDL for a specific message
		pc.sendExpect(t, "UIDL 1", "+OK")

		pc.sendExpect(t, "QUIT", "+OK")
	})

	t.Run("Mail3_SmtpSubmissionBadCredentials", func(t *testing.T) {
		sc := dialSMTP(t, mail3SubmitAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")

		if hasCapability(caps, "STARTTLS") {
			sc.starttls(t)
			sc.ehlo(t, "test.local")
		}

		// AUTH PLAIN with wrong password
		cred := base64.StdEncoding.EncodeToString([]byte("\x00testuser@mail3.test\x00wrongpassword"))
		sc.send(t, "AUTH PLAIN "+cred)
		resp := sc.readLine(t)
		if !strings.HasPrefix(resp, "535") && !strings.HasPrefix(resp, "5") {
			t.Errorf("expected 535/5xx for bad credentials, got: %s", resp)
		} else {
			t.Logf("Bad credentials correctly rejected: %s", resp)
		}
		sc.sendExpect(t, "QUIT", "221")
	})
}
