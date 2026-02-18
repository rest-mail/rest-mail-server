package dns

import "fmt"

// NewProvider creates a DNS provider by name. Supported names:
//   - "manual": logs required records for manual creation
//   - "dnsmasq": writes dnsmasq config files (opts[0] = config path, optional)
//   - "externaldns": writes Kubernetes DNSEndpoint CRD YAML files (opts[0] = output dir, optional)
func NewProvider(name string, opts ...string) (Provider, error) {
	switch name {
	case "manual":
		return NewManualProvider(), nil

	case "dnsmasq":
		var configPath string
		if len(opts) > 0 {
			configPath = opts[0]
		}
		return NewDnsmasqProvider(configPath), nil

	case "externaldns":
		var outputDir string
		if len(opts) > 0 {
			outputDir = opts[0]
		}
		return NewExternalDNSProvider(outputDir), nil

	default:
		return nil, fmt.Errorf("unknown DNS provider: %q (supported: manual, dnsmasq, externaldns)", name)
	}
}
