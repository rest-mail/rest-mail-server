package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadAuditAnchorEmail(t *testing.T) {
	for _, tc := range []struct {
		name, enabled, to string
		wantEnabled       bool
		wantTo            []string
		bad               bool
	}{
		{name: "default on", wantEnabled: true},
		{name: "off", enabled: "false"},
		{name: "one target", to: " anchors@example.test ", wantEnabled: true, wantTo: []string{"anchors@example.test"}},
		{name: "list", to: "a@example.test, b@example.test,a@example.test", wantEnabled: true, wantTo: []string{"a@example.test", "b@example.test"}},
		{name: "remote local part preserved", to: "AnchorBox@EXAMPLE.test", wantEnabled: true, wantTo: []string{"AnchorBox@example.test"}},
		{name: "international domain", to: "AnchorBox@bücher.test", wantEnabled: true, wantTo: []string{"AnchorBox@xn--bcher-kva.test"}},
		{name: "invalid address", to: "bad", bad: true},
		{name: "display name", to: "Someone <a@example.test>", bad: true},
		{name: "injection", to: "a@example.test\r\nBcc: bad@example.test", bad: true},
		{name: "invalid switch", enabled: "perhaps", bad: true},
		{name: "empty list item", to: "a@example.test,", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", "test")
			if tc.enabled != "" {
				t.Setenv("RESTMAIL_AUDIT_ANCHOR_EMAIL_ENABLED", tc.enabled)
			}
			t.Setenv("RESTMAIL_AUDIT_ANCHOR_EMAIL_TO", tc.to)
			cfg, err := Load()
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "RESTMAIL_AUDIT_ANCHOR_EMAIL") {
					t.Fatalf("invalid config accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AuditAnchorEmailEnabled != tc.wantEnabled || !reflect.DeepEqual(cfg.AuditAnchorEmailTo, tc.wantTo) {
				t.Fatalf("anchor config: %v %v", cfg.AuditAnchorEmailEnabled, cfg.AuditAnchorEmailTo)
			}
		})
	}
}

func TestLoadAuditAnchorHTTPS(t *testing.T) {
	for _, tc := range []struct {
		name, url, token string
		wantURL          string
		bad              bool
	}{
		{name: "off by default"},
		{name: "endpoint", url: " https://anchors.example.net/restmail ", token: " secret ", wantURL: "https://anchors.example.net/restmail"},
		{name: "plain http refused", url: "http://anchors.example.net/restmail", bad: true},
		{name: "no host", url: "https:///restmail", bad: true},
		{name: "credentials in URL", url: "https://user:pass@anchors.example.net/", bad: true},
		{name: "not a URL", url: "anchors.example.net", bad: true},
		{name: "token without endpoint", token: "secret", bad: true},
		{name: "token with line break", url: "https://anchors.example.net/", token: "a\r\nb", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", "test")
			t.Setenv("RESTMAIL_AUDIT_ANCHOR_HTTPS_URL", tc.url)
			t.Setenv("RESTMAIL_AUDIT_ANCHOR_HTTPS_TOKEN", tc.token)
			cfg, err := Load()
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "RESTMAIL_AUDIT_ANCHOR_HTTPS") {
					t.Fatalf("invalid config accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantToken := strings.TrimSpace(tc.token)
			if cfg.AuditAnchorHTTPSURL != tc.wantURL || cfg.AuditAnchorHTTPSToken != wantToken {
				t.Fatalf("anchor endpoint: %q %q", cfg.AuditAnchorHTTPSURL, cfg.AuditAnchorHTTPSToken)
			}
		})
	}
}

func TestLoadAuditAnchorS3(t *testing.T) {
	bucket := map[string]string{
		"RESTMAIL_AUDIT_ANCHOR_S3_BUCKET": "anchors", "RESTMAIL_AUDIT_ANCHOR_S3_ACCESS_KEY_ID": "AKIDANCHOR",
		"RESTMAIL_AUDIT_ANCHOR_S3_SECRET_ACCESS_KEY": "anchor-secret",
	}
	with := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range bucket {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want AuditAnchorS3Config
		bad  bool
	}{
		{name: "off by default"},
		{name: "aws defaults", env: bucket, want: AuditAnchorS3Config{Region: "us-east-1", Bucket: "anchors", AccessKeyID: "AKIDANCHOR", SecretAccessKey: "anchor-secret", Prefix: "audit-anchors/"}},
		{name: "custom store", env: with(map[string]string{"RESTMAIL_AUDIT_ANCHOR_S3_ENDPOINT": "https://s3.example.net", "RESTMAIL_AUDIT_ANCHOR_S3_REGION": "eu-1", "RESTMAIL_AUDIT_ANCHOR_S3_PREFIX": "mx1/anchors/", "RESTMAIL_AUDIT_ANCHOR_S3_FORCE_PATH_STYLE": "true"}),
			want: AuditAnchorS3Config{Endpoint: "https://s3.example.net", Region: "eu-1", Bucket: "anchors", AccessKeyID: "AKIDANCHOR", SecretAccessKey: "anchor-secret", Prefix: "mx1/anchors/", ForcePathStyle: true}},
		{name: "plain http refused", env: with(map[string]string{"RESTMAIL_AUDIT_ANCHOR_S3_ENDPOINT": "http://s3.example.net"}), bad: true},
		{name: "endpoint with path", env: with(map[string]string{"RESTMAIL_AUDIT_ANCHOR_S3_ENDPOINT": "https://s3.example.net/anchors"}), bad: true},
		{name: "credentials in endpoint", env: with(map[string]string{"RESTMAIL_AUDIT_ANCHOR_S3_ENDPOINT": "https://user:pass@s3.example.net"}), bad: true},
		{name: "missing secret", env: with(map[string]string{"RESTMAIL_AUDIT_ANCHOR_S3_SECRET_ACCESS_KEY": ""}), bad: true},
		{name: "settings without bucket", env: map[string]string{"RESTMAIL_AUDIT_ANCHOR_S3_ACCESS_KEY_ID": "AKIDANCHOR"}, bad: true},
		{name: "absolute prefix", env: with(map[string]string{"RESTMAIL_AUDIT_ANCHOR_S3_PREFIX": "/anchors/"}), bad: true},
		{name: "invalid path style", env: with(map[string]string{"RESTMAIL_AUDIT_ANCHOR_S3_FORCE_PATH_STYLE": "perhaps"}), bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("ENVIRONMENT", "test")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			if tc.bad {
				if err == nil || !strings.Contains(err.Error(), "RESTMAIL_AUDIT_ANCHOR_S3") {
					t.Fatalf("invalid config accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.AuditAnchorS3, tc.want) {
				t.Fatalf("anchor object storage: %+v", cfg.AuditAnchorS3)
			}
		})
	}
}
