package dnsbl

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfigJSON = `{
  "enabled": true,
  "ips": ["192.0.2.99", "2001:db8::25"],
  "start_delay": "5m",
  "interval": "1h",
  "timeout": "10s",
  "providers": [
    {"name": "example", "zone": "bl.example.org",
     "listed": ["127.0.0.2", "127.0.0.4/30"], "errors": ["127.255.255.0/24"], "ipv6": true}
  ]
}`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dnsbl.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigFile(t *testing.T) {
	cfg, enabled, err := LoadConfigFile(writeConfig(t, validConfigJSON))
	if err != nil || !enabled {
		t.Fatalf("LoadConfigFile = enabled %v, %v; want enabled", enabled, err)
	}
	if cfg.StartDelay != 5*time.Minute || cfg.Interval != time.Hour || cfg.Timeout != 10*time.Second {
		t.Fatalf("cadence = %s/%s/%s", cfg.StartDelay, cfg.Interval, cfg.Timeout)
	}
	if len(cfg.IPs) != 2 || cfg.IPs[0] != netip.MustParseAddr("192.0.2.99") {
		t.Fatalf("ips = %v", cfg.IPs)
	}
	want := Provider{
		Name: "example", Zone: "bl.example.org", IPv6: true,
		Listed: []netip.Prefix{netip.MustParsePrefix("127.0.0.2/32"), netip.MustParsePrefix("127.0.0.4/30")},
		Errors: []netip.Prefix{netip.MustParsePrefix("127.255.255.0/24")},
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != want.Name || cfg.Providers[0].Zone != want.Zone ||
		!cfg.Providers[0].IPv6 || len(cfg.Providers[0].Listed) != 2 || cfg.Providers[0].Listed[0] != want.Listed[0] ||
		cfg.Providers[0].Listed[1] != want.Listed[1] || cfg.Providers[0].Errors[0] != want.Errors[0] {
		t.Fatalf("providers = %+v, want %+v", cfg.Providers, want)
	}
	cfg.Observer = &recorder{}
	if _, err := NewMonitor(checkFunc(nil), cfg); err != nil {
		t.Fatalf("the loaded config does not validate: %v", err)
	}
}

func TestLoadConfigFileDisabled(t *testing.T) {
	for name, body := range map[string]string{
		"explicitly off":  `{"enabled": false}`,
		"off by omission": `{}`,
		// Off, the rest is not validated: an operator can stage a config.
		"off with a staged config": strings.Replace(validConfigJSON, `"enabled": true`, `"enabled": false`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, enabled, err := LoadConfigFile(writeConfig(t, body))
			if err != nil || enabled {
				t.Fatalf("LoadConfigFile = enabled %v, %v; want off", enabled, err)
			}
			if len(cfg.IPs) != 0 || len(cfg.Providers) != 0 {
				t.Fatalf("an off config returned %+v", cfg)
			}
		})
	}
}

func TestLoadConfigFileRejects(t *testing.T) {
	without := func(field string) string {
		var out []string
		for _, line := range strings.Split(validConfigJSON, "\n") {
			if !strings.Contains(line, `"`+field+`"`) {
				out = append(out, line)
			}
		}
		return strings.Join(out, "\n")
	}
	for name, body := range map[string]string{
		"not json":            `enabled: true`,
		"unknown field":       strings.Replace(validConfigJSON, `"enabled": true`, `"enabled": true, "preset": "spamhaus"`, 1),
		"unknown off field":   `{"enabled": false, "api_key": "x"}`,
		"trailing data":       validConfigJSON + `{}`,
		"no start delay":      without("start_delay"),
		"no interval":         without("interval"),
		"no timeout":          without("timeout"),
		"bad duration":        strings.Replace(validConfigJSON, `"1h"`, `"3600"`, 1),
		"bad ip":              strings.Replace(validConfigJSON, `"192.0.2.99"`, `"mail.example.org"`, 1),
		"bad code":            strings.Replace(validConfigJSON, `"127.0.0.2"`, `"listed"`, 1),
		"code with host bits": strings.Replace(validConfigJSON, `"127.0.0.4/30"`, `"127.0.0.5/30"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := LoadConfigFile(writeConfig(t, body)); !errors.Is(err, ErrInvalidConfigFile) {
				t.Fatalf("LoadConfigFile = %v, want ErrInvalidConfigFile", err)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if _, _, err := LoadConfigFile(filepath.Join(t.TempDir(), "absent.json")); !errors.Is(err, ErrInvalidConfigFile) {
			t.Fatalf("LoadConfigFile = %v, want ErrInvalidConfigFile", err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		if _, _, err := LoadConfigFile(writeConfig(t, validConfigJSON+strings.Repeat(" ", maxConfigFile))); !errors.Is(err, ErrInvalidConfigFile) {
			t.Fatalf("LoadConfigFile = %v, want ErrInvalidConfigFile", err)
		}
	})
}
