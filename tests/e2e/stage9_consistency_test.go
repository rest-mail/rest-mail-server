package e2e

import (
	"fmt"
	"net/http"
	"testing"
)

// Stage 9 checks internal consistency of the product's own database via its
// admin API. (The old /api/test/db/* raw-dump endpoints it used to cross-check
// against are gone — each server owns its own DB now — so consistency is
// verified between the admin endpoints that DO exist.)
func testStage9DatabaseConsistency(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.loginAdmin("admin", "admin123!@"); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	type domain struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	}
	type mailbox struct {
		ID       uint   `json:"id"`
		Address  string `json:"address"`
		DomainID uint   `json:"domain_id"`
		Domain   domain `json:"domain"`
	}

	listDomains := func(t *testing.T) []domain {
		resp, err := adminClient.get("/api/v1/admin/domains")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
		var out struct {
			Data []domain `json:"data"`
		}
		requireNoError(t, decodeJSON(resp, &out))
		return out.Data
	}
	listMailboxes := func(t *testing.T) []mailbox {
		resp, err := adminClient.get("/api/v1/admin/mailboxes")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
		var out struct {
			Data []mailbox `json:"data"`
		}
		requireNoError(t, decodeJSON(resp, &out))
		return out.Data
	}

	t.Run("MailboxCountMatchesStats", func(t *testing.T) {
		mailboxes := listMailboxes(t)

		resp, err := adminClient.get("/api/v1/admin/stats")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
		var stats struct {
			Data struct {
				DomainCount  int `json:"domainCount"`
				MailboxCount int `json:"mailboxCount"`
			} `json:"data"`
		}
		requireNoError(t, decodeJSON(resp, &stats))

		if stats.Data.MailboxCount != len(mailboxes) {
			t.Errorf("mailbox count inconsistent: stats=%d, list=%d",
				stats.Data.MailboxCount, len(mailboxes))
		} else {
			t.Logf("Mailbox count consistent across stats + list: %d", len(mailboxes))
		}
		if stats.Data.DomainCount != len(listDomains(t)) {
			t.Errorf("domain count inconsistent: stats=%d, list=%d",
				stats.Data.DomainCount, len(listDomains(t)))
		}
	})

	t.Run("NoOrphanedMailboxes", func(t *testing.T) {
		domainIDs := make(map[uint]bool)
		for _, d := range listDomains(t) {
			domainIDs[d.ID] = true
		}

		mailboxes := listMailboxes(t)
		orphaned := 0
		for _, mb := range mailboxes {
			if !domainIDs[mb.DomainID] {
				t.Errorf("orphaned mailbox: %s (domain_id=%d not in domains list)", mb.Address, mb.DomainID)
				orphaned++
			}
			// The embedded domain must agree with the FK.
			if mb.Domain.ID != 0 && mb.Domain.ID != mb.DomainID {
				t.Errorf("mailbox %s: embedded domain id %d != domain_id %d", mb.Address, mb.Domain.ID, mb.DomainID)
			}
		}
		if orphaned == 0 {
			t.Logf("No orphaned mailboxes (%d checked against %d domains)", len(mailboxes), len(domainIDs))
		}
	})

	t.Run("MailboxAddressMatchesDomain", func(t *testing.T) {
		// Every mailbox address's domain part must equal its owning domain name.
		mailboxes := listMailboxes(t)
		for _, mb := range mailboxes {
			if mb.Domain.Name == "" {
				continue
			}
			want := fmt.Sprintf("@%s", mb.Domain.Name)
			if len(mb.Address) <= len(want) || mb.Address[len(mb.Address)-len(want):] != want {
				t.Errorf("mailbox %s does not belong to its domain %q", mb.Address, mb.Domain.Name)
			}
		}
		t.Logf("All %d mailbox addresses match their domain", len(mailboxes))
	})
}
