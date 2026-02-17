package e2e

import (
	"fmt"
	"testing"
	"time"
)

func testStage4GatewayOutbound(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.login("admin@mail1.test", adminPassword); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	// Ensure users exist
	createMailbox(t, adminClient, "testuser@mail3.test", adminPassword, "GW Test User")
	alice := createMailbox(t, adminClient, "alice@mail1.test", adminPassword, "Alice")
	bob := createMailbox(t, adminClient, "bob@mail2.test", adminPassword, "Bob")

	gwClient := newAPIClient()
	if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
		t.Skipf("Cannot login as testuser@mail3.test: %v", err)
	}

	t.Run("Mail3_to_Mail1_SmtpRelay", func(t *testing.T) {
		subject := fmt.Sprintf("test-m3-to-m1-%d", time.Now().UnixNano())

		// Send via API (which enqueues in outbound queue, gateway relays via SMTP)
		resp, err := gwClient.post("/api/v1/messages/deliver", map[string]string{
			"address":   "alice@mail1.test",
			"sender":    "testuser@mail3.test",
			"subject":   subject,
			"body_text": "Hello Alice from mail3 via gateway relay!",
		})
		requireNoError(t, err)
		t.Logf("Enqueue response: %d", resp.StatusCode)
		resp.Body.Close()

		// Wait for delivery on alice's end
		aliceClient := newAPIClient()
		if err := aliceClient.login("alice@mail1.test", adminPassword); err != nil {
			t.Fatalf("Cannot login as alice: %v", err)
		}

		msgID := waitForMessage(t, aliceClient, alice.ID, "INBOX", subject, 60*time.Second)
		t.Logf("Mail3 → Mail1 relay delivered: id=%d", msgID)
	})

	t.Run("Mail3_to_Mail2_SmtpRelay", func(t *testing.T) {
		subject := fmt.Sprintf("test-m3-to-m2-%d", time.Now().UnixNano())

		resp, err := gwClient.post("/api/v1/messages/deliver", map[string]string{
			"address":   "bob@mail2.test",
			"sender":    "testuser@mail3.test",
			"subject":   subject,
			"body_text": "Hello Bob from mail3 via gateway relay!",
		})
		requireNoError(t, err)
		resp.Body.Close()

		bobClient := newAPIClient()
		if err := bobClient.login("bob@mail2.test", adminPassword); err != nil {
			t.Fatalf("Cannot login as bob: %v", err)
		}

		msgID := waitForMessage(t, bobClient, bob.ID, "INBOX", subject, 60*time.Second)
		t.Logf("Mail3 → Mail2 relay delivered: id=%d", msgID)
	})

	t.Run("Mail3_OutboundFallback", func(t *testing.T) {
		// Verify that EHLO from mail1 does NOT show RESTMAIL capability
		sc := dialSMTP(t, mail1SMTPAddr)
		defer sc.close()

		caps := sc.ehlo(t, "test.local")
		if hasCapability(caps, "RESTMAIL") {
			t.Error("mail1 should NOT advertise RESTMAIL capability")
		} else {
			t.Log("Confirmed: mail1 does not advertise RESTMAIL (standard SMTP only)")
		}
		sc.sendExpect(t, "QUIT", "221")
	})
}
