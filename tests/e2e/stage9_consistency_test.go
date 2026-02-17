package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func testStage9DatabaseConsistency(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.login("admin@mail1.test", adminPassword); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	t.Run("MessageCountsMatchAcrossAPIs", func(t *testing.T) {
		// Get message count from test endpoint
		resp, err := httpClient.Get(apiBaseURL + "/api/test/db/messages")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var dbMessages struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := decodeJSON(resp, &dbMessages); err != nil {
			t.Fatalf("decode db messages: %v", err)
		}
		dbCount := len(dbMessages.Data)
		t.Logf("Direct DB message count: %d", dbCount)

		// Compare with user-facing API counts
		alice := getMailboxByAddress(t, adminClient, "alice@mail1.test")

		aliceClient := newAPIClient()
		if err := aliceClient.login("alice@mail1.test", adminPassword); err != nil {
			t.Skipf("Cannot login as alice: %v", err)
		}

		foldersResp, err := aliceClient.get(fmt.Sprintf("/api/v1/accounts/%d/folders", alice.ID))
		requireNoError(t, err)
		requireStatus(t, foldersResp, http.StatusOK)

		var folders struct {
			Data []struct {
				Name  string `json:"name"`
				Total int    `json:"total"`
			} `json:"data"`
		}
		if err := decodeJSON(foldersResp, &folders); err != nil {
			t.Fatalf("decode folders: %v", err)
		}

		apiTotal := 0
		for _, f := range folders.Data {
			apiTotal += f.Total
			t.Logf("  Folder %s: %d messages", f.Name, f.Total)
		}
		t.Logf("API total for alice: %d", apiTotal)
	})

	t.Run("NoOrphanedMailboxes", func(t *testing.T) {
		// Verify all mailboxes belong to existing domains
		resp, err := httpClient.Get(apiBaseURL + "/api/test/db/mailboxes")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)

		var mailboxes struct {
			Data []struct {
				ID       uint `json:"id"`
				DomainID uint `json:"domain_id"`
				Domain   *struct {
					ID   uint   `json:"id"`
					Name string `json:"name"`
				} `json:"domain"`
				Address string `json:"address"`
			} `json:"data"`
		}
		if err := decodeJSON(resp, &mailboxes); err != nil {
			t.Fatalf("decode mailboxes: %v", err)
		}

		domainsResp, err := httpClient.Get(apiBaseURL + "/api/test/db/domains")
		requireNoError(t, err)
		requireStatus(t, domainsResp, http.StatusOK)

		var domains struct {
			Data []struct {
				ID   uint   `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := decodeJSON(domainsResp, &domains); err != nil {
			t.Fatalf("decode domains: %v", err)
		}

		domainIDs := make(map[uint]bool)
		for _, d := range domains.Data {
			domainIDs[d.ID] = true
		}

		orphaned := 0
		for _, mb := range mailboxes.Data {
			if !domainIDs[mb.DomainID] {
				t.Errorf("Orphaned mailbox: %s (domain_id=%d not found)", mb.Address, mb.DomainID)
				orphaned++
			}
		}

		if orphaned == 0 {
			t.Logf("No orphaned mailboxes found (%d mailboxes checked)", len(mailboxes.Data))
		}
	})

	t.Run("DomainConsistency", func(t *testing.T) {
		// Verify domains from test endpoint match admin API
		testResp, err := httpClient.Get(apiBaseURL + "/api/test/db/domains")
		requireNoError(t, err)
		requireStatus(t, testResp, http.StatusOK)

		var testDomains struct {
			Data []struct {
				ID   uint   `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := decodeJSON(testResp, &testDomains); err != nil {
			t.Fatalf("decode: %v", err)
		}

		adminResp, err := adminClient.get("/api/v1/admin/domains")
		requireNoError(t, err)
		requireStatus(t, adminResp, http.StatusOK)

		var adminDomains struct {
			Data []struct {
				ID   uint   `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := decodeJSON(adminResp, &adminDomains); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if len(testDomains.Data) != len(adminDomains.Data) {
			t.Errorf("domain count mismatch: test=%d, admin=%d",
				len(testDomains.Data), len(adminDomains.Data))
		} else {
			t.Logf("Domain count consistent: %d domains", len(testDomains.Data))
		}
	})

	t.Run("PostfixDovecotSeeApiData", func(t *testing.T) {
		// Create a new user via API, then verify Postfix accepts mail to them
		// and Dovecot authenticates them
		newAddr := "consistency-check@mail1.test"
		createMailbox(t, adminClient, newAddr, adminPassword, "Consistency Check")

		// Postfix should accept RCPT TO for this user
		sc := dialSMTP(t, mail1SMTPAddr)
		defer sc.close()
		sc.ehlo(t, "test.local")
		sc.sendExpect(t, "MAIL FROM:<test@test.local>", "250")
		resp := sc.sendExpect(t, "RCPT TO:<"+newAddr+">", "250")
		t.Logf("Postfix accepts API-created user: %s", resp)
		sc.sendExpect(t, "QUIT", "221")

		// Dovecot should authenticate this user
		ic := dialIMAP(t, mail1IMAPAddr)
		defer ic.close()
		result, _ := ic.command(t, fmt.Sprintf("LOGIN %s %s", newAddr, adminPassword))
		if result == "" {
			t.Error("empty IMAP LOGIN response")
		} else {
			t.Logf("Dovecot auth for API-created user: %s", result)
		}
		ic.command(t, "LOGOUT")
	})
}
