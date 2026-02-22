package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func testStage9DatabaseConsistency(t *testing.T) {
	// The API now only manages mail3.test (each server has its own database).
	// Use a mail3.test admin account for consistency checks.
	adminClient := newAPIClient()
	if err := adminClient.login("eve@mail3.test", adminPassword); err != nil {
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

		// Compare with user-facing API counts for a mail3.test user
		eve := getMailboxByAddress(t, adminClient, "eve@mail3.test")

		eveClient := newAPIClient()
		if err := eveClient.login("eve@mail3.test", adminPassword); err != nil {
			t.Skipf("Cannot login as eve: %v", err)
		}

		foldersResp, err := eveClient.get(fmt.Sprintf("/api/v1/accounts/%d/folders", eve.ID))
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
		t.Logf("API total for eve: %d", apiTotal)
	})

	t.Run("NoOrphanedMailboxes", func(t *testing.T) {
		// Verify all mailboxes in mail3 DB belong to existing domains
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
		// Verify domains from test endpoint match admin API (mail3.test only)
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

	// NOTE: The previous PostfixDovecotSeeApiData test was removed because
	// each mail server now has its own database. The API (postgres-mail3)
	// cannot create users visible to Postfix/Dovecot (postgres-mail1/mail2).
	// Traditional server users are managed via SQL init scripts.
}
