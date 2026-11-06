package db

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// MigrateFolderUIDMarks creates folder_uid_marks and the triggers that keep it:
// a message inserted into a folder, or moved into one by changing its folder
// or mailbox, raises that folder's mark to the message's ID in the same
// transaction. Writes from any path (delivery, APPEND, copy, import, rename)
// are covered without each remembering to do it (#804).
func MigrateFolderUIDMarks(database *gorm.DB) error {
	if err := database.AutoMigrate(&models.FolderUIDMark{}); err != nil {
		return err
	}
	if !database.Migrator().HasConstraint(&models.FolderUIDMark{}, "Mailbox") {
		if err := database.Migrator().CreateConstraint(&models.FolderUIDMark{}, "Mailbox"); err != nil {
			return err
		}
	}
	return database.Exec(folderUIDMarksSQL).Error
}

// folderUIDMarksSQL raises a mark and never lowers it. The UPDATE trigger
// fires only when the row lands in another folder, so a flag change does not
// touch the mark.
const folderUIDMarksSQL = `
CREATE OR REPLACE FUNCTION restmail_raise_folder_uid_mark() RETURNS trigger AS $$
BEGIN
    INSERT INTO folder_uid_marks (mailbox_id, folder, highest_uid)
    VALUES (NEW.mailbox_id, NEW.folder, NEW.id)
    ON CONFLICT (mailbox_id, folder) DO UPDATE
        SET highest_uid = GREATEST(folder_uid_marks.highest_uid, EXCLUDED.highest_uid);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER restmail_folder_uid_mark_insert
AFTER INSERT ON messages FOR EACH ROW EXECUTE FUNCTION restmail_raise_folder_uid_mark();
CREATE OR REPLACE TRIGGER restmail_folder_uid_mark_move
AFTER UPDATE OF folder, mailbox_id ON messages FOR EACH ROW
WHEN (OLD.folder IS DISTINCT FROM NEW.folder OR OLD.mailbox_id IS DISTINCT FROM NEW.mailbox_id)
EXECUTE FUNCTION restmail_raise_folder_uid_mark();`
