package sendlimits

import (
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
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
