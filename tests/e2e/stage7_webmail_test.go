package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func testStage7WebmailFlows(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.login("admin@mail1.test", adminPassword); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	alice := createMailbox(t, adminClient, "alice@mail1.test", adminPassword, "Alice")

	t.Run("WebmailLogin", func(t *testing.T) {
		client := newAPIClient()
		err := client.login("alice@mail1.test", adminPassword)
		requireNoError(t, err)
		if client.token == "" {
			t.Fatal("no token after login")
		}
		t.Log("Webmail login successful")
	})

	t.Run("WebmailListFolders", func(t *testing.T) {
		client := newAPIClient()
		requireNoError(t, client.login("alice@mail1.test", adminPassword))

		resp, err := client.get(fmt.Sprintf("/api/v1/accounts/%d/folders", alice.ID))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var result struct {
			Data []struct {
				Name   string `json:"name"`
				Total  int    `json:"total"`
				Unread int    `json:"unread"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &result); err != nil {
			t.Fatalf("decode folders: %v", err)
		}
		t.Logf("Folders: %+v", result.Data)

		// INBOX should exist
		found := false
		for _, f := range result.Data {
			if f.Name == "INBOX" {
				found = true
				t.Logf("INBOX: %d total, %d unread", f.Total, f.Unread)
			}
		}
		if !found {
			t.Error("INBOX folder not found")
		}
	})

	t.Run("WebmailReadMessage", func(t *testing.T) {
		client := newAPIClient()
		requireNoError(t, client.login("alice@mail1.test", adminPassword))

		resp, err := client.get(fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages?limit=10", alice.ID))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var result struct {
			Data []struct {
				ID      uint   `json:"id"`
				Subject string `json:"subject"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &result); err != nil {
			t.Fatalf("decode messages: %v", err)
		}

		if len(result.Data) == 0 {
			t.Skip("no messages in inbox to read")
		}

		msgID := result.Data[0].ID
		detailResp, err := client.get(fmt.Sprintf("/api/v1/messages/%d", msgID))
		requireNoError(t, err)
		requireStatus(t, detailResp, http.StatusOK)

		var detail struct {
			Data struct {
				Subject  string `json:"subject"`
				BodyText string `json:"body_text"`
			} `json:"data"`
		}
		if err := decodeJSON(detailResp, &detail); err != nil {
			t.Fatalf("decode message detail: %v", err)
		}
		t.Logf("Read message: subject=%q, bodyLen=%d", detail.Data.Subject, len(detail.Data.BodyText))
	})

	t.Run("WebmailComposeAndSend", func(t *testing.T) {
		bob := createMailbox(t, adminClient, "bob@mail2.test", adminPassword, "Bob")
		subject := fmt.Sprintf("webmail-compose-%d", time.Now().UnixNano())

		client := newAPIClient()
		requireNoError(t, client.login("alice@mail1.test", adminPassword))

		// Send via deliver endpoint
		resp, err := client.post("/api/v1/messages/deliver", map[string]string{
			"address":   "bob@mail2.test",
			"sender":    "alice@mail1.test",
			"subject":   subject,
			"body_text": "Composed from webmail!",
		})
		requireNoError(t, err)
		resp.Body.Close()

		// Verify delivery
		bobClient := newAPIClient()
		requireNoError(t, bobClient.login("bob@mail2.test", adminPassword))

		msgID := waitForMessage(t, bobClient, bob.ID, "INBOX", subject, 30*time.Second)
		t.Logf("Webmail compose delivered: id=%d", msgID)
	})

	t.Run("WebmailMarkAsRead", func(t *testing.T) {
		client := newAPIClient()
		requireNoError(t, client.login("alice@mail1.test", adminPassword))

		// Get first unread message
		resp, err := client.get(fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages?limit=10", alice.ID))
		requireNoError(t, err)

		var result struct {
			Data []struct {
				ID     uint `json:"id"`
				IsRead bool `json:"is_read"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &result); err != nil {
			t.Fatalf("decode messages: %v", err)
		}

		if len(result.Data) == 0 {
			t.Skip("no messages to mark as read")
		}

		msgID := result.Data[0].ID
		patchResp, err := client.patch(fmt.Sprintf("/api/v1/messages/%d", msgID),
			map[string]interface{}{"is_read": true})
		requireNoError(t, err)
		if patchResp.StatusCode >= 400 {
			body := readBody(patchResp)
			t.Fatalf("mark as read failed (%d): %s", patchResp.StatusCode, body)
		}
		patchResp.Body.Close()
		t.Logf("Marked message %d as read", msgID)
	})

	t.Run("WebmailDeleteMessage", func(t *testing.T) {
		client := newAPIClient()
		requireNoError(t, client.login("alice@mail1.test", adminPassword))

		// First deliver a throwaway message
		subject := fmt.Sprintf("delete-me-%d", time.Now().UnixNano())
		resp, err := client.post("/api/v1/messages/deliver", map[string]string{
			"address":   "alice@mail1.test",
			"sender":    "deletesender@test.local",
			"subject":   subject,
			"body_text": "This will be deleted",
		})
		requireNoError(t, err)
		resp.Body.Close()

		msgID := waitForMessage(t, client, alice.ID, "INBOX", subject, 15*time.Second)

		// Delete it
		delResp, err := client.delete(fmt.Sprintf("/api/v1/messages/%d", msgID))
		requireNoError(t, err)
		if delResp.StatusCode >= 400 {
			body := readBody(delResp)
			t.Fatalf("delete failed (%d): %s", delResp.StatusCode, body)
		}
		delResp.Body.Close()
		t.Logf("Deleted message %d", msgID)

		// Verify it's gone
		getResp, err := client.get(fmt.Sprintf("/api/v1/messages/%d", msgID))
		requireNoError(t, err)
		if getResp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404 after delete, got %d", getResp.StatusCode)
		}
		getResp.Body.Close()
	})

	t.Run("WebmailAccountSwitching", func(t *testing.T) {
		// Login as alice, check accounts
		client := newAPIClient()
		requireNoError(t, client.login("alice@mail1.test", adminPassword))

		resp, err := client.get("/api/v1/accounts")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var result struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := decodeJSON(resp, &result); err != nil {
			t.Fatalf("decode accounts: %v", err)
		}
		t.Logf("Accounts for alice: %d entries", len(result.Data))
	})
}
