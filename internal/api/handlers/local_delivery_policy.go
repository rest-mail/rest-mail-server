package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// localInboundPolicy chooses defaults only when the domain has no stored policy.
// An unavailable or corrupt policy cannot authorize delivery with other filters.
// Both policy and account-script reads share the delivery's cancellation budget.
func (h *MessageHandler) localInboundPolicy(ctx context.Context, mailbox models.Mailbox) (*pipeline.PipelineConfig, error) {
	db := h.db.WithContext(ctx)
	var stored models.Pipeline
	err := db.Where("domain_id = ? AND direction = ? AND active = ?", mailbox.DomainID, "inbound", true).First(&stored).Error
	var cfg *pipeline.PipelineConfig
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		cfg = pipeline.DefaultInboundPipeline(mailbox.DomainID)
	case err != nil:
		return nil, fmt.Errorf("deferred: load inbound policy: %w", err)
	default:
		var filters []pipeline.FilterConfig
		if err := json.Unmarshal(stored.Filters, &filters); err != nil {
			return nil, fmt.Errorf("deferred: decode inbound policy: %w", err)
		}
		cfg = &pipeline.PipelineConfig{ID: stored.ID, DomainID: stored.DomainID, Direction: stored.Direction, Filters: filters, Active: stored.Active}
	}
	cfg, err = withAccountSieve(db, cfg, mailbox.ID)
	if err != nil {
		return nil, fmt.Errorf("deferred: load inbound account script: %w", err)
	}
	return cfg, nil
}
