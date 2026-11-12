package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const domainDeletionDescription = "A domain containing accounts or aliases is refused with 409 domain_not_empty, except for its automatically provisioned postmaster when it holds no mail and has no credentials. The domain, that postmaster and their live configuration are archived atomically for restoration with their original identities. Grants are removed through the audited grant-change mechanism; cached permissions are invalidated only after commit. Historical reports, message traces and audit entries are retained. A cleanup, archive, audit or cancellation failure rolls the deletion back. Deleting the last postmaster on its own still returns 409 postmaster_required."

// deleteDomainData removes live state explicitly: migrations deliberately do
// not create foreign-key cascades. Historical evidence is retained, never
// reassigned to a later domain with the same name.
func (h *DomainHandler) deleteDomainData(r *http.Request, id uint32) error {
	err := preservePostmasters(h.db.WithContext(r.Context()), true, func(tx *gorm.DB) error {
		// Serialize with the existing grant mutation paths before reading grants.
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var domain models.Domain
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&domain, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errDomainNotFound
			}
			return err
		}
		var accounts []models.Mailbox
		var accountCount int64
		if err := tx.Model(&models.Mailbox{}).Where("domain_id = ?", domain.ID).Count(&accountCount).Error; err != nil {
			return err
		}
		if accountCount > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("domain_id = ?", domain.ID).Find(&accounts).Error; err != nil {
				return err
			}
		}
		for _, account := range accounts {
			if !account.ProvisionedPostmaster || account.LocalPart != "postmaster" || account.Address != "postmaster@"+domain.Name {
				return errDomainNotEmpty
			}
			if err := tx.Exec("LOCK TABLE messages, credentials IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
				return err
			}
			for _, held := range []struct {
				model  any
				column string
			}{{&models.Message{}, "mailbox_id"}, {&models.Credential{}, "account_id"}} {
				var count int64
				if err := tx.Model(held.model).Where(held.column+" = ?", account.ID).Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return errDomainNotEmpty
				}
			}
		}
		for _, held := range []any{&models.Alias{}} {
			var count int64
			if err := tx.Model(held).Where("domain_id = ?", domain.ID).Count(&count).Error; err != nil {
				return fmt.Errorf("check the domain's addresses: %w", err)
			}
			if count > 0 {
				return errDomainNotEmpty
			}
		}
		var heldOn []models.Grant
		if err := tx.Preload("Role").Where("scope = ? OR scope LIKE ?", grants.Domain(domain.Name), "mailbox:%@"+domain.Name).Order("id").Find(&heldOn).Error; err != nil {
			return err
		}
		if len(heldOn) > 0 {
			if err := RequireTwoFactor(r); err != nil {
				return err
			}
		}
		if err := archiveDomain(tx, domain, accounts, heldOn); err != nil {
			return err
		}
		for i := range heldOn {
			if !grants.Covers(grants.Domain(domain.Name), heldOn[i].Scope) {
				continue
			}
			// Match account deletion's existing held-on rule: deleting a resource
			// removes authority on that resource, not authority held by an actor.
			if err := grants.Remove(tx, &heldOn[i], middleware.RequestEntry(r, grants.ActionDelete, http.StatusNoContent)); err != nil {
				return err
			}
		}
		for _, live := range []any{&models.Pipeline{}, &models.CustomFilter{}, &models.DomainSenderRule{}, &models.Certificate{}, &models.DKIMRotationKey{}, &models.MTASTSPolicy{}, &models.DomainRelay{}, &models.Suppression{}} {
			if err := tx.Where("domain_id = ?", domain.ID).Delete(live).Error; err != nil {
				return fmt.Errorf("remove domain configuration: %w", err)
			}
		}
		for _, account := range accounts {
			if err := tx.Where("mailbox_id = ?", account.ID).Delete(&models.QuotaUsage{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&account).Error; err != nil {
				return err
			}
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		return tx.Delete(&domain).Error
	})
	if err != nil {
		return fmt.Errorf("delete domain: %w", err)
	}
	return nil
}
