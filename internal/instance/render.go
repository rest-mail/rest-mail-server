// Package instance loads and renders rest-mail instance manifests.
//
// A manifest (instances/<domain>/manifest.yml) is the human-authored source of
// truth for one logical mail instance. Render flattens it into a config.env
// that the Taskfile loads via dotenv, so per-instance values live in the
// manifest rather than hardcoded in the Taskfile. The logic lives here (not in
// cmd/) so it is exercised by the standard `go test ./internal/...` CI lane.
package instance

import (
	"bytes"
	"fmt"
	"strconv"

	yaml "go.yaml.in/yaml/v2"
)

// Manifest is the subset of the instance manifest that Render consumes. It
// mirrors the envelope/binding split of the YAML file.
type Manifest struct {
	// envelope
	Domain    string `yaml:"domain"`
	Hostname  string `yaml:"hostname"`
	ProxyHost string `yaml:"proxy_host"`
	// substrate placement
	Project      string `yaml:"project"`
	Network      string `yaml:"network"`
	CertsVolume  string `yaml:"certs_volume"`
	TestbedDNSIP string `yaml:"testbed_dns_ip"`
	// substrate providers — declared now, consumed by dns/cert work (PR3+).
	// Present so strict parsing accepts the manifest.
	DNSProvider  string `yaml:"dns_provider"`
	CertProvider string `yaml:"cert_provider"`
	// image source
	Registry string `yaml:"registry"`
	ImageTag string `yaml:"image_tag"`
	// runtime
	LogLevel    string `yaml:"log_level"`
	Environment string `yaml:"environment"`
	// MailnetOnly true means don't publish gateway/API host ports — the
	// instance is reachable only on the mailnet (like the reference servers).
	// Lets a secondary instance run alongside the primary without host-port
	// collisions. Defaults false (publish), preserving mail3.test behavior.
	MailnetOnly bool `yaml:"mailnet_only"`
	DB          struct {
		Name string `yaml:"name"`
		User string `yaml:"user"`
	} `yaml:"db"`
	// binding
	Components []Component `yaml:"components"`
}

// Component is one service placed on the substrate.
type Component struct {
	Name  string         `yaml:"name"`
	IP    string         `yaml:"ip"`
	Port  int            `yaml:"port"`
	Ports map[string]int `yaml:"ports"`
}

// Parse strict-unmarshals a manifest from YAML bytes. Unknown fields are an
// error, so manifest typos fail loudly rather than being silently dropped.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.UnmarshalStrict(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// Render flattens a manifest into a deterministic config.env byte stream. The
// variable names match the Taskfile convention (RESTMAIL_*/MAIL3_*) so that
// switching the Taskfile to load this file is behavior-preserving.
func Render(m *Manifest) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("# GENERATED from manifest.yml by `task instance:render` — DO NOT EDIT.\n")
	b.WriteString("# Edit manifest.yml, then re-render. Secrets live in secrets.env.\n\n")

	kv := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, v) }

	// Substrate + image + runtime (envelope-derived).
	kv("RESTMAIL_PROJECT", m.Project)
	kv("RESTMAIL_NETWORK", m.Network)
	kv("RESTMAIL_CERTS_VOLUME", m.CertsVolume)
	kv("RESTMAIL_TESTBED_DNS_IP", m.TestbedDNSIP)
	kv("RESTMAIL_REGISTRY", m.Registry)
	kv("RESTMAIL_IMAGE_TAG", m.ImageTag)
	kv("RESTMAIL_PROXY_HOST", m.ProxyHost)
	b.WriteString("\n")
	kv("MAIL3_HOSTNAME", m.Hostname)
	kv("MAIL3_DB_NAME", m.DB.Name)
	kv("MAIL3_DB_USER", m.DB.User)
	kv("MAIL3_LOG_LEVEL", m.LogLevel)
	kv("MAIL3_ENVIRONMENT", m.Environment)
	kv("MAIL3_MAILNET_ONLY", strconv.FormatBool(m.MailnetOnly))
	b.WriteString("\n")

	// Component IPs and ports (binding-derived). Each component maps to the
	// MAIL3_* vars the Taskfile currently uses.
	for _, c := range m.Components {
		switch c.Name {
		case "postgres":
			kv("MAIL3_POSTGRES_IP", c.IP)
		case "api":
			kv("MAIL3_API_IP", c.IP)
			kv("MAIL3_API_PORT", strconv.Itoa(c.Port))
		case "smtp-gateway":
			kv("MAIL3_SMTP_IP", c.IP)
			kv("MAIL3_SMTP_PORT_INBOUND", port(c, "inbound"))
			kv("MAIL3_SMTP_PORT_SUBMISSION", port(c, "submission"))
			kv("MAIL3_SMTP_PORT_SUBMISSION_TLS", port(c, "submission_tls"))
		case "imap-gateway":
			kv("MAIL3_IMAP_IP", c.IP)
			kv("MAIL3_IMAP_PORT", port(c, "plain"))
			kv("MAIL3_IMAP_TLS_PORT", port(c, "tls"))
		case "pop3-gateway":
			kv("MAIL3_POP3_IP", c.IP)
			kv("MAIL3_POP3_PORT", port(c, "plain"))
			kv("MAIL3_POP3_TLS_PORT", port(c, "tls"))
		case "js-filter":
			kv("MAIL3_JS_FILTER_IP", c.IP)
		case "webmail":
			kv("MAIL3_WEBMAIL_IP", c.IP)
		case "admin":
			kv("MAIL3_ADMIN_IP", c.IP)
		default:
			return nil, fmt.Errorf("unknown component %q (no catalog mapping)", c.Name)
		}
	}
	return b.Bytes(), nil
}

func port(c Component, key string) string {
	return strconv.Itoa(c.Ports[key])
}
