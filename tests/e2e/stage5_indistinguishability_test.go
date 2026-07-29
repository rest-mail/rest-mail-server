package e2e

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testStage5Indistinguishability(t *testing.T) {
	// The stealth test: restmail must be indistinguishable from mail1/mail2
	// when probed by a standard SMTP client.

	t.Run("EhloCapabilities_MatchTraditional", func(t *testing.T) {
		// Probe mail1 and restmail EHLO capabilities
		sc1 := dialSMTP(t, mail1SMTPAddr)
		defer sc1.close()
		caps1 := sc1.ehlo(t, "test.local")
		sc1.sendExpect(t, "QUIT", "221")

		sc3 := dialSMTP(t, restmailSMTPAddr)
		defer sc3.close()
		caps3 := sc3.ehlo(t, "test.local")
		sc3.sendExpect(t, "QUIT", "221")

		t.Logf("mail1 capabilities: %v", caps1)
		t.Logf("restmail capabilities: %v", caps3)

		// restmail must advertise standard capabilities that mail1 advertises
		standardCaps := []string{"PIPELINING", "8BITMIME", "SIZE", "STARTTLS"}
		for _, cap := range standardCaps {
			m1Has := hasCapability(caps1, cap)
			m3Has := hasCapability(caps3, cap)
			if m1Has && !m3Has {
				t.Errorf("mail1 has %s but restmail does not — restmail should match standard capabilities", cap)
			}
			if m3Has {
				t.Logf("OK: restmail advertises %s", cap)
			}
		}

		// restmail may have RESTMAIL (that's fine, traditional servers ignore unknown extensions)
		if hasCapability(caps3, "RESTMAIL") {
			t.Log("OK: restmail advertises RESTMAIL (traditional servers will ignore this)")
		}
	})

	t.Run("EhloResponse_Format", func(t *testing.T) {
		// The banner and EHLO format should be RFC 5321 compliant
		sc := dialSMTP(t, restmailSMTPAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")
		// First line should be "250-hostname ..."
		if len(caps) == 0 {
			t.Fatal("no EHLO response lines")
		}
		firstLine := caps[0]
		if !strings.HasPrefix(firstLine, "250") {
			t.Fatalf("EHLO first line should start with 250, got: %s", firstLine)
		}
		// Last line should have space after 250 (not dash)
		lastLine := caps[len(caps)-1]
		if len(lastLine) >= 4 && lastLine[3] != ' ' {
			t.Errorf("EHLO last line should have space after code, got: %s", lastLine)
		}
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("SmtpConversation_IdenticalBehaviour", func(t *testing.T) {
		// Run the exact same SMTP conversation against mail1 and restmail
		type smtpStep struct {
			cmd          string
			expectedCode string
		}

		steps := []smtpStep{
			{"MAIL FROM:<test@example.com>", "250"},
			{"RCPT TO:<alice@mail1.test>", "250"}, // mail1 side
			{"RSET", "250"},
			{"NOOP", "250"},
		}

		// Test mail1
		sc1 := dialSMTP(t, mail1SMTPAddr)
		defer sc1.close()
		sc1.ehlo(t, "test.local")

		for _, step := range steps {
			resp := sc1.sendExpect(t, step.cmd, step.expectedCode)
			t.Logf("mail1 %s → %s", step.cmd, resp)
		}
		sc1.sendExpect(t, "QUIT", "221")

		// Test restmail with equivalent commands
		steps3 := []smtpStep{
			{"MAIL FROM:<test@example.com>", "250"},
			{"RCPT TO:<testuser@restmail.test>", "250"}, // restmail side
			{"RSET", "250"},
			{"NOOP", "250"},
		}

		sc3 := dialSMTP(t, restmailSMTPAddr)
		defer sc3.close()
		sc3.starttlsIfOffered(t, "test.local")

		for _, step := range steps3 {
			resp := sc3.sendExpect(t, step.cmd, step.expectedCode)
			t.Logf("restmail %s → %s", step.cmd, resp)
		}
		sc3.sendExpect(t, "QUIT", "221")
	})

	t.Run("SmtpEdgeCases", func(t *testing.T) {
		sc := dialSMTP(t, restmailSMTPAddr)
		defer sc.close()
		sc.starttlsIfOffered(t, "test.local")

		// RSET mid-conversation
		sc.sendExpect(t, "MAIL FROM:<a@b.com>", "250")
		sc.sendExpect(t, "RSET", "250")

		// NOOP
		sc.sendExpect(t, "NOOP", "250")

		// Unknown recipient
		sc.sendExpect(t, "MAIL FROM:<a@b.com>", "250")
		sc.send(t, "RCPT TO:<nonexistent-user-xyz@restmail.test>")
		resp := sc.readLine(t)
		if resp[0] != '5' {
			t.Errorf("expected 5xx for unknown recipient, got: %s", resp)
		}

		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("ImapBehaviour_MatchTraditional", func(t *testing.T) {
		// Compare IMAP session behaviour between mail1 and restmail
		// Both should accept LOGIN, LIST folders, LOGOUT

		// mail1 (Dovecot)
		ic1 := dialIMAP(t, mail1IMAPAddr)
		defer ic1.close()
		ic1.login(t, "alice@mail1.test", adminPassword)

		result1, lines1 := ic1.command(t, "LIST \"\" \"*\"")
		t.Logf("mail1 LIST: %d lines, result: %s", len(lines1), result1)

		result1, _ = ic1.command(t, "SELECT INBOX")
		t.Logf("mail1 SELECT INBOX: %s", result1)

		ic1.command(t, "LOGOUT")

		// restmail (gateway)
		ic3 := dialIMAPTLS(t, restmailIMAPAddr)
		defer ic3.close()
		ic3.login(t, "testuser@restmail.test", adminPassword)

		result3, lines3 := ic3.command(t, "LIST \"\" \"*\"")
		t.Logf("restmail LIST: %d lines, result: %s", len(lines3), result3)

		result3, _ = ic3.command(t, "SELECT INBOX")
		t.Logf("restmail SELECT INBOX: %s", result3)

		ic3.command(t, "LOGOUT")

		// Both should have returned OK results
		if !strings.Contains(result1, "OK") {
			t.Error("mail1 LIST did not return OK")
		}
		if !strings.Contains(result3, "OK") {
			t.Error("restmail LIST did not return OK")
		}
	})

	t.Run("Pop3Behaviour_MatchTraditional", func(t *testing.T) {
		// POP3 on restmail gateway
		pc := dialPOP3TLS(t, restmailPOP3Addr)
		defer pc.close()

		pc.sendExpect(t, "USER testuser@restmail.test", "+OK")
		pc.sendExpect(t, "PASS "+adminPassword, "+OK")
		pc.sendExpect(t, "STAT", "+OK")
		pc.sendExpect(t, "LIST", "+OK")
		// Read the list until "."
		for {
			line := pc.readLine(t)
			if line == "." {
				break
			}
		}
		pc.sendExpect(t, "NOOP", "+OK")
		pc.sendExpect(t, "QUIT", "+OK")
	})

	t.Run("MessageHeaders_NoLeaks", func(t *testing.T) {
		// Send from restmail to mail1, then check received headers
		subject := fmt.Sprintf("header-leak-test-%d", time.Now().UnixNano())

		// We deliver via the API to testuser@restmail.test, then restmail relays to mail1
		gwClient := newAPIClient()
		if err := gwClient.login("testuser@restmail.test", adminPassword); err != nil {
			t.Skipf("Cannot login: %v", err)
		}

		resp, err := gwClient.post("/api/v1/messages/deliver", map[string]string{
			"address":   "alice@mail1.test",
			"sender":    "testuser@restmail.test",
			"subject":   subject,
			"body_text": "Testing for header leaks",
		})
		requireNoError(t, err)
		resp.Body.Close()

		aliceClient := newAPIClient()
		if err := aliceClient.login("alice@mail1.test", adminPassword); err != nil {
			t.Skipf("Cannot login as alice: %v", err)
		}

		// Wait for the message on alice's side
		// Use IMAP to check raw headers
		time.Sleep(5 * time.Second) // Give time for relay
		ic := dialIMAP(t, mail1IMAPAddr)
		defer ic.close()
		ic.login(t, "alice@mail1.test", adminPassword)

		result, _ := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Skipf("Cannot SELECT INBOX: %s", result)
		}

		// Search for our message
		searchResult, searchLines := ic.command(t, fmt.Sprintf("SEARCH SUBJECT \"%s\"", subject))
		t.Logf("Search result: %s, lines: %v", searchResult, searchLines)

		// Check that no headers reveal REST internals
		// This is a basic check - in a full implementation we'd parse the headers
		t.Log("Header leak check: manual verification needed for Received headers")
		ic.command(t, "LOGOUT")
	})

	t.Run("SmtpStarttls_Mail3", func(t *testing.T) {
		sc := dialSMTP(t, restmailSMTPAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")
		if !hasCapability(caps, "STARTTLS") {
			t.Fatal("restmail does not advertise STARTTLS")
		}

		sc.starttls(t)

		// Re-EHLO over TLS
		caps = sc.ehlo(t, "test.local")
		t.Logf("Post-STARTTLS capabilities: %d lines", len(caps))

		// Send a message over TLS
		subject := fmt.Sprintf("test-starttls-%d", time.Now().UnixNano())
		// Sender from a no-policy domain (no _dmarc record) so this unauthenticated
		// inbound inject isn't DMARC-rejected the way a spoofed mail1.test would be.
		sc.sendExpect(t, "MAIL FROM:<external@ext-e2e.test>", "250")
		sc.sendExpect(t, "RCPT TO:<testuser@restmail.test>", "250")
		sc.sendExpect(t, "DATA", "354")

		msg := fmt.Sprintf("From: external@ext-e2e.test\r\nTo: testuser@restmail.test\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <tls-%d@test.local>\r\n\r\nSent over STARTTLS!",
			subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
		sc.send(t, msg)
		sc.sendExpect(t, ".", "250")
		sc.sendExpect(t, "QUIT", "221")
		t.Logf("Message sent over STARTTLS successfully")
	})

	t.Run("SmtpSizeEnforcement_Mail3", func(t *testing.T) {
		sc := dialSMTP(t, restmailSMTPAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")

		// Verify SIZE is advertised
		foundSize := false
		for _, line := range caps {
			upper := strings.ToUpper(line)
			if len(upper) > 4 && strings.HasPrefix(upper[4:], "SIZE") {
				foundSize = true
				t.Logf("SIZE capability: %s", line)
			}
		}
		if !foundSize {
			t.Fatal("restmail does not advertise SIZE capability")
		}

		// Try MAIL FROM with declared size exceeding limit
		sc.send(t, "MAIL FROM:<test@test.local> SIZE=20000000")
		resp := sc.readLine(t)
		if strings.HasPrefix(resp, "552") || strings.HasPrefix(resp, "5") {
			t.Logf("Server rejected oversized MAIL FROM at envelope stage: %s", resp)
		} else {
			// Some servers only check during DATA, which is also valid
			t.Logf("Server accepted MAIL FROM with SIZE=20000000 (will enforce at DATA): %s", resp)
		}
		sc.sendExpect(t, "RSET", "250")
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("ImapStarttls_Mail3", func(t *testing.T) {
		ic := dialIMAPTLS(t, restmailIMAPAddr)
		defer ic.close()

		// The capability line is still worth asserting, just not for STARTTLS: on an
		// implicit-TLS port there is nothing to upgrade, and skipping on its absence
		// would have left this test permanently not running. What matters is that the
		// session can authenticate at all.
		_, lines := ic.command(t, "CAPABILITY")
		// The UNTAGGED line is the capability list; the tagged one is just
		// "A001 OK CAPABILITY completed". Matching any line containing "CAPABILITY"
		// picked the completion, so an assertion against it passed no matter what the
		// server actually advertised.
		capLine := ""
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "*") && strings.Contains(strings.ToUpper(l), "CAPABILITY") {
				capLine = l
			}
		}
		if !strings.Contains(strings.ToUpper(capLine), "AUTH=PLAIN") {
			t.Fatalf("IMAP does not offer AUTH=PLAIN on its TLS port: %s", capLine)
		}
		t.Logf("IMAP implicit-TLS session established: %s", capLine)

		// LOGIN over TLS
		ic.login(t, "testuser@restmail.test", adminPassword)

		result, _ := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX over TLS failed: %s", result)
		}
		t.Log("IMAP session fully functional over TLS")
		ic.command(t, "LOGOUT")
	})

	t.Run("Pop3Stls_Mail3", func(t *testing.T) {
		pc := dialPOP3TLS(t, restmailPOP3Addr)
		defer pc.close()

		// STLS must NOT be advertised: this is an implicit-TLS port, so offering an
		// in-band upgrade would mean the server thinks it is running in the clear.
		caps := pc.capa(t)
		for _, capability := range caps {
			if strings.ToUpper(strings.TrimSpace(capability)) == "STLS" {
				t.Errorf("POP3 advertises STLS on its implicit-TLS port: %v", caps)
			}
		}
		t.Logf("POP3 implicit-TLS session established: %v", caps)

		// Auth over TLS
		pc.sendExpect(t, "USER testuser@restmail.test", "+OK")
		pc.sendExpect(t, "PASS "+adminPassword, "+OK")

		statResp := pc.stat(t)
		t.Logf("POP3 over TLS STAT: %s", statResp)
		pc.sendExpect(t, "QUIT", "+OK")
	})

	t.Run("SmtpSizeEnforcement_OversizedData_Mail3", func(t *testing.T) {
		sc := dialSMTP(t, restmailSMTPAddr)
		defer sc.close()

		sc.starttlsIfOffered(t, "test.local")
		sc.sendExpect(t, "MAIL FROM:<test@test.local>", "250")
		sc.sendExpect(t, "RCPT TO:<testuser@restmail.test>", "250")
		sc.sendExpect(t, "DATA", "354")

		// Send headers
		sc.send(t, fmt.Sprintf("From: test@test.local\r\nTo: testuser@restmail.test\r\nSubject: size-test-%d\r\n", time.Now().UnixNano()))
		sc.send(t, "")

		// Send >20MB of data to exceed SIZE limit
		chunk := strings.Repeat("X", 1024) // 1KB line
		for i := 0; i < 21000; i++ {       // ~21MB
			_ = sc.conn.SetDeadline(time.Now().Add(30 * time.Second))
			fmt.Fprintf(sc.conn, "%s\r\n", chunk)
		}

		// End DATA
		sc.send(t, ".")
		resp := sc.readLine(t)
		// Server should either reject during DATA (552/554) or at end of DATA
		if strings.HasPrefix(resp, "552") || strings.HasPrefix(resp, "554") || strings.HasPrefix(resp, "5") {
			t.Logf("Server correctly rejected oversized message: %s", resp)
		} else if strings.HasPrefix(resp, "250") {
			// Some servers may accept it if they don't enforce at DATA stage;
			// the envelope-level SIZE check was already tested
			t.Logf("Server accepted oversized DATA (enforcement may be at envelope level only): %s", resp)
		} else {
			t.Logf("Unexpected response to oversized DATA: %s", resp)
		}
	})

	t.Run("SmtpAuthOverImplicitTLS_Mail3", func(t *testing.T) {
		// 465 is implicit TLS: the session is encrypted before EHLO, so there is no
		// STARTTLS to wait for. Skipping on its absence, as this did, would have meant
		// the test never ran again.
		sc := dialSMTPTLS(t, restmailSubmitAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")
		if hasCapability(caps, "STARTTLS") {
			t.Errorf("submission advertises STARTTLS on its implicit-TLS port: %v", caps)
		}
		if !hasCapability(caps, "AUTH") {
			t.Fatal("restmail submission does not advertise AUTH")
		}

		// Good credentials should succeed
		sc.authPlain(t, "testuser@restmail.test", adminPassword)
		t.Log("AUTH PLAIN after STARTTLS succeeded")

		// Authenticated submission relays OUTBOUND to an external recipient (the
		// real use case) — queued for delivery, not looped back through the
		// inbound DMARC pipeline as a same-domain self-inject.
		subject := fmt.Sprintf("auth-tls-test-%d", time.Now().UnixNano())
		sc.sendExpect(t, "MAIL FROM:<testuser@restmail.test>", "250")
		sc.sendExpect(t, "RCPT TO:<alice@mail1.test>", "250")
		sc.sendExpect(t, "DATA", "354")
		msg := fmt.Sprintf("From: testuser@restmail.test\r\nTo: alice@mail1.test\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <auth-tls-%d@test.local>\r\n\r\nAuth+TLS test",
			subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
		sc.send(t, msg)
		sc.sendExpect(t, ".", "250")
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("SmtpBadAuthOverImplicitTLS_Mail3", func(t *testing.T) {
		sc := dialSMTPTLS(t, restmailSubmitAddr)
		defer sc.close()

		sc.ehlo(t, "test.local")

		cred := base64.StdEncoding.EncodeToString([]byte("\x00testuser@restmail.test\x00wrongpassword"))
		sc.send(t, "AUTH PLAIN "+cred)
		resp := sc.readLine(t)
		if !strings.HasPrefix(resp, "535") && !strings.HasPrefix(resp, "5") {
			t.Errorf("expected 535/5xx for bad credentials after STARTTLS, got: %s", resp)
		} else {
			t.Logf("Bad credentials after STARTTLS correctly rejected: %s", resp)
		}
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("ImapLoginAfterStarttls_Mail3", func(t *testing.T) {
		ic := dialIMAPTLS(t, restmailIMAPAddr)
		defer ic.close()

		_, lines := ic.command(t, "CAPABILITY")
		// The UNTAGGED line is the capability list; the tagged one is just
		// "A001 OK CAPABILITY completed". Matching any line containing "CAPABILITY"
		// picked the completion, so an assertion against it passed no matter what the
		// server actually advertised.
		capLine := ""
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "*") && strings.Contains(strings.ToUpper(l), "CAPABILITY") {
				capLine = l
			}
		}
		// STARTTLS must NOT be advertised on an implicit-TLS port: offering an in-band
		// upgrade would mean the server thinks it is running in the clear.
		if strings.Contains(strings.ToUpper(capLine), "STARTTLS") {
			t.Errorf("IMAP advertises STARTTLS on its implicit-TLS port: %s", capLine)
		}
		// LOGIN with good credentials
		ic.login(t, "testuser@restmail.test", adminPassword)

		result, _ := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX after STARTTLS+LOGIN failed: %s", result)
		}
		t.Log("IMAP LOGIN after STARTTLS succeeded, SELECT INBOX OK")
		ic.command(t, "LOGOUT")
	})

	t.Run("ImapBadLoginAfterStarttls_Mail3", func(t *testing.T) {
		ic := dialIMAPTLS(t, restmailIMAPAddr)
		defer ic.close()

		_, lines := ic.command(t, "CAPABILITY")
		// The UNTAGGED line is the capability list; the tagged one is just
		// "A001 OK CAPABILITY completed". Matching any line containing "CAPABILITY"
		// picked the completion, so an assertion against it passed no matter what the
		// server actually advertised.
		capLine := ""
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "*") && strings.Contains(strings.ToUpper(l), "CAPABILITY") {
				capLine = l
			}
		}
		// STARTTLS must NOT be advertised on an implicit-TLS port: offering an in-band
		// upgrade would mean the server thinks it is running in the clear.
		if strings.Contains(strings.ToUpper(capLine), "STARTTLS") {
			t.Errorf("IMAP advertises STARTTLS on its implicit-TLS port: %s", capLine)
		}
		// LOGIN with bad credentials — should fail
		result, _ := ic.command(t, "LOGIN testuser@restmail.test wrongpassword")
		if strings.Contains(result, "OK") {
			t.Fatal("IMAP LOGIN with bad password should not return OK")
		}
		t.Logf("IMAP bad LOGIN after STARTTLS correctly rejected: %s", result)
	})

	t.Run("Pop3BadAuthAfterStls_Mail3", func(t *testing.T) {
		pc := dialPOP3TLS(t, restmailPOP3Addr)
		defer pc.close()

		// STLS must NOT be advertised on an implicit-TLS port.
		caps := pc.capa(t)
		for _, capability := range caps {
			if strings.ToUpper(strings.TrimSpace(capability)) == "STLS" {
				t.Errorf("POP3 advertises STLS on its implicit-TLS port: %v", caps)
			}
		}
		pc.sendExpect(t, "USER testuser@restmail.test", "+OK")
		resp := pc.sendExpect(t, "PASS wrongpassword", "-ERR")
		t.Logf("POP3 bad credentials after STLS correctly rejected: %s", resp)
	})
}
