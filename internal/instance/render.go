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
	// InternalMTLS true turns on gateway→API mutual-TLS for this instance: the
	// API serves the gateway-facing routes on a dedicated client-cert-verified
	// listener and the gateways present a client cert. Requires the internal
	// mTLS certs to be provisioned into the certs volume (task
	// instance:mtls:issue). Defaults false, so existing instances are unchanged
	// and their rendered config.env does not gain a line.
	InternalMTLS bool `yaml:"internal_mtls"`
	DB           struct {
		Name string `yaml:"name"`
		User string `yaml:"user"`
	} `yaml:"db"`
	// SMTP holds optional SMTP/queue policy knobs. Every field is optional:
	// a nil/empty field emits no MAIL3_* line, so the Taskfile catalog
	// `| default` and the config.go fallback apply and existing manifests (no
	// `smtp:` block) render byte-for-byte as before. Pointers (not zero values)
	// distinguish "unset" from a meaningful zero — e.g. MinTransferRate 0
	// disables the rate floor, MTASTSEnforce false differs from the default
	// true. Each field maps 1:1 to a config.go env var.
	SMTP struct {
		MaxMessageSize       *int64 `yaml:"max_message_size"`       // SMTP_MAX_MESSAGE_SIZE (bytes)
		MinTransferRate      *int64 `yaml:"min_transfer_rate"`      // SMTP_MIN_TRANSFER_RATE (bytes/sec; 0 disables the floor)
		TransferGracePeriod  *int   `yaml:"transfer_grace_period"`  // SMTP_TRANSFER_GRACE_PERIOD (seconds)
		TransferStallTimeout *int   `yaml:"transfer_stall_timeout"` // SMTP_TRANSFER_STALL_TIMEOUT (seconds)
		QueueWorkers         *int   `yaml:"queue_workers"`          // QUEUE_WORKERS
		QueuePollInterval    string `yaml:"queue_poll_interval"`    // QUEUE_POLL_INTERVAL (Go duration, e.g. "5s")
		MTASTSEnforce        *bool  `yaml:"mtasts_enforce"`         // MTASTS_ENFORCE
	} `yaml:"smtp"`
	// DKIM holds optional DKIM signing parameters. Only the public selector and
	// key size are declarative. The DKIM PRIVATE KEY is NEVER stored in the
	// manifest — it stays a runtime secret, generated and installed on the
	// instance via `task instance:dkim` (dkim-provision → admin API). Omitted
	// fields emit no line, so `task instance:dkim` keeps its defaults
	// (selector "default", 2048 bits) and existing manifests are unchanged.
	DKIM struct {
		Selector string `yaml:"selector"` // MAIL3_DKIM_SELECTOR (dkim-provision --selector)
		Bits     *int   `yaml:"bits"`     // MAIL3_DKIM_BITS (dkim-provision -bits)
	} `yaml:"dkim"`
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
	// Emit the internal-mTLS switch only when enabled, so instances that don't
	// opt in render byte-for-byte as before (no drift against committed config).
	if m.InternalMTLS {
		kv("MAIL3_INTERNAL_MTLS", "true")
	}

	// SMTP/queue policy knobs (manifest `smtp:` block). Each is emitted only
	// when set; an omitted knob renders no line, so the Taskfile catalog
	// `| default` and the config.go fallback apply — a manifest with no `smtp:`
	// block renders byte-for-byte as before.
	if v := m.SMTP.MaxMessageSize; v != nil {
		kv("MAIL3_SMTP_MAX_MESSAGE_SIZE", strconv.FormatInt(*v, 10))
	}
	if v := m.SMTP.MinTransferRate; v != nil {
		kv("MAIL3_SMTP_MIN_TRANSFER_RATE", strconv.FormatInt(*v, 10))
	}
	if v := m.SMTP.TransferGracePeriod; v != nil {
		kv("MAIL3_SMTP_TRANSFER_GRACE_PERIOD", strconv.Itoa(*v))
	}
	if v := m.SMTP.TransferStallTimeout; v != nil {
		kv("MAIL3_SMTP_TRANSFER_STALL_TIMEOUT", strconv.Itoa(*v))
	}
	if v := m.SMTP.QueueWorkers; v != nil {
		kv("MAIL3_SMTP_QUEUE_WORKERS", strconv.Itoa(*v))
	}
	if m.SMTP.QueuePollInterval != "" {
		kv("MAIL3_SMTP_QUEUE_POLL_INTERVAL", m.SMTP.QueuePollInterval)
	}
	if v := m.SMTP.MTASTSEnforce; v != nil {
		kv("MAIL3_SMTP_MTASTS_ENFORCE", strconv.FormatBool(*v))
	}

	// DKIM signing parameters (manifest `dkim:` block). Only the public
	// selector and key size are rendered; the DKIM PRIVATE KEY is never in the
	// manifest — it stays a runtime secret provisioned via `task instance:dkim`.
	// Omitted fields emit no line, so dkim-provision's defaults apply.
	if m.DKIM.Selector != "" {
		kv("MAIL3_DKIM_SELECTOR", m.DKIM.Selector)
	}
	if v := m.DKIM.Bits; v != nil {
		kv("MAIL3_DKIM_BITS", strconv.Itoa(*v))
	}
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
