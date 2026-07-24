package imap

import "strings"

// maskEmail redacts the local-part of an email address for logging:
// "alice@example.com" -> "a***@example.com". Used on the failed-auth path, where
// the attempted username is attacker-controlled and high-volume (credential
// stuffing / enumeration probes), so the individual identity is not written in
// the clear while the domain stays available for operational triage. An empty
// value maps to "" and a value without "@" is masked to its first rune.
func maskEmail(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return firstRune(addr) + "***"
	}
	return firstRune(addr[:at]) + "***" + addr[at:]
}

// firstRune returns the first rune of s (or "" if empty).
func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}
