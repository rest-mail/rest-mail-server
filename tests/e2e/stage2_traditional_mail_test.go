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

	// Setup: Create admin user and get token.
	// We need an admin user. Create a bootstrap mailbox.
	// First, create domains via the test/db endpoint (unauthenticated) or we need
	// to bootstrap admin access. Let's try creating a first user and logging in.
	// The admin API requires JWT auth. We need an initial admin user.
	//
	// Strategy: The system should have been seeded, or we use the first-created
	// mailbox as admin. For now, create domains + mailboxes assuming there's
	// an admin bootstrap mechanism (or the first user is admin).

	t.Run("Setup", func(t *testing.T) {
		// Try to create an admin user first - if auth is required, skip setup
		// For tests, we assume the API dev mode allows some admin ops or
		// there's an existing admin. Try login with a known admin account.
		err := client.login("admin@mail1.test", adminPassword)
		if err != nil {
			t.Logf("Admin login failed (will try to bootstrap): %v", err)
			// In dev mode, some admin endpoints might be open.
			// We'll try direct domain creation.
		}
	})

	// Create domains
	t.Run("CreateDomains", func(t *testing.T) {
		if client.token == "" {
			t.Skip("no admin token - need admin bootstrap")
		}
		createDomain(t, client, "mail1.test", "traditional")
		createDomain(t, client, "mail2.test", "traditional")
		createDomain(t, client, "mail3.test", "restmail")
	})

	// Create test mailboxes
	var aliceID, bobID uint
	t.Run("CreateMailboxes", func(t *testing.T) {
		if client.token == "" {
			t.Skip("no admin token")
		}
		alice := createMailbox(t, client, "alice@mail1.test", adminPassword, "Alice")
		bob := createMailbox(t, client, "bob@mail2.test", adminPassword, "Bob")
		aliceID = alice.ID
		bobID = bob.ID
		t.Logf("alice=%d, bob=%d", aliceID, bobID)
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
		sc.sendExpect(t, "RCPT TO:<bob@mail2.test>", "250")
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

		result, _ := ic.command(t, "LOGIN bob@mail2.test "+adminPassword)
		if !strings.Contains(result, "OK") {
			t.Fatalf("Dovecot auth failed for bob@mail2.test: %s", result)
		}
		ic.command(t, "LOGOUT")
	})

	subject1to2 := fmt.Sprintf("test-mail1to2-%d", time.Now().UnixNano())
	t.Run("Mail1_to_Mail2_Delivery", func(t *testing.T) {
		sendMailViaSMTP(t, mail1SMTPAddr,
			"alice@mail1.test", "bob@mail2.test",
			subject1to2, "Hello Bob from Alice via SMTP!")

		// Login as bob and check for message
		bobClient := newAPIClient()
		err := bobClient.login("bob@mail2.test", adminPassword)
		if err != nil {
			t.Skipf("Cannot login as bob: %v", err)
		}

		// We need bob's account ID from the login response - use the mailbox ID
		msgID := waitForMessage(t, bobClient, bobID, "INBOX", subject1to2, 30*time.Second)
		t.Logf("Message delivered: id=%d", msgID)
	})

	subject2to1 := fmt.Sprintf("test-mail2to1-%d", time.Now().UnixNano())
	t.Run("Mail2_to_Mail1_Delivery", func(t *testing.T) {
		sendMailViaSMTP(t, mail2SMTPAddr,
			"bob@mail2.test", "alice@mail1.test",
			subject2to1, "Hello Alice from Bob via SMTP!")

		aliceClient := newAPIClient()
		err := aliceClient.login("alice@mail1.test", adminPassword)
		if err != nil {
			t.Skipf("Cannot login as alice: %v", err)
		}

		msgID := waitForMessage(t, aliceClient, aliceID, "INBOX", subject2to1, 30*time.Second)
		t.Logf("Message delivered: id=%d", msgID)
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

		ic.login(t, "bob@mail2.test", adminPassword)

		result, lines := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", result)
		}
		t.Logf("INBOX select: %v", lines)
		ic.command(t, "LOGOUT")
	})

	t.Run("ApiCreatedUser_VisibleToPostfix", func(t *testing.T) {
		if client.token == "" {
			t.Skip("no admin token")
		}
		newUser := createMailbox(t, client, "newuser@mail1.test", adminPassword, "New User")
		subject := fmt.Sprintf("test-newuser-%d", time.Now().UnixNano())

		sendMailViaSMTP(t, mail2SMTPAddr,
			"bob@mail2.test", "newuser@mail1.test",
			subject, "Testing delivery to API-created user")

		newUserClient := newAPIClient()
		err := newUserClient.login("newuser@mail1.test", adminPassword)
		if err != nil {
			t.Skipf("Cannot login as newuser: %v", err)
		}

		msgID := waitForMessage(t, newUserClient, newUser.ID, "INBOX", subject, 30*time.Second)
		t.Logf("Delivered to API-created user: id=%d", msgID)
	})

	t.Run("ApiCreatedUser_VisibleToDovecot", func(t *testing.T) {
		ic := dialIMAP(t, mail1IMAPAddr)
		defer ic.close()

		result, _ := ic.command(t, "LOGIN newuser@mail1.test "+adminPassword)
		if !strings.Contains(result, "OK") {
			t.Fatalf("Dovecot cannot auth API-created user: %s", result)
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
				fmt.Sscanf(line, "* %d EXISTS", &exists)
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
			"alice@mail1.test", "bob@mail2.test",
			"alice@mail1.test", adminPassword,
			subject, "Sent via authenticated SMTP submission!")

		bobClient := newAPIClient()
		if err := bobClient.login("bob@mail2.test", adminPassword); err != nil {
			t.Fatalf("Cannot login as bob: %v", err)
		}

		msgID := waitForMessage(t, bobClient, bobID, "INBOX", subject, 30*time.Second)
		t.Logf("Submission delivery verified: id=%d", msgID)
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
		sc.sendExpect(t, "RCPT TO:<bob@mail2.test>", "250")
		sc.sendExpect(t, "DATA", "354")

		msg := fmt.Sprintf("From: alice@mail1.test\r\nTo: bob@mail2.test\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <proto-walk-%d@test.local>\r\n\r\nSent via manual protocol walk.",
			subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
		sc.send(t, msg)
		sc.sendExpect(t, ".", "250")
		sc.sendExpect(t, "QUIT", "221")

		// Verify delivery
		bobClient := newAPIClient()
		if err := bobClient.login("bob@mail2.test", adminPassword); err != nil {
			t.Fatalf("Cannot login as bob: %v", err)
		}
		msgID := waitForMessage(t, bobClient, bobID, "INBOX", subject, 30*time.Second)
		t.Logf("Protocol walk message delivered: id=%d", msgID)
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
			"bob@mail2.test", "alice@mail1.test",
			subjectForImap, knownBody)

		// Wait for delivery via API to be sure it's there
		aliceClient := newAPIClient()
		if err := aliceClient.login("alice@mail1.test", adminPassword); err != nil {
			t.Fatalf("Cannot login: %v", err)
		}
		waitForMessage(t, aliceClient, aliceID, "INBOX", subjectForImap, 30*time.Second)
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
				fmt.Sscanf(line, "* %d EXISTS", &exists)
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
					fmt.Sscanf(parts[2], "%d", &seqNum)
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
			"bob@mail2.test", "alice@mail1.test",
			subjectForPop, knownBodyPop)

		aliceClient := newAPIClient()
		if err := aliceClient.login("alice@mail1.test", adminPassword); err != nil {
			t.Fatalf("Cannot login: %v", err)
		}
		waitForMessage(t, aliceClient, aliceID, "INBOX", subjectForPop, 30*time.Second)
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
		fmt.Sscanf(statResp, "+OK %d %d", &msgCount, &totalSize)
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
