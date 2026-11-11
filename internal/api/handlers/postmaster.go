package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

const postmasterRequiredDescription = "A domain must retain a receiving postmaster address. A change that removes its last working account or alias, including disabling a destination or removing its receive grant, returns 409 postmaster_required. To replace the account, create a working alias at postmaster@<domain> first, then delete the account; a disabled account still shadows an alias."

var errPostmasterRequired = endpoint.Refuse(http.StatusConflict, "postmaster_required", "A domain must keep a receiving postmaster address. Create a working postmaster alias before deleting its account; disabling the account still shadows the alias.")

func provisionPostmaster(tx *gorm.DB, request *http.Request, domain models.Domain) error {
	account, err := repositories.CreateDomainPostmaster(tx, domain)
	if err != nil {
		return err
	}
	if claims := middleware.GetClaims(request); claims != nil {
		var owner models.Role
		if err := tx.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
			return err
		}
		entry := middleware.NewAuditEntry(ActionAccountsCreate, request, time.Now())
		entry.Status = http.StatusCreated
		entry.Target["account_id"] = strconv.FormatUint(uint64(account.ID), 10)
		entry.Target["address"] = account.Address
		entry.Target["password"] = "false"
		entry.Target["receive"] = "true"
		if err := audit.Append(tx, entry); err != nil {
			return err
		}
		return grantOwner(tx, request, claims, account, owner)
	}
	return nil
}

func postmasterConfigured(db *gorm.DB, domain models.Domain) (bool, error) {
	_, err := repositories.ResolveSetupRecipient(db, "postmaster@"+domain.Name, domain.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func preservePostmasters(db *gorm.DB, protect bool, change func(*gorm.DB) error) error {
	if !protect {
		return db.Transaction(change)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var domains []models.Domain
		if err := tx.Where("EXISTS (SELECT 1 FROM mailboxes WHERE mailboxes.domain_id = domains.id AND lower(mailboxes.local_part) = ?) OR EXISTS (SELECT 1 FROM aliases WHERE aliases.domain_id = domains.id)", "postmaster").Order("id").Find(&domains).Error; err != nil {
			return err
		}
		protected := make([]models.Domain, 0, len(domains))
		for _, domain := range domains {
			configured, err := postmasterConfigured(tx, domain)
			if err != nil {
				return fmt.Errorf("check postmaster configuration: %w", err)
			}
			if configured {
				protected = append(protected, domain)
			}
		}
		if err := change(tx); err != nil {
			return err
		}
		for _, domain := range protected {
			var current models.Domain
			if err := tx.First(&current, domain.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			configured, err := postmasterConfigured(tx, current)
			if err != nil {
				return err
			}
			if !configured {
				return errPostmasterRequired
			}
		}
		return nil
	})
}
