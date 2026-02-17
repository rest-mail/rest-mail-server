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
	if err := adminClient.login("admin@mail1.test", adminPassword); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	// Ensure mail3 users exist
	createMailbox(t, adminClient, "testuser@mail3.test", adminPassword, "GW Test User")
	other := createMailbox(t, adminClient, "other@mail3.test", adminPassword, "Other User")

	t.Run("Mail3_EhloAdvertisesRestmail", func(t *testing.T) {
		sc := dialSMTP(t, mail3SMTPAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")
		if !hasCapability(caps, "RESTMAIL") {
			t.Error("mail3 should advertise RESTMAIL capability in EHLO")
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
		resp, err := httpClient.Get(apiBaseURL + "/restmail/mailboxes?address=testuser@mail3.test")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		body := readBody(resp)
		t.Logf("Mailbox check: %s", body)
	})

	t.Run("RestmailEndpoint_DirectDelivery", func(t *testing.T) {
		subject := fmt.Sprintf("restmail-direct-%d", time.Now().UnixNano())

		resp, err := httpClient.Post(apiBaseURL+"/restmail/messages",
			"application/json",
			strings.NewReader(fmt.Sprintf(`{
				"address": "other@mail3.test",
				"sender": "testuser@mail3.test",
				"subject": %q,
				"body_text": "Direct REST delivery test"
			}`, subject)))
		requireNoError(t, err)

		if resp.StatusCode >= 400 {
			body := readBody(resp)
			t.Fatalf("RESTMAIL delivery failed (%d): %s", resp.StatusCode, body)
		}
		resp.Body.Close()

		// Verify delivery
		otherClient := newAPIClient()
		if err := otherClient.login("other@mail3.test", adminPassword); err != nil {
			t.Fatalf("Cannot login as other: %v", err)
		}

		msgID := waitForMessage(t, otherClient, other.ID, "INBOX", subject, 15*time.Second)
		t.Logf("RESTMAIL direct delivery verified: id=%d", msgID)
	})

	t.Run("Mail3_to_Mail3_UpgradePath", func(t *testing.T) {
		subject := fmt.Sprintf("restmail-upgrade-%d", time.Now().UnixNano())

		// Deliver from testuser to other, both on mail3
		gwClient := newAPIClient()
		if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
			t.Skipf("Cannot login: %v", err)
		}

		resp, err := gwClient.post("/api/v1/messages/deliver", map[string]string{
			"address":   "other@mail3.test",
			"sender":    "testuser@mail3.test",
			"subject":   subject,
			"body_text": "mail3 to mail3 upgrade test",
		})
		requireNoError(t, err)
		resp.Body.Close()

		otherClient := newAPIClient()
		if err := otherClient.login("other@mail3.test", adminPassword); err != nil {
			t.Fatalf("Cannot login as other: %v", err)
		}

		msgID := waitForMessage(t, otherClient, other.ID, "INBOX", subject, 15*time.Second)
		t.Logf("Mail3→Mail3 delivery (upgrade path): id=%d", msgID)
	})

	t.Run("TraditionalServer_IgnoresRestmailCap", func(t *testing.T) {
		// Verify mail1 can still deliver to mail3 normally despite RESTMAIL cap
		subject := fmt.Sprintf("trad-ignores-restmail-%d", time.Now().UnixNano())

		sendMailViaSMTP(t, mail1SMTPAddr,
			"alice@mail1.test", "testuser@mail3.test",
			subject, "mail1 sends to mail3, ignoring RESTMAIL extension")

		gwClient := newAPIClient()
		if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
			t.Fatalf("Cannot login: %v", err)
		}

		testUser := getMailboxByAddress(t, adminClient, "testuser@mail3.test")
		msgID := waitForMessage(t, gwClient, testUser.ID, "INBOX", subject, 30*time.Second)
		t.Logf("Traditional server delivered to mail3 normally: id=%d", msgID)
	})
}
