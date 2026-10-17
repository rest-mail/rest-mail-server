package e2e

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// testStage14AppPasswords: an app password (#344), made with the mailbox's
// current password, signs a mail client in over IMAPS and SMTPS, is refused by
// the interactive sign-in, and stops working the moment it is revoked.
func testStage14AppPasswords(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.loginAdmin("admin", "admin123!@"); err != nil {
		skipOrFail(t, "Cannot get admin token: %v", err)
	}
	const user = "apppw-user@restmail.test"
	createDomain(t, adminClient, "restmail.test", "restmail")
	createMailbox(t, adminClient, user, "password123", "App Password User")
	client, accountID := restmailInbox(t, user, "password123")

	// Making one takes the mailbox's current password, entered again.
	resp, err := client.post(fmt.Sprintf("/api/v1/accounts/%d/credentials", accountID), map[string]string{"name": "e2e Thunderbird"})
	requireNoError(t, err)
	requireStatus(t, resp, http.StatusUnauthorized)
	resp.Body.Close()
	resp, err = client.post(fmt.Sprintf("/api/v1/accounts/%d/credentials", accountID),
		map[string]string{"name": "e2e Thunderbird", "password": "password123"})
	requireNoError(t, err)
	requireStatus(t, resp, http.StatusCreated)
	var created struct {
		Data struct {
			ID       uint   `json:"id"`
			Password string `json:"password"`
		} `json:"data"`
	}
	requireNoError(t, decodeJSON(resp, &created))
	appPassword := created.Data.Password
	if !strings.HasPrefix(appPassword, "rm_") || len(appPassword) > 64 {
		t.Fatalf("app password %q is not in the format", appPassword)
	}

	t.Run("IMAPS_SignsIn", func(t *testing.T) {
		ic := dialIMAPTLS(t, restmailIMAPAddr)
		defer ic.close()
		ic.login(t, user, appPassword)
		if result, _ := ic.command(t, "SELECT INBOX"); !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX with an app password: %s", result)
		}
		ic.command(t, "LOGOUT")
	})

	subject := fmt.Sprintf("app password %d", time.Now().UnixNano())
	t.Run("SMTPS_SignsInAndSends", func(t *testing.T) {
		sc := dialSMTPTLS(t, restmailSubmitAddr)
		defer sc.close()
		sc.ehlo(t, "test.local")
		sc.authPlain(t, user, appPassword)
		sc.sendExpect(t, "MAIL FROM:<"+user+">", "250")
		sc.sendExpect(t, "RCPT TO:<"+user+">", "250")
		sc.sendExpect(t, "DATA", "354")
		sc.send(t, fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <apppw-%d@test.local>\r\n\r\nSent with an app password.",
			user, user, subject, time.Now().Format(time.RFC1123Z), time.Now().UnixNano()))
		sc.sendExpect(t, ".", "250")
		sc.sendExpect(t, "QUIT", "221")
		waitForMessage(t, client, accountID, "INBOX", subject, 30*time.Second)
	})

	t.Run("LoginRefusesIt", func(t *testing.T) {
		// One attempt, so the login rate limit is not a concern.
		resp, err := newAPIClient().post("/api/v1/auth/login", map[string]string{"email": user, "password": appPassword})
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusUnauthorized)
		resp.Body.Close()
	})

	t.Run("RevokedStopsWorking", func(t *testing.T) {
		resp, err := client.delete(fmt.Sprintf("/api/v1/accounts/%d/credentials/%d", accountID, created.Data.ID))
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusNoContent)
		resp.Body.Close()

		ic := dialIMAPTLS(t, restmailIMAPAddr)
		defer ic.close()
		if result, _ := ic.command(t, fmt.Sprintf("LOGIN %s %s", user, appPassword)); !strings.Contains(result, "NO") {
			t.Errorf("IMAP LOGIN with a revoked app password: %s", result)
		}

		sc := dialSMTPTLS(t, restmailSubmitAddr)
		defer sc.close()
		sc.ehlo(t, "test.local")
		cred := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + appPassword))
		sc.sendExpect(t, "AUTH PLAIN "+cred, "535")
	})
}
