package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testStage6RestmailUpgrade(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.loginAdmin("admin", "admin123!@"); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	// Ensure restmail users exist
	createMailbox(t, adminClient, "testuser@restmail.test", adminPassword, "GW Test User")
	createMailbox(t, adminClient, "other@restmail.test", adminPassword, "Other User")

	t.Run("Mail3_EhloAdvertisesRestmail", func(t *testing.T) {
		sc := dialSMTP(t, restmailSMTPAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")
		if !hasCapability(caps, "RESTMAIL") {
			t.Error("restmail should advertise RESTMAIL capability in EHLO")
		} else {
			// Find the RESTMAIL line
			for _, line := range caps {
				if strings.Contains(strings.ToUpper(line), "RESTMAIL") {
					t.Logf("RESTMAIL capability: %s", line)
				}
			}
		}
		sc.sendExpect(t, "QUIT", "221")
	})

	t.Run("RestmailEndpoint_Capabilities", func(t *testing.T) {
		resp, err := httpClient.Get(apiBaseURL + "/restmail/capabilities")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode capabilities: %v", err)
		}
		resp.Body.Close()
		t.Logf("RESTMAIL capabilities: %v", result)
	})

	t.Run("RestmailEndpoint_CheckMailbox", func(t *testing.T) {
		resp, err := httpClient.Get(apiBaseURL + "/restmail/mailboxes?address=testuser@restmail.test")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		body := readBody(resp)
		t.Logf("Mailbox check: %s", body)
	})

	t.Run("RestmailEndpoint_DirectDelivery", func(t *testing.T) {
		subject := fmt.Sprintf("restmail-direct-%d", time.Now().UnixNano())

		// The RESTMAIL direct-delivery endpoint takes from + to[] (the shape the
		// RESTMAIL queue worker posts) plus the raw message a real peer would
		// send — which must carry a Date (the inbound header_validate rejects
		// date-less mail). Sender is an unconfigured .test domain so DMARC
		// returns "none" rather than an internet-forwarded p=reject.
		raw := fmt.Sprintf("From: peer@ext-e2e.test\r\nTo: other@restmail.test\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <rm-direct-%d@ext-e2e.test>\r\n\r\nDirect REST delivery test",
			subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
		reqBody, _ := json.Marshal(map[string]any{
			"from":        "peer@ext-e2e.test",
			"to":          []string{"other@restmail.test"},
			"subject":     subject,
			"body_text":   "Direct REST delivery test",
			"raw_message": raw,
		})
		resp, err := httpClient.Post(apiBaseURL+"/restmail/messages",
			"application/json", strings.NewReader(string(reqBody)))
		requireNoError(t, err)

		if resp.StatusCode >= 400 {
			body := readBody(resp)
			t.Fatalf("RESTMAIL delivery failed (%d): %s", resp.StatusCode, body)
		}
		resp.Body.Close()

		// Verify delivery
		otherClient, otherClientAcct := restmailInbox(t, "other@restmail.test", adminPassword)
		msgID := waitForMessage(t, otherClient, otherClientAcct, "INBOX", subject, 15*time.Second)
		t.Logf("RESTMAIL direct delivery verified: id=%d", msgID)
	})

	t.Run("Mail3_to_Mail3_UpgradePath", func(t *testing.T) {
		subject := fmt.Sprintf("restmail-upgrade-%d", time.Now().UnixNano())

		// Send testuser -> other (both restmail) via the outbound compose path.
		gwClient := newAPIClient()
		if err := gwClient.login("testuser@restmail.test", adminPassword); err != nil {
			t.Skipf("Cannot login: %v", err)
		}

		resp, err := gwClient.post("/api/v1/messages/send", map[string]any{
			"from":      "testuser@restmail.test",
			"to":        []string{"other@restmail.test"},
			"subject":   subject,
			"body_text": "restmail to restmail upgrade test",
		})
		requireNoError(t, err)
		requireSuccess(t, resp)
		resp.Body.Close()

		otherClient, otherClientAcct := restmailInbox(t, "other@restmail.test", adminPassword)
		msgID := waitForMessage(t, otherClient, otherClientAcct, "INBOX", subject, 15*time.Second)
		t.Logf("Mail3→Mail3 delivery (upgrade path): id=%d", msgID)
	})

	t.Run("TraditionalServer_IgnoresRestmailCap", func(t *testing.T) {
		// Verify mail1 can still deliver to restmail normally despite RESTMAIL cap
		subject := fmt.Sprintf("trad-ignores-restmail-%d", time.Now().UnixNano())

		sendMailViaSMTP(t, mail1SMTPAddr,
			"alice@mail1.test", "testuser@restmail.test",
			subject, "mail1 sends to restmail, ignoring RESTMAIL extension")

		gwClient, gwAcct := restmailInbox(t, "testuser@restmail.test", adminPassword)
		msgID := waitForMessage(t, gwClient, gwAcct, "INBOX", subject, 30*time.Second)
		t.Logf("Traditional server delivered to restmail normally: id=%d", msgID)
	})
}
