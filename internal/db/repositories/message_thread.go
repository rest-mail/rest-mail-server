package repositories

import (
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
)

func FindMessageParent(db *gorm.DB, mailboxID uint, inReplyTo, references string) (*models.Message, error) {
	candidates := []string{inReplyTo}
	refs := strings.Fields(references)
	for index := len(refs) - 1; index >= 0; index-- {
		candidates = append(candidates, refs[index])
	}
	seen := make(map[string]bool, len(candidates))
	ids := make([]string, 0, len(candidates)*2)
	ordered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		id := rmail.CanonicalID(candidate)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id, strings.Trim(id, "<>"))
		ordered = append(ordered, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var parents []models.Message
	if err := db.Select("message_id", "in_reply_to", "references", "thread_id").
		Where("mailbox_id = ? AND message_id IN ?", mailboxID, ids).
		Order("id").Find(&parents).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]*models.Message, len(parents))
	for index := range parents {
		id := rmail.CanonicalID(parents[index].MsgID)
		if byID[id] == nil {
			byID[id] = &parents[index]
		}
	}
	for _, id := range ordered {
		if parent := byID[id]; parent != nil {
			return parent, nil
		}
	}
	return nil, nil
}

func ResolveThreadID(db *gorm.DB, mailboxID uint, messageID, inReplyTo, references string) (string, error) {
	parent, err := FindMessageParent(db, mailboxID, inReplyTo, references)
	if err != nil {
		return "", err
	}
	if parent != nil {
		if parent.ThreadID != "" {
			return rmail.CanonicalID(parent.ThreadID), nil
		}
		return rmail.HeaderThreadID(parent.MsgID, parent.InReplyTo, parent.References), nil
	}
	return rmail.HeaderThreadID(messageID, inReplyTo, references), nil
}

func ReferencesForReply(db *gorm.DB, mailboxID uint, inReplyTo string) (string, error) {
	parent, err := FindMessageParent(db, mailboxID, inReplyTo, "")
	if err != nil {
		return "", err
	}
	if parent == nil {
		return rmail.CanonicalID(inReplyTo), nil
	}
	return rmail.ReplyReferences(parent.MsgID, parent.InReplyTo, parent.References), nil
}
