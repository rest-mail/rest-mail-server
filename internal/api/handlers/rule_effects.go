package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/rules"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type undoRuleEffectRequest struct {
	ID       uint32 `path:"id" minimum:"1" example:"1"`
	EffectID string `path:"effectID" format:"uuid" example:"00000000-0000-4000-8000-000000000001"`
}

func (h *MessageHandler) RegisterRuleEffects(rs endpoint.Routes) {
	endpoint.Register(rs, endpoint.Operation{Method: "GET", Path: "/api/v1/messages/{id}/rule-effects", OperationID: "listMessageRuleEffects", Summary: "Explain the rule effects on a delivered message", Description: "Delivery snapshots identify the stable rule ID and version, action, previous value and undoability. Raw Sieve actions have no fabricated structured identity. Effects live with the message and survive rule deletion. A forward is recorded only when its outbound queue row commits with the local message, and is never undoable.", Tags: []string{"Rules"}, Errors: []int{403, 404}, Security: handlerChecked(models.CapMailRead, mailRulesSchemes...)}, h.listRuleEffects)
	endpoint.Register(rs, endpoint.Operation{Method: "POST", Path: "/api/v1/messages/{id}/rule-effects/{effectID}/undo", OperationID: "undoMessageRuleEffect", Summary: "Undo one local rule effect", Description: "Restores the previous folder or system flag atomically with marking the effect undone. Idempotent after success. 409 rule_effect_not_undoable for a forward. 409 rule_effect_changed if the current field differs from the recorded result or a later effect on that field has not been undone; later user changes are preserved. Effects are retained with the message, with no separate expiry.", Tags: []string{"Rules"}, Errors: []int{403, 404, 409}, Security: handlerChecked(models.CapMailWrite, mailRulesSchemes...)}, h.undoRuleEffect)
}
func (h *MessageHandler) listRuleEffects(r *http.Request, in messagePath) (endpoint.List[rules.Effect], error) {
	msg, err := h.ownedMessage(r, in.ID, models.CapMailRead)
	if err != nil {
		return endpoint.List[rules.Effect]{}, err
	}
	effects := msg.RuleEffects
	if effects == nil {
		effects = []rules.Effect{}
	}
	return endpoint.List[rules.Effect]{Data: effects}, nil
}

var errRuleEffectChanged = endpoint.Refuse(409, "rule_effect_changed", "The affected field changed or a later rule effect must be undone first")

func (h *MessageHandler) undoRuleEffect(r *http.Request, in undoRuleEffectRequest) (rules.Effect, error) {
	h = h.forRequest(r)
	owned, err := h.ownedMessage(r, in.ID, models.CapMailWrite)
	if err != nil {
		return rules.Effect{}, err
	}
	var answer rules.Effect
	changed := false
	err = h.db.Transaction(func(tx *gorm.DB) error {
		var msg models.Message
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("mailbox_id = ?", owned.MailboxID).First(&msg, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errMessageNotFound
			}
			return err
		}
		index := -1
		for i, e := range msg.RuleEffects {
			if e.ID == in.EffectID {
				index = i
				break
			}
		}
		if index < 0 {
			return endpoint.Refuse(404, "not_found", "Rule effect not found")
		}
		answer = msg.RuleEffects[index]
		if !answer.Undoable {
			return endpoint.Refuse(409, "rule_effect_not_undoable", "A forwarded message cannot be recalled")
		}
		if answer.UndoneAt != nil {
			return nil
		}
		for _, later := range msg.RuleEffects[index+1:] {
			if later.UndoneAt == nil && later.Type == answer.Type && (answer.Type == "fileinto" || later.Value == answer.Value) {
				return errRuleEffectChanged
			}
		}
		updates := map[string]any{}
		switch answer.Type {
		case "fileinto":
			if msg.Folder != answer.Value {
				return errRuleEffectChanged
			}
			updates["folder"] = answer.Before
		case "flag":
			column := rules.FlagColumn(answer.Value)
			current := false
			switch column {
			case "is_read":
				current = msg.IsRead
			case "is_flagged":
				current = msg.IsFlagged
			case "is_draft":
				current = msg.IsDraft
			default:
				return errRuleEffectChanged
			}
			if !current {
				return errRuleEffectChanged
			}
			updates[column] = rules.Bool(answer.Before)
		default:
			return endpoint.Refuse(409, "rule_effect_not_undoable", "This rule effect cannot be undone")
		}
		now := tx.NowFunc()
		answer.UndoneAt = &now
		msg.RuleEffects[index] = answer
		encoded, err := json.Marshal(msg.RuleEffects)
		if err != nil {
			return err
		}
		updates["rule_effects"] = string(encoded)
		if err := tx.Model(&msg).Updates(updates).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err == nil && changed && h.broker != nil {
		h.broker.Publish(r.Context(), owned.MailboxID, SSEEvent{Type: "message_updated", Data: map[string]any{"message_id": owned.ID}})
	}
	return answer, err
}

func nativeForward(email *pipeline.EmailJSON, target string) bool {
	if email == nil {
		return false
	}
	for _, effect := range email.RuleEffects {
		if effect.Type == "forward" && effect.Value == target {
			return true
		}
	}
	return false
}

// commitNativeRuleEffects shares the delivery transaction. A failed forward or
// attribution write rolls back the local message, quota and every native forward.
func commitNativeRuleEffects(tx *gorm.DB, msg *models.Message, email *pipeline.EmailJSON, forwards []models.OutboundQueue) error {
	if email == nil || len(email.RuleMatches) == 0 {
		return nil
	}
	queued := map[string]bool{}
	for i := range forwards {
		if err := tx.Create(&forwards[i]).Error; err != nil {
			return err
		}
		queued[forwards[i].Recipient] = true
	}
	acted := map[uint]bool{}
	for _, id := range email.RuleMatches {
		acted[id] = true
	}
	for _, effect := range email.RuleEffects {
		if effect.Type == "forward" && !queued[effect.Value] {
			continue
		}
		msg.RuleEffects = append(msg.RuleEffects, effect)
		acted[effect.RuleID] = true
	}
	for id := range acted {
		if err := tx.Model(&models.MailRule{}).Where("id = ? AND mailbox_id = ?", id, msg.MailboxID).UpdateColumn("run_count", gorm.Expr("run_count + 1")).Error; err != nil {
			return err
		}
	}
	return nil
}
