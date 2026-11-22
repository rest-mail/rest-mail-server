package handlers

import (
	"errors"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"github.com/rest-mail/rest-mail-server/internal/rules"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type NativeRuleHandler struct{ db *gorm.DB }

func NewNativeRuleHandler(db *gorm.DB) *NativeRuleHandler { return &NativeRuleHandler{db: db} }

type NativeRule struct {
	rules.Rule
	RunCount uint64 `json:"run_count"`
}
type nativeRulePath struct {
	ID     uint32 `path:"id" minimum:"1" example:"1"`
	RuleID uint32 `path:"ruleID" minimum:"1" example:"1"`
}
type createNativeRuleRequest struct {
	ID   uint32           `path:"id" minimum:"1" example:"1"`
	Body rules.Definition `body:""`
}
type updateNativeRuleRequest struct {
	ID     uint32           `path:"id" minimum:"1" example:"1"`
	RuleID uint32           `path:"ruleID" minimum:"1" example:"1"`
	Body   rules.Definition `body:""`
}

// RuleSieveView identifies unconverted raw Sieve explicitly. Exported identity
// markers survive all native lifecycle operations; raw text is never imported.
type RuleSieveView struct {
	Representation string `json:"representation" enums:"structured,raw,empty"`
	Script         string `json:"script"`
	Active         bool   `json:"active"`
	RuleIDs        []uint `json:"rule_ids"`
}

var errRuleNotFound = endpoint.Refuse(http.StatusNotFound, "not_found", "Rule not found")
var errRawRuleConflict = endpoint.Refuse(http.StatusConflict, "rule_representation_conflict", "Raw Sieve and structured rules cannot coexist; delete the existing representation first")

func (h *NativeRuleHandler) Register(rs endpoint.Routes) {
	const path = "/api/v1/accounts/{id}/rules"
	read := handlerChecked(models.CapMailRead, mailRulesSchemes...)
	write := handlerChecked(models.CapMailWrite, mailRulesSchemes...)
	tags := []string{"Rules"}
	endpoint.Register(rs, endpoint.Operation{Method: "GET", Path: path, OperationID: "listMailRules", Summary: "List structured mail rules", Description: "Ordered by position, then stable rule ID. At most 100 rules per account. Raw Sieve is never silently converted; getMailRuleSieve identifies it explicitly.", Tags: tags, Errors: []int{403, 404}, Security: read}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: "POST", Path: path, OperationID: "createMailRule", Summary: "Create a structured mail rule", Description: "Creates one stable rule identity. All 1–20 conditions must match; 1–20 actions run in order. Subject and parsed From/To address comparisons use ASCII case folding. Fileinto, system flags and copy-forwards are supported. A forward follows the deployment Sieve redirect allowlist and loop guards. The native engine runs bounded structured data, not the exported Sieve. The domain's enabled sieve step controls account filtering. Run counts count matched committed deliveries once per rule. 409 rule_representation_conflict when raw Sieve exists; 409 rule_limit at 100 rules.", Tags: tags, Status: 201, Errors: []int{403, 404, 409}, Security: write}, h.create)
	endpoint.Register(rs, endpoint.Operation{Method: "GET", Path: path + "/{ruleID}", OperationID: "getMailRule", Summary: "Get a structured mail rule", Tags: tags, Errors: []int{403, 404}, Security: read}, h.get)
	endpoint.Register(rs, endpoint.Operation{Method: "PUT", Path: path + "/{ruleID}", OperationID: "updateMailRule", Summary: "Replace a structured mail rule", Description: "Preserves the ID and increments its version, including enable, disable and position changes. Equal positions sort by ID. Saved delivery effects retain the version that acted.", Tags: tags, Errors: []int{403, 404}, Security: write}, h.update)
	endpoint.Register(rs, endpoint.Operation{Method: "DELETE", Path: path + "/{ruleID}", OperationID: "deleteMailRule", Summary: "Delete a structured mail rule", Description: "Removes the rule from native execution and Sieve export atomically. Existing delivered-message effects retain its ID and version. No generated script row is left behind.", Tags: tags, Status: 204, Errors: []int{403, 404}, Security: write}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: "GET", Path: path + "/sieve", OperationID: "getMailRuleSieve", Summary: "Export the account rules as Sieve", Description: "The same generated text as getSieveScript, with a stable ID/version/enabled marker per rule. Disabled rules have only a marker. Raw scripts are returned verbatim with representation raw and no structured IDs; they are never converted. This is the shared export surface for future ManageSieve transport; this server has no RFC 5804 listener yet.", Tags: tags, Errors: []int{403, 404}, Security: read}, h.sieve)
}
func nativeRuleOf(row models.MailRule) NativeRule {
	return NativeRule{Rule: rules.Rule{ID: row.ID, Version: row.Version, Definition: row.Definition}, RunCount: row.RunCount}
}
func loadNativeRules(db *gorm.DB, mailboxID uint) ([]models.MailRule, error) {
	var rows []models.MailRule
	err := db.Where("mailbox_id = ?", mailboxID).Order("position, id").Limit(rules.MaxRules + 1).Find(&rows).Error
	return rows, err
}
func nativeSnapshots(rows []models.MailRule) []rules.Rule {
	rs := make([]rules.Rule, len(rows))
	for i, row := range rows {
		rs[i] = nativeRuleOf(row).Rule
	}
	return rs
}

// Every representation writer locks the same mailbox row. This serializes
// create/delete/update with raw Sieve writes, including an initially empty set.
func lockRuleAccount(tx *gorm.DB, id uint) error {
	var account models.Mailbox
	return tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, id).Error
}
func (h *NativeRuleHandler) list(r *http.Request, in accountPath) (endpoint.List[NativeRule], error) {
	db := h.db.WithContext(r.Context())
	id, err := mailRulesMailbox(db, r, in.ID)
	if err != nil {
		return endpoint.List[NativeRule]{}, err
	}
	rows, err := loadNativeRules(db, id)
	if err != nil {
		return endpoint.List[NativeRule]{}, err
	}
	out := make([]NativeRule, len(rows))
	for i, row := range rows {
		out[i] = nativeRuleOf(row)
	}
	return endpoint.List[NativeRule]{Data: out}, nil
}
func (h *NativeRuleHandler) get(r *http.Request, in nativeRulePath) (NativeRule, error) {
	db := h.db.WithContext(r.Context())
	id, err := mailRulesMailbox(db, r, in.ID)
	if err != nil {
		return NativeRule{}, err
	}
	var row models.MailRule
	if err := db.Where("mailbox_id = ?", id).First(&row, in.RuleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return NativeRule{}, errRuleNotFound
		}
		return NativeRule{}, err
	}
	return nativeRuleOf(row), nil
}
func (h *NativeRuleHandler) create(r *http.Request, in createNativeRuleRequest) (NativeRule, error) {
	db := h.db.WithContext(r.Context())
	id, err := mailRulesMailbox(db, r, in.ID)
	if err != nil {
		return NativeRule{}, err
	}
	if err := rules.Validate(in.Body); err != nil {
		return NativeRule{}, endpoint.Invalid(map[string]string{"rule": err.Error()})
	}
	row := models.MailRule{MailboxID: id, Version: 1, Definition: in.Body}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := lockRuleAccount(tx, id); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.SieveScript{}).Where("mailbox_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errRawRuleConflict
		}
		if err := tx.Model(&models.MailRule{}).Where("mailbox_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count >= rules.MaxRules {
			return endpoint.Refuse(409, "rule_limit", "An account may have at most 100 structured rules")
		}
		return tx.Create(&row).Error
	})
	return nativeRuleOf(row), err
}
func (h *NativeRuleHandler) update(r *http.Request, in updateNativeRuleRequest) (NativeRule, error) {
	db := h.db.WithContext(r.Context())
	id, err := mailRulesMailbox(db, r, in.ID)
	if err != nil {
		return NativeRule{}, err
	}
	if err := rules.Validate(in.Body); err != nil {
		return NativeRule{}, endpoint.Invalid(map[string]string{"rule": err.Error()})
	}
	var row models.MailRule
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := lockRuleAccount(tx, id); err != nil {
			return err
		}
		if err := tx.Where("mailbox_id = ?", id).First(&row, in.RuleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errRuleNotFound
			}
			return err
		}
		row.Definition = in.Body
		row.Version++
		return tx.Save(&row).Error
	})
	return nativeRuleOf(row), err
}
func (h *NativeRuleHandler) delete(r *http.Request, in nativeRulePath) (endpoint.Empty, error) {
	db := h.db.WithContext(r.Context())
	id, err := mailRulesMailbox(db, r, in.ID)
	if err != nil {
		return endpoint.Empty{}, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := lockRuleAccount(tx, id); err != nil {
			return err
		}
		result := tx.Where("mailbox_id = ? AND id = ?", id, in.RuleID).Delete(&models.MailRule{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errRuleNotFound
		}
		return nil
	})
	return endpoint.Empty{}, err
}
func ruleSieveView(db *gorm.DB, mailboxID uint) (RuleSieveView, error) {
	rows, err := loadNativeRules(db, mailboxID)
	if err != nil {
		return RuleSieveView{}, err
	}
	view := RuleSieveView{Representation: "empty", RuleIDs: []uint{}}
	if len(rows) > 0 {
		view.Representation = "structured"
		view.Script = rules.Export(nativeSnapshots(rows))
		for _, row := range rows {
			view.RuleIDs = append(view.RuleIDs, row.ID)
			view.Active = view.Active || row.Enabled
		}
		return view, nil
	}
	var raw models.SieveScript
	err = db.Where("mailbox_id = ?", mailboxID).First(&raw).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return view, nil
	}
	if err != nil {
		return view, err
	}
	view.Representation = "raw"
	view.Script = raw.Script
	view.Active = raw.Active
	return view, nil
}
func (h *NativeRuleHandler) sieve(r *http.Request, in accountPath) (RuleSieveView, error) {
	db := h.db.WithContext(r.Context())
	id, err := mailRulesMailbox(db, r, in.ID)
	if err != nil {
		return RuleSieveView{}, err
	}
	return ruleSieveView(db, id)
}
func generatedSieveOf(view RuleSieveView, id uint) SieveScript {
	hash := filters.HashSieveScript(view.Script)
	return SieveScript{MailboxID: id, Script: view.Script, Active: view.Active, ScriptHash: &hash}
}
