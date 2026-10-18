package config

import (
	"strings"
	"testing"
)

func TestAttachmentStorageConfiguration(t *testing.T) {
	valid := AttachmentStorageConfig{Type: "s3", S3Bucket: "mail-attachments", S3Region: "garage", S3Endpoint: "http://garage-s3.garage.svc:3900", S3AccessKeyID: "access", S3SecretAccessKey: "private-value", S3Prefix: "attachments/", S3ForcePathStyle: true}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*AttachmentStorageConfig){
		func(c *AttachmentStorageConfig) { c.Type = "unknown" },
		func(c *AttachmentStorageConfig) { c.S3Bucket = "" },
		func(c *AttachmentStorageConfig) { c.S3SecretAccessKey = "" },
		func(c *AttachmentStorageConfig) { c.S3Endpoint = "http://user:private-value@garage" },
		func(c *AttachmentStorageConfig) { c.S3Endpoint = "file:///attachments" },
		func(c *AttachmentStorageConfig) { c.S3Endpoint = "http://garage?token=private-value" },
		func(c *AttachmentStorageConfig) { c.S3Prefix = "../attachments/" },
		func(c *AttachmentStorageConfig) { c.Type = "filesystem" },
	} {
		c := valid
		mutate(&c)
		if err := c.Validate(); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("invalid configuration accepted or secret exposed: %v", err)
		}
	}
}
