package e2e

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

var (
	imapsGWAddr = envOr("IMAPS_GW_ADDR", "imap.restmail.test:993")
)

func testStage10Verification(t *testing.T) {
	client := newAPIClient()
	requireNoError(t, client.loginAdmin("admin", "admin123!@"))

	// Product-owned mailboxes only: mail1.test belongs to the mail1 reference
	// server (its users are seeded there — alice/bob), and creating it in the
	// product DB would make restmail stop relaying to it.
	createDomain(t, client, "restmail.test", "restmail")
	createMailbox(t, client, "verify-recv@restmail.test", "password123", "Verify Receiver")
	createMailbox(t, client, "verify-out@restmail.test", "password123", "Verify Outbound")
	createMailbox(t, client, "verify-rm1@restmail.test", "password123", "Verify RM1")
	createMailbox(t, client, "verify-rm2@restmail.test", "password123", "Verify RM2")
	ensureE2EPipeline(t, client)

	t.Run("Mail1_to_Mail3_Inbound", func(t *testing.T) {
		subject := fmt.Sprintf("E2E-inbound-%d", time.Now().UnixNano())

		// Relay from the mail1 reference server into restmail — with proper
		// RFC 5322 headers (restmail's header_validate rejects date-less mail),
		// via the greylist-aware helper.
		sendMailViaSMTP(t, mail1SMTPAddr,
			"alice@mail1.test", "verify-recv@restmail.test",
			subject, "Inbound test body")

		// Login as receiver and check via API
		recvClient := newAPIClient()
		requireNoError(t, recvClient.login("verify-recv@restmail.test", "password123"))

		// Link account
		_, _ = recvClient.post("/api/v1/accounts", map[string]string{
			"address": "verify-recv@restmail.test", "password": "password123",
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
			if a.Address == "verify-recv@restmail.test" {
				accountID = a.ID
				break
			}
		}
		if accountID == 0 {
			t.Fatal("could not find linked account for verify-recv@restmail.test")
		}

		msgID := waitForMessage(t, recvClient, accountID, "INBOX", subject, 30*time.Second)
		if msgID == 0 {
			t.Fatal("message not delivered")
		}
		t.Logf("Inbound message delivered: ID=%d", msgID)
	})

	t.Run("Mail3_to_Mail1_Outbound", func(t *testing.T) {
		subject := fmt.Sprintf("E2E-outbound-%d", time.Now().UnixNano())

		// Login as restmail sender
		sendClient := newAPIClient()
		requireNoError(t, sendClient.login("verify-out@restmail.test", "password123"))

		// Link account
		_, _ = sendClient.post("/api/v1/accounts", map[string]string{
			"address": "verify-out@restmail.test", "password": "password123",
		})

		// Send via API
		resp, err := sendClient.post("/api/v1/messages/send", map[string]any{
			"from":      "verify-out@restmail.test",
			"to":        []string{"alice@mail1.test"},
			"subject":   subject,
			"body_text": "Outbound test body",
		})
		requireNoError(t, err)
		requireSuccess(t, resp)

		// Verify actual arrival on the mail1 reference server over its IMAP —
		// this exercises the full outbound path: queue → MX → postfix → LMTP.
		waitForImapMessage(t, mail1IMAPAddr, "alice@mail1.test", adminPassword, subject, 60*time.Second)
		t.Log("Outbound message delivered to the reference server")
	})

	t.Run("Mail3_to_Mail3_RestmailUpgrade", func(t *testing.T) {
		subject := fmt.Sprintf("E2E-restmail-%d", time.Now().UnixNano())

		// Login as sender
		sendClient := newAPIClient()
		requireNoError(t, sendClient.login("verify-rm1@restmail.test", "password123"))
		_, _ = sendClient.post("/api/v1/accounts", map[string]string{
			"address": "verify-rm1@restmail.test", "password": "password123",
		})

		// Send to another restmail user
		resp, err := sendClient.post("/api/v1/messages/send", map[string]any{
			"from":      "verify-rm1@restmail.test",
			"to":        []string{"verify-rm2@restmail.test"},
			"subject":   subject,
			"body_text": "Restmail fast delivery test",
		})
		requireNoError(t, err)
		requireSuccess(t, resp)

		// Login as receiver and verify fast delivery
		recvClient := newAPIClient()
		requireNoError(t, recvClient.login("verify-rm2@restmail.test", "password123"))
		_, _ = recvClient.post("/api/v1/accounts", map[string]string{
			"address": "verify-rm2@restmail.test", "password": "password123",
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
			if a.Address == "verify-rm2@restmail.test" {
				accountID = a.ID
				break
			}
		}
		if accountID == 0 {
			t.Fatal("could not find linked account for verify-rm2@restmail.test")
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
		defer func() { _ = tlsConn.Close() }()

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
		defer func() { _ = conn.Close() }()

		// A single Read grabs only the first packet; IMAP tagged responses
		// (esp. SELECT's untagged * lines + the tagged OK) arrive across
		// several — read until the tag.
		reader := bufio.NewReader(conn)
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		greeting, _ := reader.ReadString('\n')
		if !strings.Contains(greeting, "OK") {
			t.Fatalf("unexpected IMAP greeting: %s", greeting)
		}

		// LOGIN
		fmt.Fprintf(conn, "A001 LOGIN verify-recv@restmail.test password123\r\n")
		if loginResp := readUntilTagRaw(t, reader, "A001"); !strings.Contains(loginResp, "A001 OK") {
			t.Skipf("IMAP LOGIN failed (may need setup): %s", loginResp)
		}

		// SELECT INBOX
		fmt.Fprintf(conn, "A002 SELECT INBOX\r\n")
		if selectResp := readUntilTagRaw(t, reader, "A002"); !strings.Contains(selectResp, "A002 OK") {
			t.Fatalf("SELECT INBOX failed: %s", selectResp)
		}

		t.Log("IMAPS connection, LOGIN, SELECT INBOX all succeeded")

		// LOGOUT
		fmt.Fprintf(conn, "A003 LOGOUT\r\n")
	})
}
