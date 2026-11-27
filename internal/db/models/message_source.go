package models

import "time"

const (
	MessageSourceStaged    = "staged"
	MessageSourcePublished = "published"
	MessageSourceDeleting  = "deleting"
)

// MessageSource tracks one immutable RFC 5322 object and the number of
// committed owners of that object. ReapAt is the staging lease deadline, the
// time the last owner released it, or the next deletion retry.
type MessageSource struct {
	Ref            string     `gorm:"primaryKey"`
	Size           int64      `gorm:"not null;check:message_source_size,size >= 0"`
	Checksum       string     `gorm:"size:64;not null"`
	HeaderMetadata []byte     `gorm:"check:message_source_header_size,octet_length(header_metadata) <= 65536"`
	ReferenceCount int64      `gorm:"not null;check:message_source_refs,reference_count >= 0"`
	ReapAt         *time.Time `gorm:"index:,where:reap_at IS NOT NULL"`
	State          string     `gorm:"size:9;not null;check:message_source_state,state IN ('staged','published','deleting') AND ((state = 'published' AND reference_count > 0) = (reap_at IS NULL)) AND (state = 'published' OR reference_count = 0) AND (state <> 'published' OR length(checksum) = 64)"`
}
