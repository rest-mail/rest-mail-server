// Package instance loads and renders rest-mail instance manifests.
//
// A manifest (config/<domain>/manifest.yml) is the human-authored source of
// truth for one logical mail instance. Render flattens it into a config.env
// that the Taskfile loads via dotenv, so per-instance values live in the
// manifest rather than hardcoded in the Taskfile. The logic lives here (not in
// cmd/) so it is exercised by the standard `go test ./internal/...` CI lane.
package instance

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v2"
)

// Manifest is the subset of the instance manifest that Render consumes. It
// mirrors the envelope/binding split of the YAML file.
// DefaultVarsPrefix is the namespace a manifest gets when it names none. One
// constant, in one place: the prefix used to be forty string literals, and a typo
// in any of them emitted a variable nothing read, which renders as empty rather
// than failing. It was MAIL3_ — a mail server that no longer exists, whose name
// had reached ~250 places including the volume rest-mail-mail4-mail3-attachments.
const DefaultVarsPrefix = "RESTMAIL"

type Manifest struct {
	// VarsPrefix namespaces every variable this manifest renders — RESTMAIL_HOSTNAME,
	// RESTMAIL_POSTGRES_IP. Set it here and nothing in the binary decides the name;
	// DefaultVarsPrefix applies when it is absent.
	//
	// The prefix is a NAMESPACE and yours to choose. The part after it is a SCHEMA:
	// `chores.yml` reads {{.RESTMAIL_POSTGRES_IP}} by name, so those suffixes are
	// the field names of a contract and are deliberately not configurable — nothing
	// could consume the output reliably if they were.
	VarsPrefix string `yaml:"vars_prefix"`
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
	// collisions. Defaults false (publish), preserving the primary instance's behavior.
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
	// a nil/empty field emits no RESTMAIL_* line, so the Taskfile catalog
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
		Selector string `yaml:"selector"` // RESTMAIL_DKIM_SELECTOR (dkim-provision --selector)
		Bits     *int   `yaml:"bits"`     // RESTMAIL_DKIM_BITS (dkim-provision -bits)
	} `yaml:"dkim"`
	// Domains declares ADDITIONAL served domains beyond the primary `domain`.
	// Optional and additive: omitted → the instance serves only the primary
	// domain and every rendered/provisioned artifact is byte-for-byte as before.
	// The primary is NEVER repeated here — it stays the top-level
	// `domain`/`hostname` (instance identity + default cert CN). Each entry gets
	// its own DB domain row (with server_type), DKIM selector/key, and DNS
	// records at provision time. Domains, mailboxes, aliases and DKIM keys remain
	// DB-driven — this list only DECLARES which domains the instance serves so
	// they can be provisioned at instance-up. See ServedDomains().
	Domains []DomainEntry `yaml:"domains"`
	// TLS holds optional TLS declarations for the instance. Every field is
	// optional and strict-parse-safe: an omitted `tls:` block emits no
	// RESTMAIL_TLS_* / RESTMAIL_CERT_PROVIDER / RESTMAIL_INTERNAL_CA_SOURCE line, so
	// existing manifests render byte-for-byte as before. It carries two
	// independent concerns: the PUBLIC serving cert (`extra_hostnames`, `acme`)
	// and the gateway→API INTERNAL mutual-TLS channel (`internal:`). The internal
	// block is the richer, declarative counterpart of the legacy top-level
	// `internal_mtls` bool (still supported) — see InternalTLS.
	TLS TLS `yaml:"tls"`
	// binding
	Components []Component `yaml:"components"`
}

// TLS is the optional public-TLS block of a manifest.
//
//   - ExtraHostnames are ADDITIONAL DNS names the instance serving cert must
//     cover on top of every served domain's mail hostname — e.g.
//     autoconfig/autodiscover, mta-sts, or a vanity submission host. They are
//     purely cert SANs: unlike `domains:` entries they get NO DB row, DKIM key
//     or DNS provisioning. See CertSANHostnames().
//   - ACME declares how a public CA (Let's Encrypt-style) would issue the cert.
//     PR5 wires these fields through so they are declarable and the
//     `cert_provider` seam exists; the ACME client itself is NOT implemented in
//     this PR (see the `instance:certs:issue` task — the `acme`/`letsencrypt`
//     providers return a "not yet implemented" error).
type TLS struct {
	ExtraHostnames []string `yaml:"extra_hostnames"`
	ACME           ACME     `yaml:"acme"`
	// Internal is the optional gateway→API internal-mTLS sub-block. A pointer so
	// "block present" is distinguishable from "omitted": present makes the block
	// authoritative over the legacy `internal_mtls` bool (with mode defaulting to
	// require/on); omitted falls back to the bool, so existing manifests are
	// byte-for-byte unchanged.
	Internal *InternalTLS `yaml:"internal"`
}

// InternalTLS is the optional `tls.internal:` sub-block — the declarative form of
// the gateway→API internal mutual-TLS switch, and the richer counterpart of the
// legacy top-level `internal_mtls: bool` (which stays supported for
// back-compat). Where the bool only toggles on/off, this block also declares HOW
// the internal trust material is provisioned.
//
//   - Mode selects the handshake posture. "require" (the default when the block
//     is present with an empty mode) and "verify" both turn internal mTLS ON: the
//     API's dedicated internal listener REQUIRES and verifies a client
//     certificate signed by the internal CA (tls.RequireAndVerifyClientCert) —
//     exactly today's behavior. The implementation always enforces the client
//     cert, so "verify" and "require" are equivalent today; the pair is kept for
//     forward-compatibility, and the secure-by-construction posture means neither
//     is a soft/optional mode. "off" disables the channel (routes stay on the
//     public listener, tokenless — the pre-mTLS behavior).
//   - CASource selects the provisioning path `task instance:mtls:issue` takes:
//     "testbed-certgen" (the default) mints a dedicated internal CA + API server
//     cert + gateway client cert with the in-repo certgen — what the testbed does
//     today; "manual" means the deployer provisions the internal CA + server/
//     client material out of band into the certs volume and the task only
//     validates its presence (no issuance).
//
// Rendered env: mode → RESTMAIL_INTERNAL_MTLS (only when ON, matching the bool);
// a non-default ca_source → RESTMAIL_INTERNAL_CA_SOURCE. Both lines are emitted
// only when they carry information, so an omitted block renders nothing new.
type InternalTLS struct {
	Mode     string `yaml:"mode"`      // off | verify | require; default require (on)
	CASource string `yaml:"ca_source"` // testbed-certgen (default) | manual
}

// Internal-mTLS mode tokens. verify and require both ENABLE the enforced
// gateway→API handshake (the implementation always RequireAndVerifyClientCert);
// off disables it. An empty mode defaults to require (secure-by-default: on).
const (
	internalMTLSModeOff     = "off"
	internalMTLSModeVerify  = "verify"
	internalMTLSModeRequire = "require"
)

// internalCASourceDefault is the CA-provisioning source that renders NO line (the
// Taskfile `| default` supplies it), so testbed/default manifests are unchanged.
const internalCASourceDefault = "testbed-certgen"

// internalMTLSEnabled resolves whether this instance runs the gateway→API mutual
// TLS channel. The optional `tls.internal:` block is authoritative when present:
// mode require/verify — or an empty mode, which defaults to require — turns it
// ON; mode off turns it OFF. When the block is OMITTED the legacy top-level
// `internal_mtls` bool decides, so existing manifests render byte-for-byte as
// before.
func (m *Manifest) internalMTLSEnabled() bool {
	if m.TLS.Internal != nil {
		return m.TLS.Internal.Mode != internalMTLSModeOff
	}
	return m.InternalMTLS
}

// ACME is the optional `tls.acme:` sub-block. Bools are pointers so an unset
// field emits no line (and is distinguishable from an explicit false).
type ACME struct {
	Enabled   *bool  `yaml:"enabled"`   // RESTMAIL_TLS_ACME_ENABLED
	Email     string `yaml:"email"`     // RESTMAIL_TLS_ACME_EMAIL (ACME account contact)
	Staging   *bool  `yaml:"staging"`   // RESTMAIL_TLS_ACME_STAGING (use the CA's staging endpoint)
	Directory string `yaml:"directory"` // RESTMAIL_TLS_ACME_DIRECTORY (ACME directory URL override)
}

// DomainEntry is one ADDITIONAL served domain declared in the manifest
// `domains:` list. Only Name is required. ServerType defaults to "restmail"
// (matching the primary/seed fixture) and, when set, must be one of the
// DB-supported values ("traditional" | "restmail"). Hostname defaults to Name.
// As with the top-level `dkim:` block, only the PUBLIC selector/bits are
// declarative — the DKIM PRIVATE KEY is never in the manifest; it is generated
// and installed at runtime via `task instance:dkim`.
type DomainEntry struct {
	Name       string `yaml:"name"`
	ServerType string `yaml:"server_type"` // "traditional" | "restmail"; default "restmail"
	Hostname   string `yaml:"hostname"`    // gateway EHLO/DNS name; default = Name
	DKIM       struct {
		Selector string `yaml:"selector"`
		Bits     *int   `yaml:"bits"`
	} `yaml:"dkim"`
	// DNS is an optional per-domain DNS override. Declared now for strict-parse
	// acceptance and forward-compat; the derived MX/A/PTR/MTA-STS records come
	// from the SHARED instance gateways (every served domain resolves to the same
	// smtp/imap/pop3 IPs). Richer per-domain records are consumed by the PR5
	// DNS/cert seam.
	DNS *DomainDNS `yaml:"dns"`
}

// DomainDNS is the optional per-domain DNS override block. Kept minimal and
// strict: unknown keys under `dns:` still error. ExtraRecords is declared but
// not yet consumed (PR5 DNS/cert seam).
type DomainDNS struct {
	ExtraRecords []string `yaml:"extra_records"`
}

// validServerType reports whether s is an accepted per-domain server type.
// Empty is accepted (unset → defaults to "restmail" at provision time).
func validServerType(s string) bool {
	return s == "" || s == "traditional" || s == "restmail"
}

// Validate checks cross-field manifest invariants that strict YAML parsing
// cannot: each declared additional domain must have a non-empty name and a
// valid server_type, names must be unique, and an additional domain must not
// duplicate the primary `domain`. Called by Parse so malformed manifests fail
// loudly at load time.
func (m *Manifest) Validate() error {
	seen := map[string]bool{}
	for i, d := range m.Domains {
		if d.Name == "" {
			return fmt.Errorf("domains[%d]: name is required", i)
		}
		if !validServerType(d.ServerType) {
			return fmt.Errorf("domains[%d] (%s): invalid server_type %q (want traditional|restmail)", i, d.Name, d.ServerType)
		}
		if d.Name == m.Domain {
			return fmt.Errorf("domains[%d]: %q duplicates the primary domain — declare additional domains only", i, d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("domains[%d]: duplicate domain %q", i, d.Name)
		}
		seen[d.Name] = true
	}
	if ti := m.TLS.Internal; ti != nil {
		switch ti.Mode {
		case "", internalMTLSModeOff, internalMTLSModeVerify, internalMTLSModeRequire:
		default:
			return fmt.Errorf("tls.internal.mode %q invalid (want off|verify|require)", ti.Mode)
		}
		switch ti.CASource {
		case "", internalCASourceDefault, "manual":
		default:
			return fmt.Errorf("tls.internal.ca_source %q invalid (want testbed-certgen|manual)", ti.CASource)
		}
		// A manifest that both flips the legacy bool on and sets the block off is
		// contradictory — fail loudly rather than silently pick one.
		if m.InternalMTLS && ti.Mode == internalMTLSModeOff {
			return fmt.Errorf("internal_mtls: true conflicts with tls.internal.mode: off — set exactly one")
		}
	}
	return nil
}

// ServedDomain is a resolved entry in the instance's served-domain set: the
// primary plus each additional `domains:` entry, with defaults applied. It is
// the single source the render/DNS/DKIM/seed provisioning paths iterate.
type ServedDomain struct {
	Name       string // domain name (DB domain row Name)
	Hostname   string // gateway EHLO / DNS hostname
	ServerType string // "traditional" | "restmail"
	Primary    bool   // true for the top-level primary domain
	Selector   string // DKIM selector ("" → provisioner default "default")
	Bits       *int   // DKIM key bits (nil → provisioner default)
}

// ServedDomains returns every domain this instance serves: the primary first
// (from `domain`/`hostname` + the top-level `dkim:` block), then each
// `domains:` entry in order. Defaults are resolved here so callers don't
// repeat them: Hostname←Name, ServerType←"restmail". The primary always uses
// server_type "restmail" to match the seed fixture.
func (m *Manifest) ServedDomains() []ServedDomain {
	out := make([]ServedDomain, 0, 1+len(m.Domains))
	primaryHost := m.Hostname
	if primaryHost == "" {
		primaryHost = m.Domain
	}
	out = append(out, ServedDomain{
		Name:       m.Domain,
		Hostname:   primaryHost,
		ServerType: "restmail",
		Primary:    true,
		Selector:   m.DKIM.Selector,
		Bits:       m.DKIM.Bits,
	})
	for _, d := range m.Domains {
		host := d.Hostname
		if host == "" {
			host = d.Name
		}
		st := d.ServerType
		if st == "" {
			st = "restmail"
		}
		out = append(out, ServedDomain{
			Name:       d.Name,
			Hostname:   host,
			ServerType: st,
			Selector:   d.DKIM.Selector,
			Bits:       d.DKIM.Bits,
		})
	}
	return out
}

// AdditionalServedDomains returns ServedDomains minus the primary — the
// domains that need provisioning ON TOP OF the primary path.
func (m *Manifest) AdditionalServedDomains() []ServedDomain {
	all := m.ServedDomains()
	return all[1:]
}

// CertSANHostnames returns the complete Subject-Alternative-Name set the
// instance serving certificate must cover: every served domain's mail hostname
// (ServedDomains(), primary first) UNION any tls.extra_hostnames, de-duplicated
// while preserving first-seen order. It is the single source the cert-issuance
// path (task instance:certs:issue) consumes so a single multi-SAN cert covers
// the whole instance. For a bare single-domain manifest with no extra hostnames
// this is exactly []string{hostname}, so the issuance default is byte-identical
// to before (#99 deferred this multi-name derivation to PR5 — resolved here).
func (m *Manifest) CertSANHostnames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, 1+len(m.Domains)+len(m.TLS.ExtraHostnames))
	add := func(h string) {
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, d := range m.ServedDomains() {
		add(d.Hostname)
	}
	for _, h := range m.TLS.ExtraHostnames {
		add(h)
	}
	return out
}

// SeedServedDomain is one additional served domain to seed a DB row for,
// decoded from the RESTMAIL_SEED_SERVED_DOMAINS env line.
type SeedServedDomain struct {
	Name       string
	ServerType string
}

// ParseSeedServedDomains decodes the RESTMAIL_SEED_SERVED_DOMAINS value
// ("name:server_type,name:server_type,...") that Render emits, into structured
// entries. It is the decode side of that encoding — `cmd/seed` consumes it to
// create a DB domain row per additional domain. Empty input yields no entries
// (single-domain instances seed exactly as before). server_type defaults to
// "restmail" and, when present, must be valid.
func ParseSeedServedDomains(spec string) ([]SeedServedDomain, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []SeedServedDomain
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, st, hasST := strings.Cut(part, ":")
		name = strings.TrimSpace(name)
		st = strings.TrimSpace(st)
		if name == "" {
			return nil, fmt.Errorf("served-domains spec %q: empty domain name", spec)
		}
		if !hasST || st == "" {
			st = "restmail"
		}
		if !validServerType(st) {
			return nil, fmt.Errorf("served-domains spec: domain %q has invalid server_type %q", name, st)
		}
		out = append(out, SeedServedDomain{Name: name, ServerType: st})
	}
	return out, nil
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
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// Render flattens a manifest into a deterministic config.env byte stream. The
// variable names match the Taskfile convention (RESTMAIL_*/RESTMAIL_*) so that
// switching the Taskfile to load this file is behavior-preserving.
func Render(m *Manifest) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("# GENERATED from manifest.yml by `task instance:render` — DO NOT EDIT.\n")
	b.WriteString("# Edit manifest.yml, then re-render. Secrets live in secrets.env.\n\n")

	kv := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, v) }
	// Every rendered name is namespaced here, once.
	prefix := m.VarsPrefix
	if prefix == "" {
		prefix = DefaultVarsPrefix
	}
	iv := func(k, v string) { kv(prefix+"_"+k, v) }

	// Substrate + image + runtime (envelope-derived).
	iv("PROJECT", m.Project)
	iv("NETWORK", m.Network)
	iv("CERTS_VOLUME", m.CertsVolume)
	iv("TESTBED_DNS_IP", m.TestbedDNSIP)
	iv("REGISTRY", m.Registry)
	iv("IMAGE_TAG", m.ImageTag)
	iv("PROXY_HOST", m.ProxyHost)
	// Cert provider selects which branch `task instance:certs:issue` takes.
	// "testbed-certgen" is THE default, so it renders no line (and the Taskfile
	// `| default "testbed-certgen"` supplies it) — keeping existing manifests
	// byte-for-byte identical. Only a non-default provider (manual, acme/
	// letsencrypt) emits the switch line.
	if m.CertProvider != "" && m.CertProvider != "testbed-certgen" {
		iv("CERT_PROVIDER", m.CertProvider)
	}
	// Internal-mTLS CA provisioning source (manifest `tls.internal.ca_source`).
	// Like cert_provider, "testbed-certgen" is THE default and renders no line
	// (the Taskfile `| default "testbed-certgen"` supplies it), so existing
	// manifests are byte-for-byte identical; only a non-default source (manual)
	// emits the switch that steers `task instance:mtls:issue`.
	if ti := m.TLS.Internal; ti != nil && ti.CASource != "" && ti.CASource != internalCASourceDefault {
		iv("INTERNAL_CA_SOURCE", ti.CASource)
	}
	b.WriteString("\n")
	iv("HOSTNAME", m.Hostname)
	iv("DB_NAME", m.DB.Name)
	iv("DB_USER", m.DB.User)
	iv("LOG_LEVEL", m.LogLevel)
	iv("ENVIRONMENT", m.Environment)
	iv("MAILNET_ONLY", strconv.FormatBool(m.MailnetOnly))
	// Emit the internal-mTLS switch only when enabled, so instances that don't
	// opt in render byte-for-byte as before (no drift against committed config).
	// Enablement is resolved from the optional `tls.internal:` block (mode
	// require/verify, or an empty mode → default require) when present, else the
	// legacy top-level `internal_mtls` bool — see internalMTLSEnabled.
	if m.internalMTLSEnabled() {
		iv("INTERNAL_MTLS", "true")
	}

	// SMTP/queue policy knobs (manifest `smtp:` block). Each is emitted only
	// when set; an omitted knob renders no line, so the Taskfile catalog
	// `| default` and the config.go fallback apply — a manifest with no `smtp:`
	// block renders byte-for-byte as before.
	if v := m.SMTP.MaxMessageSize; v != nil {
		iv("SMTP_MAX_MESSAGE_SIZE", strconv.FormatInt(*v, 10))
	}
	if v := m.SMTP.MinTransferRate; v != nil {
		iv("SMTP_MIN_TRANSFER_RATE", strconv.FormatInt(*v, 10))
	}
	if v := m.SMTP.TransferGracePeriod; v != nil {
		iv("SMTP_TRANSFER_GRACE_PERIOD", strconv.Itoa(*v))
	}
	if v := m.SMTP.TransferStallTimeout; v != nil {
		iv("SMTP_TRANSFER_STALL_TIMEOUT", strconv.Itoa(*v))
	}
	if v := m.SMTP.QueueWorkers; v != nil {
		iv("SMTP_QUEUE_WORKERS", strconv.Itoa(*v))
	}
	if m.SMTP.QueuePollInterval != "" {
		iv("SMTP_QUEUE_POLL_INTERVAL", m.SMTP.QueuePollInterval)
	}
	if v := m.SMTP.MTASTSEnforce; v != nil {
		iv("SMTP_MTASTS_ENFORCE", strconv.FormatBool(*v))
	}

	// DKIM signing parameters (manifest `dkim:` block). Only the public
	// selector and key size are rendered; the DKIM PRIVATE KEY is never in the
	// manifest — it stays a runtime secret provisioned via `task instance:dkim`.
	// Omitted fields emit no line, so dkim-provision's defaults apply.
	if m.DKIM.Selector != "" {
		iv("DKIM_SELECTOR", m.DKIM.Selector)
	}
	if v := m.DKIM.Bits; v != nil {
		iv("DKIM_BITS", strconv.Itoa(*v))
	}

	// Multi-domain (manifest `domains:` list). Emitted ONLY when the instance
	// declares additional served domains, so a single-domain manifest renders
	// byte-for-byte as before. Two flat lines carry what the provisioning tasks
	// need; the structured per-domain data (selector/bits) is read from the
	// manifest by `instance domains` at provision time.
	//   - RESTMAIL_SERVED_HOSTNAMES  every served mail hostname (primary first) —
	//     the cert SAN set + the guard that turns on the per-domain DKIM/DNS
	//     provisioning loops.
	//   - RESTMAIL_SEED_SERVED_DOMAINS  the ADDITIONAL domains as name:server_type,
	//     so `cmd/seed` creates a DB domain row (with server_type) for each.
	if len(m.Domains) > 0 {
		served := m.ServedDomains()
		hostnames := make([]string, 0, len(served))
		for _, d := range served {
			hostnames = append(hostnames, d.Hostname)
		}
		iv("SERVED_HOSTNAMES", strings.Join(hostnames, ","))

		extra := make([]string, 0, len(m.Domains))
		for _, d := range m.AdditionalServedDomains() {
			extra = append(extra, d.Name+":"+d.ServerType)
		}
		iv("SEED_SERVED_DOMAINS", strings.Join(extra, ","))
	}

	// Public-TLS seam (manifest `tls:` block + the derived cert SAN set). Every
	// line is emitted only when it carries information, so a manifest without a
	// `tls:` block and without additional `domains:` renders byte-for-byte as
	// before.
	//   - RESTMAIL_TLS_EXTRA_HOSTNAMES  the raw tls.extra_hostnames, for visibility.
	//   - RESTMAIL_TLS_CERT_SANS        the COMPLETE SAN list the instance cert must
	//     cover: served hostnames ∪ extra_hostnames (see CertSANHostnames()).
	//     Emitted whenever it holds more than the single primary hostname
	//     (additional domains and/or extra hostnames); `task instance:certs:issue`
	//     consumes it, falling back to RESTMAIL_HOSTNAME for the single-domain
	//     default — byte-identical to before.
	//   - RESTMAIL_TLS_ACME_*           the declarative ACME inputs, wired through so
	//     the cert_provider seam is complete; the ACME client is not implemented
	//     in PR5.
	if len(m.TLS.ExtraHostnames) > 0 {
		iv("TLS_EXTRA_HOSTNAMES", strings.Join(m.TLS.ExtraHostnames, ","))
	}
	if len(m.Domains) > 0 || len(m.TLS.ExtraHostnames) > 0 {
		iv("TLS_CERT_SANS", strings.Join(m.CertSANHostnames(), ","))
	}
	if v := m.TLS.ACME.Enabled; v != nil {
		iv("TLS_ACME_ENABLED", strconv.FormatBool(*v))
	}
	if m.TLS.ACME.Email != "" {
		iv("TLS_ACME_EMAIL", m.TLS.ACME.Email)
	}
	if v := m.TLS.ACME.Staging; v != nil {
		iv("TLS_ACME_STAGING", strconv.FormatBool(*v))
	}
	if m.TLS.ACME.Directory != "" {
		iv("TLS_ACME_DIRECTORY", m.TLS.ACME.Directory)
	}
	b.WriteString("\n")

	// Component IPs and ports (binding-derived). Each component maps to the
	// RESTMAIL_* vars the Taskfile currently uses.
	for _, c := range m.Components {
		switch c.Name {
		case "postgres":
			iv("POSTGRES_IP", c.IP)
		case "api":
			iv("API_IP", c.IP)
			iv("API_PORT", strconv.Itoa(c.Port))
		case "smtp-gateway":
			iv("SMTP_IP", c.IP)
			iv("SMTP_PORT_INBOUND", port(c, "inbound"))
			iv("SMTP_PORT_SUBMISSION", port(c, "submission"))
			iv("SMTP_PORT_SUBMISSION_TLS", port(c, "submission_tls"))
		case "imap-gateway":
			iv("IMAP_IP", c.IP)
			iv("IMAP_PORT", port(c, "plain"))
			iv("IMAP_TLS_PORT", port(c, "tls"))
		case "pop3-gateway":
			iv("POP3_IP", c.IP)
			iv("POP3_PORT", port(c, "plain"))
			iv("POP3_TLS_PORT", port(c, "tls"))
		case "js-filter":
			iv("JS_FILTER_IP", c.IP)
		case "webmail":
			iv("WEBMAIL_IP", c.IP)
		case "admin":
			iv("ADMIN_IP", c.IP)
		default:
			return nil, fmt.Errorf("unknown component %q (no catalog mapping)", c.Name)
		}
	}
	return b.Bytes(), nil
}

func port(c Component, key string) string {
	return strconv.Itoa(c.Ports[key])
}
