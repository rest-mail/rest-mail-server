package config

import (
	"strings"
	"testing"
)

// The inbound structure limits default to the established values and take an admin's positive
// value; a malformed, zero or negative one is a startup error (#1647, #1641).
func TestLoad_InboundLimits(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENVIRONMENT", "development")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MIMELimits(); got.MaxReceived != 50 || got.MaxNestingDepth != 100 || got.MaxParts != 1000 {
		t.Fatalf("defaults = %+v, want 50/100/1000", got)
	}
	t.Setenv("SMTP_MAX_RECEIVED_HEADERS", "30")
	t.Setenv("MIME_MAX_NESTING_DEPTH", "20")
	t.Setenv("MIME_MAX_PARTS", "500")
	if cfg, err = Load(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.MIMELimits(); got.MaxReceived != 30 || got.MaxNestingDepth != 20 || got.MaxParts != 500 {
		t.Fatalf("configured = %+v, want 30/20/500", got)
	}
	for _, key := range []string{"SMTP_MAX_RECEIVED_HEADERS", "MIME_MAX_NESTING_DEPTH", "MIME_MAX_PARTS"} {
		for _, bad := range []string{"0", "-1", "many"} {
			t.Run(key+"="+bad, func(t *testing.T) {
				t.Setenv(key, bad)
				if _, err := Load(); err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("Load() = %v, want an error naming %s", err, key)
				}
			})
		}
	}
}
