package db

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// MigrateFolderModSeqs creates folder_mod_seqs and folder_vanished_uids and the
// triggers that keep them and messages.mod_seq (RFC 7162, go-imap#162). Every
// change an IMAP client can see in a folder raises that folder's mod-sequence
// in the same transaction: a message stored or moved in gets the new value as
// its mod_seq, so does a message whose flags change, and a message that leaves
// (a move, a permanent delete, or a soft delete that hides it) is recorded in
// folder_vanished_uids with the source folder's new value. Writes from any path
// are covered without each remembering to do it.
func MigrateFolderModSeqs(database *gorm.DB) error {
	for _, model := range []any{&models.FolderModSeq{}, &models.FolderVanishedUID{}} {
		if err := database.AutoMigrate(model); err != nil {
			return err
		}
		if !database.Migrator().HasConstraint(model, "Mailbox") {
			if err := database.Migrator().CreateConstraint(model, "Mailbox"); err != nil {
				return err
			}
		}
	}
	return database.Exec(folderModSeqsSQL).Error
}

// folderModSeqsSQL raises a folder's mod-sequence and never lowers it. The
// mailbox check keeps a mailbox's own deletion, which cascades to its
// messages, from writing rows for a mailbox that is going away.
const folderModSeqsSQL = `
CREATE OR REPLACE FUNCTION restmail_next_mod_seq(mb bigint, f text) RETURNS bigint AS $$
DECLARE
    v bigint;
BEGIN
    -- Rows stored before mod-sequences existed hold 1, so a folder starts at 2.
    INSERT INTO folder_mod_seqs (mailbox_id, folder, highest_mod_seq)
    VALUES (mb, f, 2)
    ON CONFLICT (mailbox_id, folder) DO UPDATE
        SET highest_mod_seq = folder_mod_seqs.highest_mod_seq + 1
    RETURNING highest_mod_seq INTO v;
    RETURN v;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION restmail_message_vanished(mb bigint, f text, id bigint) RETURNS void AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM mailboxes WHERE mailboxes.id = mb) THEN
        RETURN;
    END IF;
    INSERT INTO folder_vanished_uids (mailbox_id, folder, uid, mod_seq)
    VALUES (mb, f, id, restmail_next_mod_seq(mb, f))
    ON CONFLICT (mailbox_id, folder, uid) DO UPDATE SET mod_seq = EXCLUDED.mod_seq;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION restmail_message_mod_seq() RETURNS trigger AS $$
DECLARE
    moved boolean;
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF NOT OLD.is_deleted THEN
            PERFORM restmail_message_vanished(OLD.mailbox_id, OLD.folder, OLD.id);
        END IF;
        RETURN OLD;
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.mod_seq := restmail_next_mod_seq(NEW.mailbox_id, NEW.folder);
        DELETE FROM folder_vanished_uids
        WHERE mailbox_id = NEW.mailbox_id AND folder = NEW.folder AND uid = NEW.id;
        RETURN NEW;
    END IF;
    moved := OLD.folder IS DISTINCT FROM NEW.folder OR OLD.mailbox_id IS DISTINCT FROM NEW.mailbox_id;
    -- A soft delete hides the message from IMAP, which sees it leave.
    IF (moved OR NEW.is_deleted) AND NOT OLD.is_deleted THEN
        PERFORM restmail_message_vanished(OLD.mailbox_id, OLD.folder, OLD.id);
    END IF;
    IF moved OR (OLD.is_deleted AND NOT NEW.is_deleted) THEN
        DELETE FROM folder_vanished_uids
        WHERE mailbox_id = NEW.mailbox_id AND folder = NEW.folder AND uid = NEW.id;
    END IF;
    IF NEW.is_deleted AND NOT moved THEN
        -- Hidden from IMAP: no client sees the row change.
        NEW.mod_seq := OLD.mod_seq;
    ELSIF moved
        OR OLD.is_deleted IS DISTINCT FROM NEW.is_deleted
        OR OLD.is_read IS DISTINCT FROM NEW.is_read
        OR OLD.is_answered IS DISTINCT FROM NEW.is_answered
        OR OLD.is_flagged IS DISTINCT FROM NEW.is_flagged
        OR OLD.is_starred IS DISTINCT FROM NEW.is_starred
        OR OLD.is_draft IS DISTINCT FROM NEW.is_draft
        OR OLD.imap_deleted IS DISTINCT FROM NEW.imap_deleted
        OR OLD.keywords::jsonb IS DISTINCT FROM NEW.keywords::jsonb THEN
        NEW.mod_seq := restmail_next_mod_seq(NEW.mailbox_id, NEW.folder);
    ELSE
        -- Nothing a client sees changed: the mod-sequence stays (RFC 7162
        -- §3.1.11), whatever the statement wrote to it.
        NEW.mod_seq := OLD.mod_seq;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER restmail_message_mod_seq_write
BEFORE INSERT OR UPDATE ON messages FOR EACH ROW EXECUTE FUNCTION restmail_message_mod_seq();
CREATE OR REPLACE TRIGGER restmail_message_mod_seq_delete
AFTER DELETE ON messages FOR EACH ROW EXECUTE FUNCTION restmail_message_mod_seq();`
