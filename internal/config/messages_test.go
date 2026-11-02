package config

import "testing"

func TestMessageStorageConfig(t *testing.T) {
	s3 := AttachmentStorageConfig{Type: "s3", S3Prefix: "attachments/"}
	c, err := loadMessageStorage(s3)
	if err != nil || c.Directory != "/messages" || c.S3Prefix != "messages/" {
		t.Fatalf("defaults: %+v, %v", c, err)
	}
	for name, tc := range map[string]struct {
		key, value string
		attachment AttachmentStorageConfig
	}{
		"relative directory":      {"MESSAGE_STORAGE_DIR", "messages", s3},
		"bad prefix":              {"MESSAGE_S3_PREFIX", "../messages/", s3},
		"inside the attachments":  {"MESSAGE_S3_PREFIX", "attachments/messages/", s3},
		"holding the attachments": {"MESSAGE_S3_PREFIX", "attachments/", s3},
		"above the attachments":   {"MESSAGE_S3_PREFIX", "a/", AttachmentStorageConfig{Type: "s3", S3Prefix: "a/attachments/"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := loadMessageStorage(tc.attachment); err == nil {
				t.Fatal("it was taken")
			}
		})
	}
}
