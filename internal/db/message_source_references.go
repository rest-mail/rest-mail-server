package db

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// Every path that permanently removes a message releases its reference in the
// same transaction, including account purges and bulk deletes.
func migrateMessageSourceReferences(db *gorm.DB) error {
	if !db.Migrator().HasConstraint(&models.Message{}, "SourceObject") {
		if err := db.Migrator().CreateConstraint(&models.Message{}, "SourceObject"); err != nil {
			return err
		}
	}
	return db.Exec(`
CREATE OR REPLACE FUNCTION restmail_release_message_source() RETURNS trigger AS $$
BEGIN
    UPDATE message_sources
    SET reference_count = reference_count - 1,
        reap_at = CASE WHEN reference_count = 1 THEN clock_timestamp() ELSE NULL END
    WHERE ref = OLD.source_ref AND state = 'published' AND reference_count > 0;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'message source % has no reference to release', OLD.source_ref;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER restmail_message_source_delete
AFTER DELETE ON messages FOR EACH ROW
WHEN (OLD.source_ref IS NOT NULL)
EXECUTE FUNCTION restmail_release_message_source();

CREATE OR REPLACE TRIGGER restmail_message_source_replace
AFTER UPDATE OF source_ref ON messages FOR EACH ROW
WHEN (OLD.source_ref IS NOT NULL AND OLD.source_ref IS DISTINCT FROM NEW.source_ref)
EXECUTE FUNCTION restmail_release_message_source();
`).Error
}
