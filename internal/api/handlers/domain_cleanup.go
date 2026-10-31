package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const domainDeletionDescription = "A domain containing accounts or aliases is refused with 409 domain_not_empty. An empty domain and its live pipelines, custom filters, sender rules, certificates, rotation keys, MTA-STS policy, relay credentials and suppressions are removed atomically. Grants on the domain and its mailbox scopes, including eligible activations, are removed through the audited grant-change mechanism; cached permissions are invalidated only after commit. Historical TLS reports, message traces, aggregate reports and audit entries are retained under their original identities and existing retention rules. A cleanup, audit or cancellation failure rolls the deletion back."

// deleteDomainData removes live state explicitly: migrations deliberately do
// not create foreign-key cascades. Historical evidence is retained, never
// reassigned to a later domain with the same name.
func (h *DomainHandler) deleteDomainData(r *http.Request, id uint32) error {
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
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
		for _, held := range []any{&models.Mailbox{}, &models.Alias{}} {
			var count int64
			if err := tx.Model(held).Where("domain_id = ?", domain.ID).Count(&count).Error; err != nil {
				return fmt.Errorf("check the domain's addresses: %w", err)
			}
			if count > 0 {
				return endpoint.Refuse(http.StatusConflict, "domain_not_empty", "The domain still has accounts or aliases: delete them first")
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
