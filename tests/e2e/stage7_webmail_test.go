package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func testStage7WebmailFlows(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.loginAdmin(); err != nil {
		skipOrFail(t, "Cannot get admin token: %v", err)
	}

	createMailbox(t, adminClient, "alice@restmail.test", accountPassword, "Alice")
	ensureE2EPipeline(t, adminClient)
	_, aliceAcct := restmailInbox(t, "alice@restmail.test", accountPassword)

	t.Run("WebmailLogin", func(t *testing.T) {
		client := newAPIClient()
		err := client.login("alice@restmail.test", accountPassword)
		requireNoError(t, err)
		if client.token == "" {
			t.Fatal("no token after login")
		}
		t.Log("Webmail login successful")
	})

	t.Run("WebmailListFolders", func(t *testing.T) {
		client := newAPIClient()
		requireNoError(t, client.login("alice@restmail.test", accountPassword))

		resp, err := client.get(fmt.Sprintf("/api/v1/accounts/%d/folders", aliceAcct))
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
		requireNoError(t, client.login("alice@restmail.test", accountPassword))

		resp, err := client.get(fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages?limit=10", aliceAcct))
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
		createMailbox(t, adminClient, "bob@restmail.test", accountPassword, "Bob")
		subject := fmt.Sprintf("webmail-compose-%d", time.Now().UnixNano())

		client := newAPIClient()
		requireNoError(t, client.login("alice@restmail.test", accountPassword))

		// Compose = the OUTBOUND path (/messages/send). /messages/deliver is
		// inbound-only and DMARC-rejects a same-domain injected sender.
		resp, err := client.post("/api/v1/messages/send", map[string]any{
			"from":      "alice@restmail.test",
			"to":        []string{"bob@restmail.test"},
			"subject":   subject,
			"body_text": "Composed from webmail!",
		})
		requireNoError(t, err)
		requireSuccess(t, resp)
		resp.Body.Close()

		// Verify delivery
		bobClient, bobAcct := restmailInbox(t, "bob@restmail.test", accountPassword)
		msgID := waitForMessage(t, bobClient, bobAcct, "INBOX", subject, 30*time.Second)
		t.Logf("Webmail compose delivered: id=%d", msgID)
	})

	t.Run("WebmailMarkAsRead", func(t *testing.T) {
		client := newAPIClient()
		requireNoError(t, client.login("alice@restmail.test", accountPassword))

		// Get first unread message
		resp, err := client.get(fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages?limit=10", aliceAcct))
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
		requireNoError(t, client.login("alice@restmail.test", accountPassword))

		// First create a throwaway message via a self-send (outbound path).
		subject := fmt.Sprintf("delete-me-%d", time.Now().UnixNano())
		resp, err := client.post("/api/v1/messages/send", map[string]any{
			"from":      "alice@restmail.test",
			"to":        []string{"alice@restmail.test"},
			"subject":   subject,
			"body_text": "This will be deleted",
		})
		requireNoError(t, err)
		requireSuccess(t, resp)
		resp.Body.Close()

		msgID := waitForMessage(t, client, aliceAcct, "INBOX", subject, 15*time.Second)

		// DELETE is two-stage (standard webmail): the first moves the message to
		// Trash (soft delete, still retrievable), the second purges it. Delete
		// twice, then it's gone.
		for pass := 1; pass <= 2; pass++ {
			delResp, err := client.delete(fmt.Sprintf("/api/v1/messages/%d", msgID))
			requireNoError(t, err)
			if delResp.StatusCode >= 400 {
				body := readBody(delResp)
				t.Fatalf("delete pass %d failed (%d): %s", pass, delResp.StatusCode, body)
			}
			delResp.Body.Close()
		}
		t.Logf("Deleted message %d (to Trash, then purged)", msgID)

		// Verify it's gone
		getResp, err := client.get(fmt.Sprintf("/api/v1/messages/%d", msgID))
		requireNoError(t, err)
		if getResp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404 after purge, got %d", getResp.StatusCode)
		}
		getResp.Body.Close()
	})

	t.Run("WebmailAccountSwitching", func(t *testing.T) {
		// An account links bob's by proving its password: it is listed after
		// its own, and it reads bob's mail. Linking takes recent proof of a
		// second factor, so the linking account is one of this test's own,
		// enrolled in TOTP (the other stages sign alice in with a password).
		createMailbox(t, adminClient, "bob@restmail.test", accountPassword, "Bob")
		switcher := fmt.Sprintf("switcher-%d@restmail.test", time.Now().UnixNano())
		createMailbox(t, adminClient, switcher, accountPassword, "Switcher")
		client := withTwoFactor(t, switcher, accountPassword)

		resp, err := client.post("/api/v1/accounts/link", map[string]string{
			"address": "bob@restmail.test", "password": accountPassword,
		})
		requireNoError(t, err)
		// 409: linked by an earlier run against this database.
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
			t.Fatalf("link bob@restmail.test: %d: %s", resp.StatusCode, readBody(resp))
		}
		resp.Body.Close()

		resp, err = client.get("/api/v1/accounts")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var result struct {
			Data []struct {
				ID      uint   `json:"id"`
				Address string `json:"address"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &result); err != nil {
			t.Fatalf("decode accounts: %v", err)
		}
		if len(result.Data) < 2 || result.Data[0].Address != switcher {
			t.Fatalf("%s's accounts: %+v, want its own first, then bob's", switcher, result.Data)
		}
		var bob uint
		for _, a := range result.Data {
			if a.Address == "bob@restmail.test" {
				bob = a.ID
			}
		}
		if bob == 0 {
			t.Fatalf("%s's accounts: %+v, bob's is not among them", switcher, result.Data)
		}
		resp, err = client.get(fmt.Sprintf("/api/v1/accounts/%d/folders", bob))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
		resp.Body.Close()
	})
}
