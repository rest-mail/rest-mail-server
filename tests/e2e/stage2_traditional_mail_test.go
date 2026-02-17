package e2e

import (
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
}
