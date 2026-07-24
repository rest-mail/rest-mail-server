package instance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// componentIP returns the IP assigned to a named component, or "" if absent.
func (m *Manifest) componentIP(name string) string {
	for _, c := range m.Components {
		if c.Name == name {
			return c.IP
		}
	}
	return ""
}

// reversePTR turns an IPv4 like 10.99.0.13 into its in-addr.arpa form.
func reversePTR(ip string) string {
	p := strings.Split(ip, ".")
	if len(p) != 4 {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s.%s.in-addr.arpa", p[3], p[2], p[1], p[0])
}

// mtaStsID derives a stable, opaque MTA-STS policy id from the hostname. In
// production this must match the id the API's MTA-STS endpoint serves; for the
// testbed a deterministic value is enough.
func mtaStsID(hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	return hex.EncodeToString(sum[:])[:12]
}

// DNSEnv renders the dns.env for the instance's PRIMARY domain. See
// dnsEnvFor for the shape. Kept as the back-compatible entry point.
func DNSEnv(m *Manifest) ([]byte, error) {
	return dnsEnvFor(m, m.Domain, m.Hostname)
}

// DNSEnvForDomain renders the dns.env for one SERVED domain of the instance
// (by domain name). Every served domain resolves to the SAME instance gateways
// (SMTP/IMAP/POP3/API IPs) — only the MAIL_NAME/MAIL_HOSTNAME/MTA_STS_ID differ
// — so multi-domain DNS is one fragment per served domain over the shared
// gateway IPs. The domain must be one this instance serves (primary or a
// `domains:` entry).
func DNSEnvForDomain(m *Manifest, domain string) ([]byte, error) {
	for _, d := range m.ServedDomains() {
		if d.Name == domain {
			return dnsEnvFor(m, d.Name, d.Hostname)
		}
	}
	return nil, fmt.Errorf("manifest %q does not serve domain %q", m.Domain, domain)
}

// dnsEnvFor renders the dns.env consumed by the reference-dnsmasq
// `render-fragment` subcommand together with rest-mail's split-gateway fragment
// template (projects/dnsmasq/fragment.tmpl). Unlike the reference unified-host
// env, it exposes each gateway's IP (SMTP/IMAP/POP3) plus the API IP, because
// rest-mail runs those as separate containers. name/hostname identify which
// served domain the fragment is for; the gateway IPs are shared instance-wide.
func dnsEnvFor(m *Manifest, name, hostname string) ([]byte, error) {
	smtp := m.componentIP("smtp-gateway")
	if smtp == "" {
		return nil, fmt.Errorf("manifest %q has no smtp-gateway component", m.Domain)
	}
	var b bytes.Buffer
	b.WriteString("# GENERATED from manifest.yml for reference-dnsmasq render-fragment — DO NOT EDIT.\n\n")
	kv := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, v) }
	kv("MAIL_NAME", slugFor(name))
	kv("MAIL_HOSTNAME", hostname)
	kv("SMTP_IP", smtp)
	kv("IMAP_IP", m.componentIP("imap-gateway"))
	kv("POP3_IP", m.componentIP("pop3-gateway"))
	kv("API_IP", m.componentIP("api"))
	kv("MAIL_PTR", reversePTR(smtp))
	kv("MTA_STS_ID", mtaStsID(hostname))
	return b.Bytes(), nil
}
