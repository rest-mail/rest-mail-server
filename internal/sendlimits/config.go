package sendlimits

import (
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func FromConfig(cfg *config.SendLimitConfig) *Service {
	if cfg == nil {
		return nil
	}
	convert := func(v config.SendLimitDefaults) models.SendLimitValues {
		return models.SendLimitValues{MessagesPerHour: v.MessagesPerHour, MessagesPerDay: v.MessagesPerDay, RecipientsPerHour: v.RecipientsPerHour, RecipientsPerDay: v.RecipientsPerDay}
	}
	return &Service{Defaults: convert(cfg.Account), Domain: convert(cfg.Domain), Server: convert(cfg.Server), PerMinute: cfg.PerMinute}
}
