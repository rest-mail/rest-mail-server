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
