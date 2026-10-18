// Package grants decides what a holder may do. A grant gives a holder a role
// at a scope (models.Grant); what the holder may do is every capability of
// every role it holds actively, each at the scope it was granted at.
//
// Scopes nest along mail's hierarchy: "*" (the installation) covers every
// domain and mailbox, "domain:<name>" covers that domain and its mailboxes,
// and "mailbox:<address>" covers only that mailbox. Covers is the one place
// scopes are matched, so a grant can later name a counterpart beside its
// target (#341) without hunting for other copies of the rule.
package grants

import (
	"errors"
	"strings"
)

// All is the scope of the whole installation.
const All = "*"

const (
	domainPrefix  = "domain:"
	mailboxPrefix = "mailbox:"
)

// ErrBadScope is a scope that is none of "*", "domain:<name>" or
// "mailbox:<address>".
var ErrBadScope = errors.New(`scope must be "*", "domain:<name>" or "mailbox:<address>"`)

// Domain is the scope of the domain called name.
func Domain(name string) string { return domainPrefix + strings.ToLower(name) }

// Mailbox is the scope of the mailbox at address.
func Mailbox(address string) string { return mailboxPrefix + strings.ToLower(address) }

// ParseScope checks s is a scope and returns it as grants store it, in lower
// case.
func ParseScope(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == All {
		return s, nil
	}
	if name, ok := strings.CutPrefix(s, domainPrefix); ok && validDomain(name) {
		return s, nil
	}
	if address, ok := strings.CutPrefix(s, mailboxPrefix); ok {
		local, domain, found := strings.Cut(address, "@")
		if found && local != "" && !strings.ContainsAny(local, " \t\r\n@") && validDomain(domain) {
			return s, nil
		}
	}
	return "", ErrBadScope
}

// validDomain is a loose check: dot-separated labels of letters, digits and
// hyphens. It keeps out what could never match a hosted domain, not every
// name DNS would refuse.
func validDomain(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// Covers reports whether a grant at scope covers target, both scopes as
// ParseScope returns them: "*" covers everything, a domain covers itself and
// its mailboxes, and a mailbox covers only itself.
func Covers(scope, target string) bool {
	if scope == All || scope == target {
		return true
	}
	domain, ok := strings.CutPrefix(scope, domainPrefix)
	if !ok {
		return false
	}
	address, ok := strings.CutPrefix(target, mailboxPrefix)
	if !ok {
		return false
	}
	at := strings.LastIndexByte(address, '@')
	return at >= 0 && address[at+1:] == domain
}
