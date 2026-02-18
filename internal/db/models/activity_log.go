package models

import (
	"encoding/json"
	"time"
)

// ActivityLog records admin and system actions for auditing.
type ActivityLog struct {
	ID           uint            `gorm:"primaryKey" json:"id"`
	Actor        string          `gorm:"size:255;not null;index" json:"actor"`          // email or "system"
	Action       string          `gorm:"size:50;not null;index" json:"action"`          // "create", "update", "delete", "login", "ban", etc.
	ResourceType string          `gorm:"size:50;not null;index" json:"resource_type"`   // "domain", "mailbox", "pipeline", "ban", etc.
	ResourceID   *uint           `json:"resource_id,omitempty"`
	Detail       string          `gorm:"type:text" json:"detail,omitempty"`
	Metadata     json.RawMessage `gorm:"type:jsonb" json:"metadata,omitempty"`
	IP           string          `gorm:"size:45" json:"ip,omitempty"`
	CreatedAt    time.Time       `gorm:"index" json:"created_at"`
}

func (ActivityLog) TableName() string { return "activity_logs" }
