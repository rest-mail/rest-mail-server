package filters

import (
	"fmt"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
)

// A domain that is not live is not served: it has no usable certificate, so its
// mail cannot be carried over TLS at all (issues #289, #290). Mail addressed to
// it must be rejected like any unknown recipient, rather than accepted because a
// mailbox row happens to exist.
func TestRecipientCheck_DomainNotLive(t *testing.T) {
	gdb := openRecipientCheckTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	unique := time.Now().UnixNano()
	live := models.Domain{Name: fmt.Sprintf("live-%d.test", unique), ServerType: "traditional", Active: true}
	if err := tx.Create(&live).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	dormant := models.Domain{Name: fmt.Sprintf("dormant-%d.test", unique), ServerType: "traditional", Active: false}
	if err := tx.Create(&dormant).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	for _, d := range []models.Domain{live, dormant} {
		if err := tx.Create(&models.Mailbox{
			DomainID:  d.ID,
			LocalPart: "user",
			Address:   "user@" + d.Name,
			Active:    true,
		}).Error; err != nil {
			t.Fatalf("create mailbox: %v", err)
		}
	}

	if err := tx.Create(&models.Alias{
		DomainID:           dormant.ID,
		SourceAddress:      "team@" + dormant.Name,
		DestinationAddress: "user@" + live.Name,
		Active:             true,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}

	t.Run("a mailbox on a live domain is accepted", func(t *testing.T) {
		res := runRecipientCheck(t, tx, "user@"+live.Name)
		if res.Action == pipeline.ActionReject {
			t.Fatalf("mail for a live domain was rejected: %+v", res.Log)
		}
	})

	t.Run("a mailbox on a domain that is not live is rejected", func(t *testing.T) {
		res := runRecipientCheck(t, tx, "user@"+dormant.Name)
		if res.Action != pipeline.ActionReject {
			t.Fatalf("action = %v, want reject for a domain that is not live (%+v)", res.Action, res.Log)
		}
	})

	t.Run("an alias on a domain that is not live is rejected", func(t *testing.T) {
		res := runRecipientCheck(t, tx, "team@"+dormant.Name)
		if res.Action != pipeline.ActionReject {
			t.Fatalf("action = %v, want reject for an alias on a domain that is not live (%+v)", res.Action, res.Log)
		}
	})
}
