package handlers

import (
	"encoding/json"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// withNativeRules runs the native snapshot after the domain's content filters.
// The enabled Sieve step remains the domain operator's account-filter switch.
func withNativeRules(cfg *pipeline.PipelineConfig, rows []models.MailRule) (*pipeline.PipelineConfig, error) {
	encoded, err := json.Marshal(nativeSnapshots(rows))
	if err != nil {
		return nil, err
	}
	last := -1
	for i, step := range cfg.Filters {
		if step.Name == "sieve" && step.Enabled {
			last = i
		}
	}
	if last < 0 {
		return cfg, nil
	}
	out := *cfg
	out.Filters = make([]pipeline.FilterConfig, 0, len(cfg.Filters)+1)
	out.Filters = append(out.Filters, cfg.Filters...)
	out.Filters = append(out.Filters, pipeline.FilterConfig{Name: "mail_rules", Type: pipeline.FilterTypeTransform, Enabled: true, Config: encoded, AccountRuleMailboxID: rows[0].MailboxID})
	return &out, nil
}
