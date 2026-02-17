package models

import "time"

type Alias struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	DomainID           uint      `gorm:"not null;index" json:"domain_id"`
	SourceAddress      string    `gorm:"size:255;not null;index" json:"source_address"`
	DestinationAddress string    `gorm:"size:255;not null" json:"destination_address"`
	Active             bool      `gorm:"default:true" json:"active"`
	CreatedAt          time.Time `json:"created_at"`

	// Associations
	Domain Domain `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
}

func (Alias) TableName() string { return "aliases" }
