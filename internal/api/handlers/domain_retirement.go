package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errDomainNotEmpty = endpoint.Refuse(http.StatusConflict, "domain_not_empty", "The domain still has accounts or aliases: delete them first")

var retiredDomainTables = []string{"domains", "mailboxes", "quota_usage", "pipelines", "custom_filters", "domain_sender_rules", "certificates", "dkim_rotation_keys", "mtasts_policies", "domain_relays", "suppressions"}

func archiveDomain(tx *gorm.DB, domain models.Domain, accounts []models.Mailbox, held []models.Grant) error {
	accountIDs := []uint{0}
	for _, account := range accounts {
		accountIDs = append(accountIDs, account.ID)
	}
	state := map[string]json.RawMessage{}
	for _, table := range retiredDomainTables {
		if table == "quota_usage" && len(accounts) == 0 {
			state[table] = json.RawMessage(`[]`)
			continue
		}
		column, value := "domain_id", any(domain.ID)
		if table == "domains" {
			column = "id"
		}
		if table == "quota_usage" {
			column, value = "mailbox_id", accountIDs
		}
		query := tx.Table(table).Select("COALESCE(jsonb_agg(to_jsonb(" + table + ")), '[]'::jsonb)")
		if table == "quota_usage" {
			query = query.Where(column+" IN ?", value)
		} else {
			query = query.Where(column+" = ?", value)
		}
		rows, err := query.Rows()
		if err != nil {
			return fmt.Errorf("archive %s: %w", table, err)
		}
		var encoded []byte
		if !rows.Next() {
			rows.Close()
			return fmt.Errorf("archive %s: missing snapshot", table)
		}
		err = rows.Scan(&encoded)
		rows.Close()
		if err != nil {
			return err
		}
		state[table] = encoded
	}
	removed := []models.Grant{}
	for _, grant := range held {
		if grants.Covers(grants.Domain(domain.Name), grant.Scope) {
			grant.Role = models.Role{}
			removed = append(removed, grant)
		}
	}
	encoded, err := json.Marshal(removed)
	if err != nil {
		return err
	}
	state["grants"] = encoded
	encoded, err = json.Marshal(state)
	if err != nil {
		return err
	}
	return tx.Create(&models.DomainRetirement{DomainID: domain.ID, Name: domain.Name, State: models.JSONB(encoded)}).Error
}

func (h *DomainHandler) restore(request *http.Request, in domainPath) (Domain, error) {
	var restored models.Domain
	err := h.db.WithContext(request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var retired models.DomainRetirement
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&retired, "domain_id = ?", in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errDomainNotFound
			}
			return err
		}
		if err := RequireTwoFactor(request); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.Domain{}).Where("id = ? OR name = ?", in.ID, retired.Name).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return endpoint.Refuse(http.StatusConflict, "domain_exists", "A live domain already uses this identity or name")
		}
		state := map[string]json.RawMessage{}
		if err := json.Unmarshal(retired.State, &state); err != nil {
			return err
		}
		for _, table := range retiredDomainTables {
			if _, ok := state[table]; !ok {
				return fmt.Errorf("retirement has no %s snapshot", table)
			}
			if err := tx.Exec("INSERT INTO "+table+" SELECT * FROM jsonb_populate_recordset(NULL::"+table+", ?::jsonb)", string(state[table])).Error; err != nil {
				return fmt.Errorf("restore %s: %w", table, err)
			}
		}
		var held []models.Grant
		if err := json.Unmarshal(state["grants"], &held); err != nil {
			return err
		}
		for _, grant := range held {
			if claims := middleware.GetClaims(request); claims != nil {
				if err := grants.Authorize(tx, claims.AccountID, grant.RoleID, grant.Scope); err != nil {
					return grantChangeError(request.Context(), err)
				}
			}
			if err := grants.Add(tx, &grant, middleware.RequestEntry(request, grants.ActionCreate, http.StatusOK)); err != nil {
				return err
			}
		}
		if err := tx.First(&restored, in.ID).Error; err != nil {
			return err
		}
		if err := request.Context().Err(); err != nil {
			return err
		}
		return tx.Delete(&retired).Error
	})
	if err != nil {
		return Domain{}, err
	}
	if h.cache != nil {
		h.cache.Invalidate()
	}
	return domainOf(restored), nil
}
