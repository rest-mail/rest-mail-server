package repositories

import (
	"encoding/json"
	"fmt"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// RecordLocalRefusals keeps, on a message's Sent copy, the recipients on this server that
// refused it, for its sender to read in the message's recipient status. No refusals
// writes nothing.
func RecordLocalRefusals(db *gorm.DB, messageID uint, refusals []models.LocalRefusal) error {
	if len(refusals) == 0 {
		return nil
	}
	stored, err := json.Marshal(refusals)
	if err != nil {
		return fmt.Errorf("encode local refusals: %w", err)
	}
	if err := db.Model(&models.Message{}).Where("id = ?", messageID).UpdateColumn("local_refusals", models.JSONB(stored)).Error; err != nil {
		return fmt.Errorf("record local refusals: %w", err)
	}
	return nil
}
