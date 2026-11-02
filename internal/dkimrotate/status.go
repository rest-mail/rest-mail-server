package dkimrotate

import (
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func Status(keys []models.DKIMRotationKey) (string, *time.Time) {
	var previous *models.DKIMRotationKey
	for index := range keys {
		key := &keys[index]
		switch key.Slot {
		case models.DKIMRotationStaged:
			if key.PublishedAt == nil {
				return "awaiting_publication", &key.CreatedAt
			}
			return "waiting_ttl", key.PublishedAt
		case models.DKIMRotationPrevious:
			previous = key
		}
	}
	if previous != nil {
		if previous.RetireAfter != nil {
			switched := previous.RetireAfter.Add(-time.Duration(previous.TTLSeconds) * time.Second)
			return "retiring", &switched
		}
		return "retiring", nil
	}
	return "stable", nil
}
