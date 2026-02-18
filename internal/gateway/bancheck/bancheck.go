package bancheck

import (
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	"gorm.io/gorm"
)

// Wire sets up DB-backed ban checking on the limiter for the given protocol.
func Wire(limiter *connlimiter.Limiter, database *gorm.DB, protocol string) {
	if database == nil {
		return
	}
	limiter.SetBanChecker(func(ip, proto string) bool {
		var count int64
		database.Model(&models.Ban{}).
			Where("ip = ? AND (protocol = ? OR protocol = 'all') AND (expires_at IS NULL OR expires_at > ?)",
				ip, proto, time.Now()).
			Count(&count)
		return count > 0
	}, protocol)
}
