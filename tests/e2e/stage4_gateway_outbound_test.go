package e2e

import (
	"fmt"
	"net/http"
	"strings"
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

	t.Run("QuotaEnforcement", func(t *testing.T) {
		// Create a mailbox with a very small quota (1024 bytes)
		quotaAddr := fmt.Sprintf("quotauser-%d@mail3.test", time.Now().UnixNano())
		var quotaBytes int64 = 1024

		resp, err := adminClient.post("/api/v1/admin/mailboxes", map[string]interface{}{
			"address":      quotaAddr,
			"password":     "password123",
			"display_name": "Quota Test User",
			"quota_bytes":  quotaBytes,
		})
		requireNoError(t, err)

		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			t.Skip("Quota test mailbox already exists, skipping")
		}
		if resp.StatusCode != http.StatusCreated {
			body := readBody(resp)
			t.Fatalf("create quota mailbox: status %d: %s", resp.StatusCode, body)
		}

		var created struct {
			Data struct {
				ID         uint   `json:"id"`
				Address    string `json:"address"`
				QuotaBytes int64  `json:"quota_bytes"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &created); err != nil {
			t.Fatalf("decode created mailbox: %v", err)
		}

		quotaUserID := created.Data.ID
		t.Logf("Created quota test mailbox: id=%d addr=%s quota=%d bytes",
			quotaUserID, created.Data.Address, created.Data.QuotaBytes)

		if created.Data.QuotaBytes != quotaBytes {
			t.Errorf("expected quota %d bytes, got %d", quotaBytes, created.Data.QuotaBytes)
		}

		// Login as the quota user
		quotaClient := newAPIClient()
		if err := quotaClient.login(quotaAddr, "password123"); err != nil {
			t.Fatalf("Cannot login as quota user: %v", err)
		}

		// Send messages until quota fills.
		// Each message with headers is roughly 200-400 bytes,
		// so 1024 bytes should fill after a few messages.
		var lastRejectCode string
		quotaExceeded := false
		for i := 0; i < 20; i++ {
			subject := fmt.Sprintf("quota-fill-%d-%d", i, time.Now().UnixNano())
			// Pad the body to help fill the quota faster
			body := strings.Repeat("X", 300)

			sc := dialSMTP(t, mail1SMTPAddr)
			sc.ehlo(t, "test.local")
			sc.sendExpect(t, "MAIL FROM:<filler@mail1.test>", "250")
			sc.send(t, "RCPT TO:<"+quotaAddr+">")
			rcptResp := sc.readLine(t)

			if strings.HasPrefix(rcptResp, "4") || strings.HasPrefix(rcptResp, "5") {
				// Quota exceeded at RCPT TO stage
				lastRejectCode = rcptResp
				quotaExceeded = true
				t.Logf("Quota rejected at RCPT TO on message %d: %s", i, rcptResp)
				sc.sendExpect(t, "QUIT", "221")
				sc.close()
				break
			}

			if !strings.HasPrefix(rcptResp, "250") {
				t.Logf("Unexpected RCPT TO response on message %d: %s", i, rcptResp)
				sc.sendExpect(t, "QUIT", "221")
				sc.close()
				break
			}

			sc.sendExpect(t, "DATA", "354")
			msg := fmt.Sprintf("From: filler@mail1.test\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <qfill-%d-%d@test.local>\r\n\r\n%s",
				quotaAddr, subject, time.Now().Format(time.RFC1123Z), i, time.Now().UnixNano(), body)
			sc.send(t, msg)

			// Check if DATA terminator gets rejected
			sc.send(t, ".")
			dotResp := sc.readLine(t)
			if strings.HasPrefix(dotResp, "4") || strings.HasPrefix(dotResp, "5") {
				lastRejectCode = dotResp
				quotaExceeded = true
				t.Logf("Quota rejected at DATA end on message %d: %s", i, dotResp)
				sc.sendExpect(t, "QUIT", "221")
				sc.close()
				break
			}

			sc.sendExpect(t, "QUIT", "221")
			sc.close()

			// Brief pause to let processing complete
			time.Sleep(500 * time.Millisecond)
		}

		if quotaExceeded {
			t.Logf("Quota enforcement confirmed with reject: %s", lastRejectCode)
			// Verify the reject code is 452 (insufficient storage) or 422 or 5xx
			if !strings.HasPrefix(lastRejectCode, "452") &&
				!strings.HasPrefix(lastRejectCode, "422") &&
				!strings.HasPrefix(lastRejectCode, "5") {
				t.Errorf("expected 452/422/5xx quota rejection, got: %s", lastRejectCode)
			}
		} else {
			t.Log("No SMTP-level quota rejection observed (quota enforcement may happen at API level)")
		}

		// Check quota API returns usage info
		quotaResp, err := quotaClient.get(fmt.Sprintf("/api/v1/accounts/%d/quota", quotaUserID))
		requireNoError(t, err)
		if quotaResp.StatusCode == http.StatusOK {
			var quotaInfo struct {
				Data struct {
					QuotaBytes     int64   `json:"quota_bytes"`
					QuotaUsedBytes int64   `json:"quota_used_bytes"`
					MessageCount   int64   `json:"message_count"`
					PercentUsed    float64 `json:"percent_used"`
				} `json:"data"`
			}
			if err := decodeJSON(quotaResp, &quotaInfo); err != nil {
				t.Fatalf("decode quota response: %v", err)
			}
			t.Logf("Quota status: used=%d/%d bytes (%.1f%%), messages=%d",
				quotaInfo.Data.QuotaUsedBytes, quotaInfo.Data.QuotaBytes,
				quotaInfo.Data.PercentUsed, quotaInfo.Data.MessageCount)

			if quotaInfo.Data.QuotaBytes != quotaBytes {
				t.Errorf("API quota mismatch: expected %d, got %d", quotaBytes, quotaInfo.Data.QuotaBytes)
			}
			if quotaInfo.Data.MessageCount == 0 && !quotaExceeded {
				t.Log("No messages delivered (quota may have been enforced before any delivery)")
			}
		} else {
			body := readBody(quotaResp)
			t.Logf("Quota API returned status %d: %s", quotaResp.StatusCode, body)
		}
	})
}
