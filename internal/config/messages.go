package config

import (
	"fmt"
	"strings"
)

// MessageStorageConfig is where the original bytes of messages are kept: the attachments' bucket,
// under a prefix of their own.
type MessageStorageConfig struct {
	// S3Prefix is where messages sit in the attachments' bucket.
	S3Prefix string
	// SpoolDir holds a message while it is uploaded to the bucket. Empty is the system's
	// temporary directory.
	SpoolDir string
}

func loadMessageStorage(attachments AttachmentStorageConfig) (MessageStorageConfig, error) {
	c := MessageStorageConfig{
		S3Prefix: getEnv("MESSAGE_S3_PREFIX", "messages/"),
		SpoolDir: getEnv("MESSAGE_SPOOL_DIR", ""),
	}
	if !validObjectPrefix(c.S3Prefix) {
		return c, fmt.Errorf("MESSAGE_S3_PREFIX must be a relative object prefix ending in /, using letters, digits, /, _ or -")
	}
	// The attachment cleaner removes whatever under its prefix no attachment refers to.
	if attachments.Type == "s3" && (strings.HasPrefix(c.S3Prefix, attachments.S3Prefix) || strings.HasPrefix(attachments.S3Prefix, c.S3Prefix)) {
		return c, fmt.Errorf("MESSAGE_S3_PREFIX must not overlap ATTACHMENT_S3_PREFIX")
	}
	return c, nil
}
