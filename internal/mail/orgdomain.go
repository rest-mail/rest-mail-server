package mail

import "golang.org/x/net/publicsuffix"

// OrgDomain returns the Organizational Domain of domain (RFC 7489 §3.2): its
// public suffix, from the Public Suffix List, plus one label. It returns "" for
// a name that is itself a public suffix, which has no Organizational Domain.
// It is the dmarc.OrgDomainFunc for DMARC discovery, alignment and report
// destinations: the library's default takes the last two labels, which puts
// every registrant under a multi-label suffix such as co.uk in one
// organization.
func OrgDomain(domain string) string {
	org, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return ""
	}
	return org
}
