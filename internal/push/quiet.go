package push

import (
	"context"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/quiethours"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const quietDispatchLock int64 = 0x726d7175696574

func quietSetting(account models.Mailbox) quiethours.Setting {
	return quiethours.Setting{Enabled: account.QuietHoursEnabled, Start: account.QuietHoursStart, End: account.QuietHoursEnd, TimeZone: account.QuietHoursTimeZone}
}

func (s *Store) hold(mailbox uint, now time.Time) (bool, error) {
	var account models.Mailbox
	if err := s.db.Select("quiet_hours_enabled", "quiet_hours_start", "quiet_hours_end", "quiet_hours_time_zone").First(&account, mailbox).Error; err != nil {
		return true, err
	}
	setting := quietSetting(account)
	if !setting.Until(now).IsZero() {
		row := models.QuietNotification{MailboxID: mailbox, ArrivedAt: now, Start: setting.Start, End: setting.End, TimeZone: setting.TimeZone}
		return true, s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	}
	var count int64
	err := s.db.Model(&models.QuietNotification{}).Where("mailbox_id = ?", mailbox).Count(&count).Error
	return count > 0, err
}

func (s *Store) dispatchQuiet(ctx context.Context, now time.Time, send func(context.Context, target, int64, int64)) (time.Time, error) {
	var next time.Time
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", quietDispatchLock).Scan(&locked).Error; err != nil || !locked {
			return err
		}
		var accounts []uint
		if err := tx.Model(&models.QuietNotification{}).Distinct("mailbox_id").Order("mailbox_id").Pluck("mailbox_id", &accounts).Error; err != nil {
			return err
		}
		store := NewStore(tx)
		for _, mailbox := range accounts {
			var rows []models.QuietNotification
			if err := tx.Where("mailbox_id = ?", mailbox).Find(&rows).Error; err != nil {
				return err
			}
			var account models.Mailbox
			if err := tx.First(&account, mailbox).Error; err != nil {
				return err
			}
			until := quietSetting(account).Until(now)
			var arrived int64
			ids := make([]uint, 0, len(rows))
			latest := make(map[quiethours.Setting]time.Time)
			for _, row := range rows {
				ids = append(ids, row.MessageID)
				setting := quiethours.Setting{Enabled: true, Start: row.Start, End: row.End, TimeZone: row.TimeZone}
				if row.ArrivedAt.After(latest[setting]) {
					latest[setting] = row.ArrivedAt
				}
				if row.MessageID != 0 {
					arrived++
				}
			}
			for setting, at := range latest {
				if end := setting.Until(at); end.After(until) {
					until = end
				}
			}
			if until.After(now) {
				if next.IsZero() || until.Before(next) {
					next = until
				}
				continue
			}
			targets, err := store.targets(mailbox, now)
			if err != nil {
				return err
			}
			unread, err := store.unread(mailbox)
			if err != nil {
				return err
			}
			for _, device := range targets {
				send(ctx, device, unread, arrived)
			}
			for offset := 0; offset < len(ids); offset += 1000 {
				if err := tx.Where("mailbox_id = ? AND message_id IN ?", mailbox, ids[offset:min(offset+1000, len(ids))]).Delete(&models.QuietNotification{}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	return next, err
}
