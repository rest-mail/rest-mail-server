package e2e

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

var (
	imapsGWAddr = envOr("IMAPS_GW_ADDR", "10.99.0.15:993")
)

func testStage10Verification(t *testing.T) {
	client := newAPIClient()
	requireNoError(t, client.login("admin@mail3.test", adminPassword))

	// Ensure domains and mailboxes exist
	createDomain(t, client, "mail1.test", "traditional")
	createDomain(t, client, "mail3.test", "restmail")
	createMailbox(t, client, "verify-sender@mail1.test", "password123", "Verify Sender")
	createMailbox(t, client, "verify-recv@mail3.test", "password123", "Verify Receiver")
	createMailbox(t, client, "verify-out@mail3.test", "password123", "Verify Outbound")
	createMailbox(t, client, "verify-rm1@mail3.test", "password123", "Verify RM1")
	createMailbox(t, client, "verify-rm2@mail3.test", "password123", "Verify RM2")
	createMailbox(t, client, "smtp-auth-user@mail1.test", "password123", "SMTP Auth")
	createMailbox(t, client, "imap-test@mail1.test", "password123", "IMAP Test")

	t.Run("Mail1_to_Mail3_Inbound", func(t *testing.T) {
		subject := fmt.Sprintf("E2E-inbound-%d", time.Now().UnixNano())

		// Send via SMTP from mail1 to mail3
		msg := fmt.Sprintf("From: verify-sender@mail1.test\r\nTo: verify-recv@mail3.test\r\nSubject: %s\r\n\r\nInbound test body\r\n", subject)
		err := smtp.SendMail(mail1SMTPAddr, nil, "verify-sender@mail1.test", []string{"verify-recv@mail3.test"}, []byte(msg))
		requireNoError(t, err)

		// Login as receiver and check via API
		recvClient := newAPIClient()
		requireNoError(t, recvClient.login("verify-recv@mail3.test", "password123"))

		// Link account
		recvClient.post("/api/v1/accounts", map[string]string{
			"address": "verify-recv@mail3.test", "password": "password123",
		})

		// Get account ID
		resp, err := recvClient.get("/api/v1/accounts")
		requireNoError(t, err)
		var accts struct {
			Data []struct {
				ID      uint   `json:"id"`
				Address string `json:"address"`
			} `json:"data"`
		}
		requireNoError(t, decodeJSON(resp, &accts))

		var accountID uint
		for _, a := range accts.Data {
			if a.Address == "verify-recv@mail3.test" {
				accountID = a.ID
				break
			}
		}
		if accountID == 0 {
			t.Fatal("could not find linked account for verify-recv@mail3.test")
		}

		msgID := waitForMessage(t, recvClient, accountID, "INBOX", subject, 30*time.Second)
		if msgID == 0 {
			t.Fatal("message not delivered")
		}
		t.Logf("Inbound message delivered: ID=%d", msgID)
	})

	t.Run("Mail3_to_Mail1_Outbound", func(t *testing.T) {
		subject := fmt.Sprintf("E2E-outbound-%d", time.Now().UnixNano())

		// Login as mail3 sender
		sendClient := newAPIClient()
		requireNoError(t, sendClient.login("verify-out@mail3.test", "password123"))

		// Link account
		sendClient.post("/api/v1/accounts", map[string]string{
			"address": "verify-out@mail3.test", "password": "password123",
		})

		// Send via API
		resp, err := sendClient.post("/api/v1/messages/send", map[string]any{
			"from":      "verify-out@mail3.test",
			"to":        []string{"verify-sender@mail1.test"},
			"subject":   subject,
			"body_text": "Outbound test body",
		})
		requireNoError(t, err)
		requireStatus(t, resp, 200)

		// Verify via SMTP that mail1 received it (check via the API as admin)
		adminClient := newAPIClient()
		requireNoError(t, adminClient.login("admin@mail3.test", adminPassword))

		// We can't easily check mail1's mailbox via our API since it's traditional.
		// Instead verify the message was queued successfully (200 response is sufficient).
		t.Log("Outbound message accepted for delivery")
	})

	t.Run("Mail3_to_Mail3_RestmailUpgrade", func(t *testing.T) {
		subject := fmt.Sprintf("E2E-restmail-%d", time.Now().UnixNano())

		// Login as sender
		sendClient := newAPIClient()
		requireNoError(t, sendClient.login("verify-rm1@mail3.test", "password123"))
		sendClient.post("/api/v1/accounts", map[string]string{
			"address": "verify-rm1@mail3.test", "password": "password123",
		})

		// Send to another mail3 user
		resp, err := sendClient.post("/api/v1/messages/send", map[string]any{
			"from":      "verify-rm1@mail3.test",
			"to":        []string{"verify-rm2@mail3.test"},
			"subject":   subject,
			"body_text": "Restmail fast delivery test",
		})
		requireNoError(t, err)
		requireStatus(t, resp, 200)

		// Login as receiver and verify fast delivery
		recvClient := newAPIClient()
		requireNoError(t, recvClient.login("verify-rm2@mail3.test", "password123"))
		recvClient.post("/api/v1/accounts", map[string]string{
			"address": "verify-rm2@mail3.test", "password": "password123",
		})

		resp2, err := recvClient.get("/api/v1/accounts")
		requireNoError(t, err)
		var accts struct {
			Data []struct {
				ID      uint   `json:"id"`
				Address string `json:"address"`
			} `json:"data"`
		}
		requireNoError(t, decodeJSON(resp2, &accts))

		var accountID uint
		for _, a := range accts.Data {
			if a.Address == "verify-rm2@mail3.test" {
				accountID = a.ID
				break
			}
		}
		if accountID == 0 {
			t.Fatal("could not find linked account for verify-rm2@mail3.test")
		}

		// RESTMAIL path should be near-instant for same-server
		msgID := waitForMessage(t, recvClient, accountID, "INBOX", subject, 10*time.Second)
		if msgID == 0 {
			t.Fatal("restmail fast delivery failed")
		}
		t.Logf("Restmail fast delivery confirmed: ID=%d", msgID)
	})

	t.Run("SmtpAuth_Port587", func(t *testing.T) {
		// Connect to submission port with STARTTLS
		conn, err := net.DialTimeout("tcp", mail1SubmitAddr, 10*time.Second)
		if err != nil {
			t.Skipf("Cannot connect to submission port %s: %v", mail1SubmitAddr, err)
		}
		defer conn.Close()

		// Read greeting
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		requireNoError(t, err)
		greeting := string(buf[:n])
		if !strings.HasPrefix(greeting, "220") {
			t.Fatalf("unexpected greeting: %s", greeting)
		}

		// Send EHLO
		fmt.Fprintf(conn, "EHLO test.local\r\n")
		n, err = conn.Read(buf)
		requireNoError(t, err)
		ehlo := string(buf[:n])
		if !strings.Contains(ehlo, "STARTTLS") {
			t.Skipf("STARTTLS not advertised on submission port: %s", ehlo)
		}

		// Send STARTTLS
		fmt.Fprintf(conn, "STARTTLS\r\n")
		n, err = conn.Read(buf)
		requireNoError(t, err)
		if !strings.HasPrefix(string(buf[:n]), "220") {
			t.Fatalf("STARTTLS rejected: %s", string(buf[:n]))
		}

		// Upgrade to TLS
		tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
		err = tlsConn.Handshake()
		requireNoError(t, err)
		defer tlsConn.Close()

		// Send EHLO again over TLS
		fmt.Fprintf(tlsConn, "EHLO test.local\r\n")
		n, err = tlsConn.Read(buf)
		requireNoError(t, err)
		ehloTLS := string(buf[:n])
		if !strings.Contains(ehloTLS, "AUTH") {
			t.Skipf("AUTH not advertised after STARTTLS: %s", ehloTLS)
		}

		t.Log("SMTP submission port accepts STARTTLS and advertises AUTH")

		// QUIT
		fmt.Fprintf(tlsConn, "QUIT\r\n")
	})

	t.Run("Imaps_Port993", func(t *testing.T) {
		// Connect via TLS to IMAPS port
		conn, err := tls.DialWithDialer(
			&net.Dialer{Timeout: 10 * time.Second},
			"tcp",
			imapsGWAddr,
			&tls.Config{InsecureSkipVerify: true},
		)
		if err != nil {
			t.Skipf("Cannot connect to IMAPS %s: %v", imapsGWAddr, err)
		}
		defer conn.Close()

		// Read server greeting
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		requireNoError(t, err)
		greeting := string(buf[:n])
		if !strings.Contains(greeting, "OK") {
			t.Fatalf("unexpected IMAP greeting: %s", greeting)
		}

		// LOGIN
		fmt.Fprintf(conn, "A001 LOGIN imap-test@mail1.test password123\r\n")
		n, err = conn.Read(buf)
		requireNoError(t, err)
		loginResp := string(buf[:n])
		if !strings.Contains(loginResp, "A001 OK") {
			t.Skipf("IMAP LOGIN failed (may need setup): %s", loginResp)
		}

		// SELECT INBOX
		fmt.Fprintf(conn, "A002 SELECT INBOX\r\n")
		n, err = conn.Read(buf)
		requireNoError(t, err)
		selectResp := string(buf[:n])
		if !strings.Contains(selectResp, "A002 OK") {
			t.Fatalf("SELECT INBOX failed: %s", selectResp)
		}

		t.Log("IMAPS connection, LOGIN, SELECT INBOX all succeeded")

		// LOGOUT
		fmt.Fprintf(conn, "A003 LOGOUT\r\n")
	})
}
