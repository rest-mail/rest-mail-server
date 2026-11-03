package db

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func MigrateQuietNotifications(database *gorm.DB) error {
	if err := database.AutoMigrate(&models.QuietNotification{}); err != nil {
		return err
	}
	if !database.Migrator().HasConstraint(&models.QuietNotification{}, "Mailbox") {
		if err := database.Migrator().CreateConstraint(&models.QuietNotification{}, "Mailbox"); err != nil {
			return err
		}
	}
	return database.Exec(quietNotificationsSQL).Error
}

const quietNotificationsSQL = `
CREATE OR REPLACE FUNCTION restmail_hold_quiet_notification() RETURNS trigger AS $$
DECLARE
    account mailboxes%ROWTYPE;
    local_clock text;
BEGIN
    IF NEW.folder <> 'INBOX' OR NEW.is_read OR NEW.is_draft OR NEW.is_deleted THEN
        RETURN NEW;
    END IF;
    SELECT * INTO account FROM mailboxes WHERE id = NEW.mailbox_id;
    IF NOT account.quiet_hours_enabled THEN
        RETURN NEW;
    END IF;
    local_clock := to_char(NEW.created_at AT TIME ZONE account.quiet_hours_time_zone, 'HH24:MI');
    IF (account.quiet_hours_start < account.quiet_hours_end AND local_clock >= account.quiet_hours_start AND local_clock < account.quiet_hours_end)
       OR (account.quiet_hours_start > account.quiet_hours_end AND (local_clock >= account.quiet_hours_start OR local_clock < account.quiet_hours_end)) THEN
        INSERT INTO quiet_notifications (mailbox_id, message_id, arrived_at, start, "end", time_zone)
        VALUES (NEW.mailbox_id, NEW.id, NEW.created_at, account.quiet_hours_start, account.quiet_hours_end, account.quiet_hours_time_zone)
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER restmail_hold_quiet_notification
AFTER INSERT ON messages FOR EACH ROW EXECUTE FUNCTION restmail_hold_quiet_notification();`
