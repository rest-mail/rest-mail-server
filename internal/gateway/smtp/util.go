package smtp

import (
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// maskEmail redacts the local-part of an email address for logging (OSI-5):
// "alice@example.com" -> "a***@example.com". The domain is preserved for
// operational triage while the individual identity is not written in the clear —
// important on the failed-auth / not-authorized paths, which log attacker-
// supplied, high-volume, often non-customer addresses (credential-stuffing and
// enumeration probes). An empty value maps to "" and a value without "@" is
// masked whole.
func maskEmail(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		// No usable local-part/domain split: reveal only the first rune.
		return firstRune(addr) + "***"
	}
	return firstRune(addr[:at]) + "***" + addr[at:]
}

// firstRune returns the first rune of s (or "" if empty), used to keep a minimal
// non-identifying prefix in masked log values.
func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// extractIP extracts the IP address from a remote address string (host:port).
func extractIP(addr string) string {
	host, _, err := splitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// splitHostPort is a simple host:port splitter.
func splitHostPort(addr string) (string, string, error) {
	// Handle IPv6
	if strings.HasPrefix(addr, "[") {
		end := strings.Index(addr, "]")
		if end == -1 {
			return addr, "", nil
		}
		host := addr[1:end]
		if end+1 < len(addr) && addr[end+1] == ':' {
			return host, addr[end+2:], nil
		}
		return host, "", nil
	}

	last := strings.LastIndex(addr, ":")
	if last == -1 {
		return addr, "", nil
	}
	return addr[:last], addr[last+1:], nil
}

// bareAddresses reduces parsed addresses to their address strings, dropping any
// entry that carries no address.
func bareAddresses(addrs []pipeline.Address) []string {
	var out []string
	for _, a := range addrs {
		if a.Address != "" {
			out = append(out, a.Address)
		}
	}
	return out
}

// The body-tree walk lives in internal/mime (TextAndHTML): the IMAP gateway
// needs the same two fields on APPEND, and one copy is enough.
