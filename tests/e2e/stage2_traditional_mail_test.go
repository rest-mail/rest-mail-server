package e2e

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testStage2TraditionalMail(t *testing.T) {
	client := newAPIClient()

	// Decomposed-testbed model: every instance owns its users. The reference
	// servers (mail1/mail2) seed alice+bob@mail1.test and charlie+diana@
	// mail2.test in their OWN databases — they are not entities in the product
	// API, so delivery to them is verified over IMAP, exactly as a user of
	// that server would. The product API is only authoritative for
	// restmail.test; its admin surface is exercised with the seeded RBAC
	// admin below.

	t.Run("Setup", func(t *testing.T) {
		if err := client.loginAdmin("admin", "admin123!@"); err != nil {
			t.Fatalf("RBAC admin login failed: %v", err)
		}
	})

	// The admin API can still provision domains — but ONLY restmail-owned
	// ones. Creating mail1.test/mail2.test here (as the shared-DB era did)
	// would make the product believe it owns the reference domains and stop
	// relaying to them. Prove the provisioning path with a throwaway domain
	// and clean it up.
	t.Run("CreateDomains", func(t *testing.T) {
		if client.token == "" {
			t.Skip("no admin token")
		}
		d := createDomain(t, client, "e2e-stage2.test", "traditional")
		resp, err := client.delete(fmt.Sprintf("/api/v1/admin/domains/%d", d.ID))
		requireNoError(t, err)
		resp.Body.Close()
		t.Logf("throwaway domain %d created and deleted", d.ID)
	})

	// Provision a probe mailbox on the product's own domain; the
	// ApiCreatedUser_* subtests below verify it is visible to the SMTP and
	// IMAP planes of the product.
	probeAddr := "e2eprobe@restmail.test"
	t.Run("CreateMailboxes", func(t *testing.T) {
		if client.token == "" {
			t.Skip("no admin token")
		}
		probe := createMailbox(t, client, probeAddr, adminPassword, "E2E Probe")
		t.Logf("probe mailbox %s id=%d", probeAddr, probe.ID)
	})

	t.Run("Mail1_PostfixAcceptsSmtp", func(t *testing.T) {
		sc := dialSMTP(t, mail1SMTPAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")
		t.Logf("mail1 EHLO capabilities: %v", caps)

		// MAIL FROM should be accepted
		sc.sendExpect(t, "MAIL FROM:<test@test.local>", "250")
		// RCPT TO for a known user should be accepted
		sc.sendExpect(t, "RCPT TO:<alice@mail1.test>", "250")
		sc.sendExpect(t, "RSET", "250")
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("Mail2_PostfixAcceptsSmtp", func(t *testing.T) {
		sc := dialSMTP(t, mail2SMTPAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")
		t.Logf("mail2 EHLO capabilities: %v", caps)

		sc.sendExpect(t, "MAIL FROM:<test@test.local>", "250")
		sc.sendExpect(t, "RCPT TO:<charlie@mail2.test>", "250")
		sc.sendExpect(t, "RSET", "250")
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("Mail1_DovecotAuth", func(t *testing.T) {
		ic := dialIMAP(t, mail1IMAPAddr)
		defer ic.close()

		result, _ := ic.command(t, "LOGIN alice@mail1.test "+adminPassword)
		if !strings.Contains(result, "OK") {
			t.Fatalf("Dovecot auth failed for alice@mail1.test: %s", result)
		}
		ic.command(t, "LOGOUT")
	})

	t.Run("Mail2_DovecotAuth", func(t *testing.T) {
		ic := dialIMAP(t, mail2IMAPAddr)
		defer ic.close()

		result, _ := ic.command(t, "LOGIN charlie@mail2.test "+adminPassword)
		if !strings.Contains(result, "OK") {
			t.Fatalf("Dovecot auth failed for charlie@mail2.test: %s", result)
		}
		ic.command(t, "LOGOUT")
	})

	subject1to2 := fmt.Sprintf("test-mail1to2-%d", time.Now().UnixNano())
	t.Run("Mail1_to_Mail2_Delivery", func(t *testing.T) {
		sendMailViaSMTP(t, mail1SMTPAddr,
			"alice@mail1.test", "charlie@mail2.test",
			subject1to2, "Hello Charlie from Alice via SMTP!")

		// charlie lives on the mail2 reference server — verify over its IMAP.
		waitForImapMessage(t, mail2IMAPAddr, "charlie@mail2.test", adminPassword, subject1to2, 30*time.Second)
		t.Log("Message delivered to charlie@mail2.test")
	})

	subject2to1 := fmt.Sprintf("test-mail2to1-%d", time.Now().UnixNano())
	t.Run("Mail2_to_Mail1_Delivery", func(t *testing.T) {
		sendMailViaSMTP(t, mail2SMTPAddr,
			"charlie@mail2.test", "alice@mail1.test",
			subject2to1, "Hello Alice from Charlie via SMTP!")

		waitForImapMessage(t, mail1IMAPAddr, "alice@mail1.test", adminPassword, subject2to1, 30*time.Second)
		t.Log("Message delivered to alice@mail1.test")
	})

	t.Run("Mail1_ImapReadback", func(t *testing.T) {
		ic := dialIMAP(t, mail1IMAPAddr)
		defer ic.close()

		ic.login(t, "alice@mail1.test", adminPassword)

		result, lines := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", result)
		}
		t.Logf("INBOX select: %v", lines)

		// FETCH to verify messages exist
		_, fetchLines := ic.command(t, "FETCH 1:* (FLAGS ENVELOPE)")
		t.Logf("FETCH returned %d lines", len(fetchLines))
		ic.command(t, "LOGOUT")
	})

	t.Run("Mail2_ImapReadback", func(t *testing.T) {
		ic := dialIMAP(t, mail2IMAPAddr)
		defer ic.close()

		ic.login(t, "charlie@mail2.test", adminPassword)

		result, lines := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", result)
		}
		t.Logf("INBOX select: %v", lines)
		ic.command(t, "LOGOUT")
	})

	// In the shared-DB era these proved an API-created user became visible to
	// postfix/dovecot. The decomposed equivalent: an admin-API-created mailbox
	// on the product's own domain is visible to the product's SMTP plane
	// (RCPT accepted — deliberately no DATA, so greylisting never enters the
	// picture) and to its IMAP gateway (login succeeds).
	t.Run("ApiCreatedUser_VisibleToPostfix", func(t *testing.T) {
		if client.token == "" {
			t.Skip("no admin token")
		}
		sc := dialSMTP(t, restmailSMTPAddr)
		defer sc.close()
		sc.ehlo(t, "test.local")
		sc.sendExpect(t, "MAIL FROM:<charlie@mail2.test>", "250")
		sc.sendExpect(t, "RCPT TO:<"+probeAddr+">", "250")
		sc.sendExpect(t, "RSET", "250")
		sc.sendExpect(t, "QUIT", "221")
		t.Logf("SMTP plane accepts RCPT for API-created %s", probeAddr)
	})

	t.Run("ApiCreatedUser_VisibleToDovecot", func(t *testing.T) {
		ic := dialIMAP(t, restmailIMAPAddr)
		defer ic.close()

		// The product's IMAP gateway (correctly) refuses plaintext LOGIN.
		ic.starttls(t)
		result, _ := ic.command(t, "LOGIN "+probeAddr+" "+adminPassword)
		if !strings.Contains(result, "OK") {
			t.Fatalf("IMAP gateway cannot auth API-created user: %s", result)
		}
		ic.command(t, "LOGOUT")
	})

	t.Run("Mail1_ImapFetchContent", func(t *testing.T) {
		ic := dialIMAP(t, mail1IMAPAddr)
		defer ic.close()

		ic.login(t, "alice@mail1.test", adminPassword)

		result, lines := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", result)
		}

		// Extract EXISTS count to fetch the last message
		exists := 0
		for _, line := range lines {
			if strings.Contains(line, "EXISTS") {
				_, _ = fmt.Sscanf(line, "* %d EXISTS", &exists)
			}
		}
		if exists == 0 {
			t.Skip("no messages in INBOX to fetch")
		}

		// FETCH the last message body
		body := ic.fetchBody(t, exists)
		if body == "" {
			t.Fatal("FETCH BODY[] returned empty response")
		}
		// Verify it looks like an email (has headers)
		if !strings.Contains(body, "From:") && !strings.Contains(body, "Subject:") {
			t.Errorf("FETCH BODY[] does not contain expected email headers: %.200s", body)
		}
		t.Logf("IMAP FETCH BODY[] returned %d bytes", len(body))
		ic.command(t, "LOGOUT")
	})

	t.Run("Mail1_Pop3Readback", func(t *testing.T) {
		pc := dialPOP3(t, mail1POP3Addr)
		defer pc.close()

		pc.sendExpect(t, "USER alice@mail1.test", "+OK")
		pc.sendExpect(t, "PASS "+adminPassword, "+OK")

		statResp := pc.stat(t)
		t.Logf("POP3 STAT: %s", statResp)

		// RETR first message
		msg := pc.retr(t, 1)
		if msg == "" {
			t.Fatal("POP3 RETR 1 returned empty")
		}
		if !strings.Contains(msg, "From:") && !strings.Contains(msg, "Subject:") {
			t.Errorf("POP3 RETR does not contain expected headers: %.200s", msg)
		}
		t.Logf("POP3 RETR 1 returned %d bytes", len(msg))
		pc.sendExpect(t, "QUIT", "+OK")
	})

	t.Run("Mail1_SmtpSubmissionAuth", func(t *testing.T) {
		subject := fmt.Sprintf("test-submission-%d", time.Now().UnixNano())

		sendMailViaSubmission(t, mail1SubmitAddr,
			"alice@mail1.test", "charlie@mail2.test",
			"alice@mail1.test", adminPassword,
			subject, "Sent via authenticated SMTP submission!")

		waitForImapMessage(t, mail2IMAPAddr, "charlie@mail2.test", adminPassword, subject, 30*time.Second)
		t.Log("Submission delivery verified via mail2 IMAP")
	})

	// ── Deep protocol-level verification ─────────────────────────────

	t.Run("Mail1_SmtpSubmission_ProtocolWalk", func(t *testing.T) {
		// Manually walk the submission port protocol to verify each step
		sc := dialSMTP(t, mail1SubmitAddr)
		defer sc.close()

		// Step 1: EHLO — must succeed and advertise STARTTLS
		caps := sc.ehlo(t, "test.local")
		if !hasCapability(caps, "STARTTLS") {
			t.Fatal("submission port must advertise STARTTLS before upgrade")
		}
		t.Logf("Pre-TLS capabilities: %v", caps)

		// AUTH should NOT be advertised before STARTTLS on a properly configured
		// submission port (RFC 4954 §4). Some servers do advertise it, so just log.
		if hasCapability(caps, "AUTH") {
			t.Log("Note: AUTH advertised before STARTTLS (some servers allow this)")
		}

		// Step 2: STARTTLS upgrade
		sc.starttls(t)

		// Step 3: Re-EHLO over TLS — must now advertise AUTH
		capsAfterTLS := sc.ehlo(t, "test.local")
		if !hasCapability(capsAfterTLS, "AUTH") {
			t.Fatal("submission port must advertise AUTH after STARTTLS")
		}
		t.Logf("Post-TLS capabilities: %v", capsAfterTLS)

		// Verify AUTH PLAIN is among the mechanisms
		authFound := false
		for _, line := range capsAfterTLS {
			upper := strings.ToUpper(line)
			if len(upper) > 4 && strings.HasPrefix(upper[4:], "AUTH") {
				authFound = true
				if !strings.Contains(upper, "PLAIN") {
					t.Errorf("AUTH line does not include PLAIN mechanism: %s", line)
				}
				t.Logf("AUTH mechanisms: %s", line)
			}
		}
		if !authFound {
			t.Fatal("no AUTH capability line found after STARTTLS")
		}

		// Step 4: AUTH PLAIN with valid credentials
		sc.authPlain(t, "alice@mail1.test", adminPassword)

		// Step 5: Send a message after authenticating
		subject := fmt.Sprintf("test-proto-walk-%d", time.Now().UnixNano())
		sc.sendExpect(t, "MAIL FROM:<alice@mail1.test>", "250")
		sc.sendExpect(t, "RCPT TO:<charlie@mail2.test>", "250")
		sc.sendExpect(t, "DATA", "354")

		msg := fmt.Sprintf("From: alice@mail1.test\r\nTo: charlie@mail2.test\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <proto-walk-%d@test.local>\r\n\r\nSent via manual protocol walk.",
			subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
		sc.send(t, msg)
		sc.sendExpect(t, ".", "250")
		sc.sendExpect(t, "QUIT", "221")

		// Verify delivery on the mail2 reference server
		waitForImapMessage(t, mail2IMAPAddr, "charlie@mail2.test", adminPassword, subject, 30*time.Second)
		t.Log("Protocol walk message delivered")
	})

	t.Run("Mail1_SmtpSubmission_BadCredentials", func(t *testing.T) {
		sc := dialSMTP(t, mail1SubmitAddr)
		defer sc.close()

		sc.ehlo(t, "test.local")
		sc.starttls(t)
		sc.ehlo(t, "test.local")

		// AUTH PLAIN with wrong password — must be rejected
		cred := base64.StdEncoding.EncodeToString([]byte("\x00alice@mail1.test\x00wrongpassword"))
		sc.send(t, "AUTH PLAIN "+cred)
		resp := sc.readLine(t)
		if !strings.HasPrefix(resp, "535") && !strings.HasPrefix(resp, "5") {
			t.Errorf("expected 535/5xx for bad credentials, got: %s", resp)
		} else {
			t.Logf("Bad credentials correctly rejected: %s", resp)
		}
		sc.sendExpect(t, "QUIT", "221")
	})

	// Send a message with a known unique body, then verify it via IMAP FETCH BODY[]
	knownBody := fmt.Sprintf("E2E-IMAP-verify-body-%d", time.Now().UnixNano())
	subjectForImap := fmt.Sprintf("imap-body-verify-%d", time.Now().UnixNano())
	t.Run("Mail1_SendKnownMessage_ForImapVerify", func(t *testing.T) {
		sendMailViaSMTP(t, mail2SMTPAddr,
			"charlie@mail2.test", "alice@mail1.test",
			subjectForImap, knownBody)

		waitForImapMessage(t, mail1IMAPAddr, "alice@mail1.test", adminPassword, subjectForImap, 30*time.Second)
	})

	t.Run("Mail1_ImapFetchBody_VerifyContent", func(t *testing.T) {
		ic := dialIMAP(t, mail1IMAPAddr)
		defer ic.close()

		ic.login(t, "alice@mail1.test", adminPassword)

		result, lines := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", result)
		}

		// Find the message count
		exists := 0
		for _, line := range lines {
			if strings.Contains(line, "EXISTS") {
				_, _ = fmt.Sscanf(line, "* %d EXISTS", &exists)
			}
		}
		if exists == 0 {
			t.Skip("no messages in INBOX")
		}

		// Search for our specific subject
		searchResult, searchLines := ic.command(t, fmt.Sprintf(`SEARCH SUBJECT "%s"`, subjectForImap))
		if !strings.Contains(searchResult, "OK") {
			t.Fatalf("SEARCH failed: %s", searchResult)
		}

		// Parse sequence number from SEARCH response (e.g., "* SEARCH 5")
		var seqNum int
		for _, line := range searchLines {
			if strings.HasPrefix(line, "* SEARCH") {
				parts := strings.Fields(line)
				if len(parts) >= 3 {
					_, _ = fmt.Sscanf(parts[2], "%d", &seqNum)
				}
			}
		}
		if seqNum == 0 {
			// Fall back to fetching the last message
			seqNum = exists
			t.Logf("SEARCH did not return a sequence number; falling back to last message %d", seqNum)
		}

		// FETCH BODY[] for the specific message
		body := ic.fetchBody(t, seqNum)
		if body == "" {
			t.Fatal("FETCH BODY[] returned empty response")
		}

		// Verify the body contains our known content
		if !strings.Contains(body, knownBody) {
			t.Errorf("FETCH BODY[] does not contain expected body text %q; got:\n%.500s", knownBody, body)
		} else {
			t.Logf("IMAP FETCH BODY[] correctly contains known body text (%d bytes total)", len(body))
		}

		// Verify headers are present
		if !strings.Contains(body, "From:") {
			t.Error("FETCH BODY[] missing From: header")
		}
		if !strings.Contains(body, "Subject:") {
			t.Error("FETCH BODY[] missing Subject: header")
		}
		if !strings.Contains(body, subjectForImap) {
			t.Errorf("FETCH BODY[] missing expected subject %q", subjectForImap)
		}

		// FETCH BODY[HEADER] — just the headers
		_, headerLines := ic.command(t, fmt.Sprintf("FETCH %d (BODY[HEADER])", seqNum))
		headerContent := strings.Join(headerLines[:len(headerLines)-1], "\n")
		if !strings.Contains(headerContent, "From:") {
			t.Error("FETCH BODY[HEADER] missing From: header")
		}
		if !strings.Contains(headerContent, "Subject:") {
			t.Error("FETCH BODY[HEADER] missing Subject: header")
		}
		// Headers should NOT contain the body text
		if strings.Contains(headerContent, knownBody) {
			t.Error("FETCH BODY[HEADER] unexpectedly contains body text")
		}
		t.Logf("IMAP FETCH BODY[HEADER] returned %d bytes", len(headerContent))

		// FETCH BODY[TEXT] — just the body
		_, textLines := ic.command(t, fmt.Sprintf("FETCH %d (BODY[TEXT])", seqNum))
		textContent := strings.Join(textLines[:len(textLines)-1], "\n")
		if !strings.Contains(textContent, knownBody) {
			t.Errorf("FETCH BODY[TEXT] does not contain known body text %q", knownBody)
		}
		t.Logf("IMAP FETCH BODY[TEXT] returned %d bytes", len(textContent))

		ic.command(t, "LOGOUT")
	})

	// Send a known message and verify via POP3 RETR
	knownBodyPop := fmt.Sprintf("E2E-POP3-verify-body-%d", time.Now().UnixNano())
	subjectForPop := fmt.Sprintf("pop3-body-verify-%d", time.Now().UnixNano())
	t.Run("Mail1_SendKnownMessage_ForPop3Verify", func(t *testing.T) {
		sendMailViaSMTP(t, mail2SMTPAddr,
			"charlie@mail2.test", "alice@mail1.test",
			subjectForPop, knownBodyPop)

		waitForImapMessage(t, mail1IMAPAddr, "alice@mail1.test", adminPassword, subjectForPop, 30*time.Second)
	})

	t.Run("Mail1_Pop3Retr_VerifyContent", func(t *testing.T) {
		pc := dialPOP3(t, mail1POP3Addr)
		defer pc.close()

		pc.sendExpect(t, "USER alice@mail1.test", "+OK")
		pc.sendExpect(t, "PASS "+adminPassword, "+OK")

		// STAT to get message count
		statResp := pc.stat(t)
		t.Logf("POP3 STAT: %s", statResp)

		// Parse message count from STAT response ("+OK n size")
		var msgCount, totalSize int
		_, _ = fmt.Sscanf(statResp, "+OK %d %d", &msgCount, &totalSize)
		if msgCount == 0 {
			t.Skip("no messages in POP3 mailbox")
		}
		t.Logf("POP3 mailbox has %d messages, %d bytes total", msgCount, totalSize)

		// RETR the last message (most recently delivered)
		msg := pc.retr(t, msgCount)
		if msg == "" {
			t.Fatal("POP3 RETR returned empty")
		}

		// Verify the message contains our known content
		if !strings.Contains(msg, knownBodyPop) {
			t.Errorf("POP3 RETR does not contain expected body text %q; got:\n%.500s", knownBodyPop, msg)
		} else {
			t.Logf("POP3 RETR correctly contains known body text (%d bytes)", len(msg))
		}

		// Verify headers
		if !strings.Contains(msg, "From:") {
			t.Error("POP3 RETR missing From: header")
		}
		if !strings.Contains(msg, "Subject:") {
			t.Error("POP3 RETR missing Subject: header")
		}
		if !strings.Contains(msg, subjectForPop) {
			t.Errorf("POP3 RETR missing expected subject %q", subjectForPop)
		}

		// LIST command to verify message sizes are reported
		pc.sendExpect(t, "LIST", "+OK")
		var listEntries []string
		for {
			line := pc.readLine(t)
			if line == "." {
				break
			}
			listEntries = append(listEntries, line)
		}
		if len(listEntries) != msgCount {
			t.Errorf("POP3 LIST returned %d entries, expected %d", len(listEntries), msgCount)
		}
		t.Logf("POP3 LIST returned %d entries", len(listEntries))

		// UIDL command to verify unique IDs
		pc.sendExpect(t, "UIDL", "+OK")
		var uidlEntries []string
		for {
			line := pc.readLine(t)
			if line == "." {
				break
			}
			uidlEntries = append(uidlEntries, line)
		}
		if len(uidlEntries) != msgCount {
			t.Errorf("POP3 UIDL returned %d entries, expected %d", len(uidlEntries), msgCount)
		}
		t.Logf("POP3 UIDL returned %d entries", len(uidlEntries))

		pc.sendExpect(t, "QUIT", "+OK")
	})

	t.Run("Mail1_Pop3_BadCredentials", func(t *testing.T) {
		pc := dialPOP3(t, mail1POP3Addr)
		defer pc.close()

		pc.sendExpect(t, "USER alice@mail1.test", "+OK")
		// Wrong password should fail
		_ = pc.conn.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintf(pc.conn, "PASS wrongpassword\r\n")
		resp := pc.readLine(t)
		if !strings.HasPrefix(resp, "-ERR") {
			t.Errorf("expected -ERR for bad POP3 credentials, got: %s", resp)
		} else {
			t.Logf("Bad POP3 credentials correctly rejected: %s", resp)
		}
	})
}
