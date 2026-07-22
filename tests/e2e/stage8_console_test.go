package e2e

import (
	"fmt"
	"net/http"
	"testing"
)

func testStage8ConsoleFlows(t *testing.T) {
	// The TUI is a terminal application — we can't drive it interactively
	// from a test. Instead, we test the API operations that the console performs:
	// domain management, user CRUD, inbox browsing, and compose/send.

	adminClient := newAPIClient()
	if err := adminClient.loginAdmin("admin", "admin123!@"); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	t.Run("ConsoleDomainManagement", func(t *testing.T) {
		// List domains
		resp, err := adminClient.get("/api/v1/admin/domains")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var list struct {
			Data []domainInfo `json:"data"`
		}
		if err := decodeJSON(resp, &list); err != nil {
			t.Fatalf("decode domains: %v", err)
		}
		t.Logf("Domains: %d", len(list.Data))

		// Create a test domain
		testDomain := createDomain(t, adminClient, "console-test.example", "traditional")
		t.Logf("Created domain: %+v", testDomain)

		// Delete it
		delResp, err := adminClient.delete(fmt.Sprintf("/api/v1/admin/domains/%d", testDomain.ID))
		requireNoError(t, err)
		if delResp.StatusCode >= 400 {
			body := readBody(delResp)
			t.Fatalf("delete domain failed (%d): %s", delResp.StatusCode, body)
		}
		delResp.Body.Close()
		t.Log("Domain created and deleted successfully")
	})

	t.Run("ConsoleUserCreation", func(t *testing.T) {
		// Create a user
		mb := createMailbox(t, adminClient, "consoleuser@restmail.test", adminPassword, "Console User")
		t.Logf("Created mailbox: %+v", mb)

		// Verify it shows in list
		resp, err := adminClient.get("/api/v1/admin/mailboxes")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var list struct {
			Data []mailboxInfo `json:"data"`
		}
		if err := decodeJSON(resp, &list); err != nil {
			t.Fatalf("decode mailboxes: %v", err)
		}

		found := false
		for _, m := range list.Data {
			if m.Address == "consoleuser@restmail.test" {
				found = true
				break
			}
		}
		if !found {
			t.Error("created mailbox not found in list")
		}
	})

	t.Run("ConsolePasswordReset", func(t *testing.T) {
		// Update mailbox password via PATCH
		mb := getMailboxByAddress(t, adminClient, "consoleuser@restmail.test")
		resp, err := adminClient.patch(fmt.Sprintf("/api/v1/admin/mailboxes/%d", mb.ID),
			map[string]string{"password": "newpassword123"})
		requireNoError(t, err)
		if resp.StatusCode >= 400 {
			body := readBody(resp)
			t.Fatalf("password reset failed (%d): %s", resp.StatusCode, body)
		}
		resp.Body.Close()

		// Verify new password works
		testClient := newAPIClient()
		err = testClient.login("consoleuser@restmail.test", "newpassword123")
		if err != nil {
			t.Fatalf("login with new password failed: %v", err)
		}
		t.Log("Password reset and login with new password successful")
	})

	t.Run("ConsoleInboxBrowsing", func(t *testing.T) {
		// Login as a seeded product user and browse inbox (same API the console
		// uses). restmailInbox resolves the webmail-account id.
		client, acctID := restmailInbox(t, "eve@restmail.test", "password123")

		// List folders
		resp, err := client.get(fmt.Sprintf("/api/v1/accounts/%d/folders", acctID))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
		resp.Body.Close()

		// List messages
		resp, err = client.get(fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages?limit=50", acctID))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var msgs struct {
			Data []struct {
				ID      uint   `json:"id"`
				Subject string `json:"subject"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &msgs); err != nil {
			t.Fatalf("decode messages: %v", err)
		}
		t.Logf("Inbox has %d messages", len(msgs.Data))

		if len(msgs.Data) > 0 {
			// Read first message
			msgResp, err := client.get(fmt.Sprintf("/api/v1/messages/%d", msgs.Data[0].ID))
			requireNoError(t, err)
			requireStatus(t, msgResp, http.StatusOK)
			msgResp.Body.Close()
			t.Logf("Read message: id=%d subject=%q", msgs.Data[0].ID, msgs.Data[0].Subject)
		}
	})

	t.Run("ConsoleComposeMail", func(t *testing.T) {
		// Compose = the OUTBOUND path. Authenticate as a seeded sender (eve)
		// and send via /messages/send — the API a webmail/console compose uses.
		// (/messages/deliver is inbound-only and DMARC-rejects same-domain injects.)
		sender := newAPIClient()
		requireNoError(t, sender.login("eve@restmail.test", "password123"))
		resp, err := sender.post("/api/v1/messages/send", map[string]any{
			"from":      "eve@restmail.test",
			"to":        []string{"consoleuser@restmail.test"},
			"subject":   "Console compose test",
			"body_text": "Sent from console test",
		})
		requireNoError(t, err)
		requireSuccess(t, resp)
		resp.Body.Close()
		t.Log("Console compose/send successful")
	})

	t.Run("ConsoleServerStatus", func(t *testing.T) {
		// The console polls /api/health for status — verify it works
		resp, err := httpClient.Get(apiBaseURL + "/api/health")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
		t.Log("Server health check OK")
	})
}
