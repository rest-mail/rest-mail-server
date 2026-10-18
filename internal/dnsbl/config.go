package dnsbl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"time"
)

// ErrInvalidConfigFile is returned for a monitor config file that cannot be
// read or does not say everything the Monitor needs.
var ErrInvalidConfigFile = errors.New("dnsbl: invalid monitor config file")

// maxConfigFile bounds what LoadConfigFile reads; a real file is a few KiB.
const maxConfigFile = 1 << 20

// fileConfig is the operator's monitor config file. Nothing has a default:
// with "enabled": true every field but a provider's "errors" and "ipv6" must
// be given, and no provider is built in.
//
//	{
//	  "enabled": true,
//	  "ips": ["203.0.113.10"],
//	  "start_delay": "5m",
//	  "interval": "1h",
//	  "timeout": "10s",
//	  "providers": [
//	    {"name": "example", "zone": "bl.example.org",
//	     "listed": ["127.0.0.2/32"], "errors": ["127.255.255.0/24"], "ipv6": false}
//	  ]
//	}
type fileConfig struct {
	Enabled    bool           `json:"enabled"`
	IPs        []string       `json:"ips"`
	StartDelay *string        `json:"start_delay"`
	Interval   *string        `json:"interval"`
	Timeout    *string        `json:"timeout"`
	Providers  []fileProvider `json:"providers"`
}

type fileProvider struct {
	Name   string   `json:"name"`
	Zone   string   `json:"zone"`
	Listed []string `json:"listed"`
	Errors []string `json:"errors"`
	IPv6   bool     `json:"ipv6"`
}

// LoadConfigFile reads the monitor config at path. It reports enabled=false,
// and nothing else, when the file says "enabled": false; the file must still
// be well-formed JSON with no unknown fields. When enabled, the returned
// config has everything but the Observer; NewMonitor then validates it.
func LoadConfigFile(path string) (cfg MonitorConfig, enabled bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return MonitorConfig{}, false, fmt.Errorf("%w: %v", ErrInvalidConfigFile, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFile+1))
	if err != nil {
		return MonitorConfig{}, false, fmt.Errorf("%w: %v", ErrInvalidConfigFile, err)
	}
	if len(data) > maxConfigFile {
		return MonitorConfig{}, false, fmt.Errorf("%w: %s is larger than %d bytes", ErrInvalidConfigFile, path, maxConfigFile)
	}
	return parseConfig(data)
}

func parseConfig(data []byte) (MonitorConfig, bool, error) {
	var fc fileConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fc); err != nil {
		return MonitorConfig{}, false, fmt.Errorf("%w: %v", ErrInvalidConfigFile, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return MonitorConfig{}, false, fmt.Errorf("%w: unexpected data after the config object", ErrInvalidConfigFile)
	}
	if !fc.Enabled {
		return MonitorConfig{}, false, nil
	}

	var cfg MonitorConfig
	var err error
	if cfg.StartDelay, err = requiredDuration("start_delay", fc.StartDelay); err != nil {
		return MonitorConfig{}, false, err
	}
	if cfg.Interval, err = requiredDuration("interval", fc.Interval); err != nil {
		return MonitorConfig{}, false, err
	}
	if cfg.Timeout, err = requiredDuration("timeout", fc.Timeout); err != nil {
		return MonitorConfig{}, false, err
	}
	for _, s := range fc.IPs {
		ip, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			return MonitorConfig{}, false, fmt.Errorf("%w: ips: %v", ErrInvalidConfigFile, err)
		}
		cfg.IPs = append(cfg.IPs, ip)
	}
	for i, fp := range fc.Providers {
		p := Provider{Name: fp.Name, Zone: fp.Zone, IPv6: fp.IPv6}
		if p.Listed, err = parseCodes(fp.Listed); err != nil {
			return MonitorConfig{}, false, fmt.Errorf("%w: providers[%d].listed: %v", ErrInvalidConfigFile, i, err)
		}
		if p.Errors, err = parseCodes(fp.Errors); err != nil {
			return MonitorConfig{}, false, fmt.Errorf("%w: providers[%d].errors: %v", ErrInvalidConfigFile, i, err)
		}
		cfg.Providers = append(cfg.Providers, p)
	}
	return cfg, true, nil
}

func requiredDuration(field string, s *string) (time.Duration, error) {
	if s == nil {
		return 0, fmt.Errorf("%w: %s is required", ErrInvalidConfigFile, field)
	}
	d, err := time.ParseDuration(strings.TrimSpace(*s))
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", ErrInvalidConfigFile, field, err)
	}
	return d, nil
}

// parseCodes accepts each answer code as a prefix ("127.0.0.0/24") or a
// single address ("127.0.0.2", meaning /32).
func parseCodes(ss []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, err
			}
			if p != p.Masked() {
				return nil, fmt.Errorf("%s has host bits set", s)
			}
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
