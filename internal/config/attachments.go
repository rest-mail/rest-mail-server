package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// AttachmentStorageConfig is deployment configuration, never a pipeline's
// sender-controlled data. Credentials belong in environment secrets.
type AttachmentStorageConfig struct {
	Type              string
	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3ForcePathStyle  bool
	S3Prefix          string
}

func loadAttachmentStorage() (AttachmentStorageConfig, error) {
	c := AttachmentStorageConfig{
		Type:              getEnv("ATTACHMENT_STORAGE_TYPE", "filesystem"),
		S3Endpoint:        getEnv("ATTACHMENT_S3_ENDPOINT", ""),
		S3Region:          getEnv("ATTACHMENT_S3_REGION", "us-east-1"),
		S3Bucket:          getEnv("ATTACHMENT_S3_BUCKET", ""),
		S3AccessKeyID:     getEnv("ATTACHMENT_S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: getEnv("ATTACHMENT_S3_SECRET_ACCESS_KEY", ""),
		S3Prefix:          getEnv("ATTACHMENT_S3_PREFIX", "attachments/"),
	}
	pathStyle, err := strconv.ParseBool(getEnv("ATTACHMENT_S3_FORCE_PATH_STYLE", "true"))
	if err != nil {
		return c, fmt.Errorf("ATTACHMENT_S3_FORCE_PATH_STYLE must be true or false")
	}
	c.S3ForcePathStyle = pathStyle
	return c, c.Validate()
}

func (c AttachmentStorageConfig) Validate() error {
	if c.Type == "filesystem" || c.Type == "" {
		if c.S3Endpoint != "" || c.S3Bucket != "" || c.S3AccessKeyID != "" || c.S3SecretAccessKey != "" {
			return fmt.Errorf("S3 attachment settings require ATTACHMENT_STORAGE_TYPE=s3")
		}
		return nil
	}
	if c.Type != "s3" {
		return fmt.Errorf("ATTACHMENT_STORAGE_TYPE must be filesystem or s3")
	}
	if c.S3Bucket == "" || c.S3Region == "" || c.S3AccessKeyID == "" || c.S3SecretAccessKey == "" {
		return fmt.Errorf("S3 attachment storage requires bucket, region, access key ID and secret access key")
	}
	if c.S3Endpoint != "" {
		u, err := url.Parse(c.S3Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("ATTACHMENT_S3_ENDPOINT must be an HTTP(S) origin without credentials, path or query")
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_-]*/$`).MatchString(c.S3Prefix) || strings.Contains(c.S3Prefix, "//") {
		return fmt.Errorf("ATTACHMENT_S3_PREFIX must be a relative object prefix ending in /, using letters, digits, /, _ or -")
	}
	return nil
}
