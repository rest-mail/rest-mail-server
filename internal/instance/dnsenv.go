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

// DNSEnv renders the dns.env consumed by the reference-dnsmasq `render-fragment`
// subcommand together with rest-mail's split-gateway fragment template
// (projects/dnsmasq/fragment.tmpl). Unlike the reference unified-host env, it
// exposes each gateway's IP (SMTP/IMAP/POP3) plus the API IP, because rest-mail
// runs those as separate containers.
func DNSEnv(m *Manifest) ([]byte, error) {
	smtp := m.componentIP("smtp-gateway")
	if smtp == "" {
		return nil, fmt.Errorf("manifest %q has no smtp-gateway component", m.Domain)
	}
	var b bytes.Buffer
	b.WriteString("# GENERATED from manifest.yml for reference-dnsmasq render-fragment — DO NOT EDIT.\n\n")
	kv := func(k, v string) { fmt.Fprintf(&b, "%s=%s\n", k, v) }
	kv("MAIL_NAME", slugFor(m.Domain))
	kv("MAIL_HOSTNAME", m.Hostname)
	kv("SMTP_IP", smtp)
	kv("IMAP_IP", m.componentIP("imap-gateway"))
	kv("POP3_IP", m.componentIP("pop3-gateway"))
	kv("API_IP", m.componentIP("api"))
	kv("MAIL_PTR", reversePTR(smtp))
	kv("MTA_STS_ID", mtaStsID(m.Hostname))
	return b.Bytes(), nil
}
