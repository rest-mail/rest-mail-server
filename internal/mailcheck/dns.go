package mailcheck

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// CommonDKIMSelectors is a list of commonly used DKIM selectors to try.
var CommonDKIMSelectors = []string{
	"default", "dkim", "mail", "email", "selector1", "selector2",
	"s1", "s2", "k1", "google", "google2048", "everlytickey1",
	"everlytickey2", "dkim1", "smtp", "ses", "mailjet",
}

// CheckMX looks up MX records for the domain.
func CheckMX(ctx context.Context, domain string) (CheckResult, []string) {
	start := time.Now()
	result := CheckResult{
		Name:     "MX Records",
		Category: "dns",
	}

	mxRecords, err := net.DefaultResolver.LookupMX(ctx, domain)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "No MX records found"
		result.Detail = err.Error()
		result.Fix = "Add an MX record pointing to your mail server: example.com MX 10 mail.example.com"
		result.Duration = time.Since(start)
		return result, nil
	}

	if len(mxRecords) == 0 {
		result.Status = StatusFail
		result.Summary = "No MX records found"
		result.Duration = time.Since(start)
		return result, nil
	}

	var hosts []string
	var parts []string
	for _, mx := range mxRecords {
		host := strings.TrimSuffix(mx.Host, ".")
		hosts = append(hosts, host)
		parts = append(parts, fmt.Sprintf("%s (priority %d)", host, mx.Pref))
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("%d MX record(s): %s", len(mxRecords), strings.Join(parts, ", "))
	result.Duration = time.Since(start)
	return result, hosts
}

// CheckSPF looks up and validates the SPF TXT record.
func CheckSPF(ctx context.Context, domain string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "SPF Record",
		Category: "dns",
	}

	txtRecords, err := net.DefaultResolver.LookupTXT(ctx, domain)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "Failed to query TXT records"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	var spfRecord string
	for _, txt := range txtRecords {
		if strings.HasPrefix(txt, "v=spf1") {
			spfRecord = txt
			break
		}
	}

	if spfRecord == "" {
		result.Status = StatusFail
		result.Summary = "No SPF record found"
		result.Detail = "Add a TXT record like: v=spf1 ip4:YOUR_IP -all"
		result.Fix = "Add a TXT record at your domain: v=spf1 ip4:YOUR_SERVER_IP -all. Without SPF, anyone can forge emails from your domain."
		result.Duration = time.Since(start)
		return result
	}

	result.Summary = spfRecord

	// Check for weak policies
	hasRedirect := strings.Contains(spfRecord, "redirect=")
	if strings.Contains(spfRecord, "+all") {
		result.Status = StatusFail
		result.Detail = "SPF record uses +all which allows anyone to send as your domain"
		result.Fix = "Change +all to -all in your SPF record. +all means ANYONE can send as your domain."
	} else if strings.Contains(spfRecord, "~all") {
		result.Status = StatusWarn
		result.Detail = "SPF uses ~all (softfail); consider -all (hardfail) for stricter enforcement"
		result.Fix = "Change ~all to -all for strict enforcement. Softfail means spoofed emails may still be delivered."
	} else if strings.Contains(spfRecord, "?all") {
		result.Status = StatusWarn
		result.Detail = "SPF uses ?all (neutral); consider -all (hardfail) for stricter enforcement"
	} else if strings.Contains(spfRecord, "-all") {
		result.Status = StatusPass
	} else if hasRedirect {
		result.Status = StatusPass
		result.Detail = "SPF uses redirect= to delegate policy"
	} else {
		result.Status = StatusWarn
		result.Detail = "SPF record does not end with an 'all' mechanism"
	}

	result.Duration = time.Since(start)
	return result
}

// CheckDKIM looks up DKIM records for the given selector(s).
// When brute-forcing common selectors, lookups run in parallel.
func CheckDKIM(ctx context.Context, domain string, selector string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "DKIM Record",
		Category: "dns",
	}

	selectors := CommonDKIMSelectors
	if selector != "" {
		selectors = []string{selector}
	}

	type dkimResult struct {
		selector string
		found    bool
	}

	ch := make(chan dkimResult, len(selectors))
	for _, sel := range selectors {
		go func(sel string) {
			dkimDomain := fmt.Sprintf("%s._domainkey.%s", sel, domain)
			txtRecords, err := net.DefaultResolver.LookupTXT(ctx, dkimDomain)
			if err != nil {
				ch <- dkimResult{selector: sel, found: false}
				return
			}
			for _, txt := range txtRecords {
				if strings.Contains(txt, "v=DKIM1") || strings.Contains(txt, "k=rsa") || strings.Contains(txt, "k=ed25519") || strings.Contains(txt, "p=") {
					ch <- dkimResult{selector: sel, found: true}
					return
				}
			}
			ch <- dkimResult{selector: sel, found: false}
		}(sel)
	}

	var found []string
	for range selectors {
		r := <-ch
		if r.found {
			found = append(found, r.selector)
		}
	}

	if len(found) == 0 {
		if selector != "" {
			result.Status = StatusFail
			result.Summary = fmt.Sprintf("No DKIM record found for selector '%s'", selector)
			result.Detail = fmt.Sprintf("Looked up %s._domainkey.%s", selector, domain)
		} else {
			result.Status = StatusWarn
			result.Summary = "No DKIM record found for common selectors"
			result.Detail = fmt.Sprintf("Tried %d common selectors in parallel. Use --dkim-selector to specify yours.", len(CommonDKIMSelectors))
		}
	} else {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("DKIM found for selector(s): %s", strings.Join(found, ", "))

		// Analyze key strength for the first found selector
		keyInfo := analyzeDKIMKeyStrength(ctx, domain, found[0])
		if keyInfo != "" {
			result.Detail = keyInfo
			// Downgrade status if key is weak
			if strings.Contains(keyInfo, "WEAK") {
				result.Status = StatusFail
			} else if strings.Contains(keyInfo, "UPGRADE") {
				result.Status = StatusWarn
			}
		}
	}

	result.Duration = time.Since(start)
	return result
}

// analyzeDKIMKeyStrength fetches a DKIM record and analyzes the public key size.
// Returns a human-readable string about key strength, or empty if unable to parse.
func analyzeDKIMKeyStrength(ctx context.Context, domain, selector string) string {
	dkimDomain := fmt.Sprintf("%s._domainkey.%s", selector, domain)
	txtRecords, err := net.DefaultResolver.LookupTXT(ctx, dkimDomain)
	if err != nil {
		return ""
	}

	// Concatenate all TXT record strings (DKIM records can be split across multiple strings)
	var fullRecord string
	for _, txt := range txtRecords {
		fullRecord += txt
	}

	// Extract the p= field (public key data)
	pIdx := strings.Index(fullRecord, "p=")
	if pIdx < 0 {
		return ""
	}
	pValue := fullRecord[pIdx+2:]
	// Value ends at semicolon or end of string
	if idx := strings.IndexByte(pValue, ';'); idx >= 0 {
		pValue = pValue[:idx]
	}
	pValue = strings.TrimSpace(pValue)
	// Remove any whitespace within the base64 data
	pValue = strings.Join(strings.Fields(pValue), "")

	if pValue == "" {
		return "Key revoked (empty p= field)"
	}

	// Check key type from k= field
	keyType := "rsa" // default per RFC 6376
	if kIdx := strings.Index(fullRecord, "k="); kIdx >= 0 {
		kValue := fullRecord[kIdx+2:]
		if idx := strings.IndexAny(kValue, "; \t"); idx >= 0 {
			kValue = kValue[:idx]
		}
		keyType = strings.TrimSpace(strings.ToLower(kValue))
	}

	if keyType == "ed25519" {
		return "Ed25519 key (strong, modern algorithm)"
	}

	// Parse RSA public key
	derBytes, err := base64.StdEncoding.DecodeString(pValue)
	if err != nil {
		// Try with padding
		for len(pValue)%4 != 0 {
			pValue += "="
		}
		derBytes, err = base64.StdEncoding.DecodeString(pValue)
		if err != nil {
			return fmt.Sprintf("Cannot decode key: %s", err.Error())
		}
	}

	pub, err := x509.ParsePKIXPublicKey(derBytes)
	if err != nil {
		// Try parsing as raw RSA public key
		rsaKey, err2 := x509.ParsePKCS1PublicKey(derBytes)
		if err2 != nil {
			return ""
		}
		pub = rsaKey
	}

	if rsaKey, ok := pub.(*rsa.PublicKey); ok {
		bits := rsaKey.N.BitLen()
		if bits < 1024 {
			return fmt.Sprintf("WEAK: RSA %d-bit key — easily crackable, must upgrade immediately", bits)
		} else if bits < 2048 {
			return fmt.Sprintf("UPGRADE: RSA %d-bit key — should upgrade to 2048-bit", bits)
		}
		return fmt.Sprintf("RSA %d-bit key (strong)", bits)
	}

	return ""
}

// CheckDMARC looks up and validates the DMARC record.
func CheckDMARC(ctx context.Context, domain string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "DMARC Record",
		Category: "dns",
	}

	dmarcDomain := "_dmarc." + domain
	txtRecords, err := net.DefaultResolver.LookupTXT(ctx, dmarcDomain)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "No DMARC record found"
		result.Detail = fmt.Sprintf("Add a TXT record at %s like: v=DMARC1; p=reject; rua=mailto:postmaster@%s", dmarcDomain, domain)
		result.Duration = time.Since(start)
		return result
	}

	var dmarcRecord string
	for _, txt := range txtRecords {
		if strings.HasPrefix(txt, "v=DMARC1") {
			dmarcRecord = txt
			break
		}
	}

	if dmarcRecord == "" {
		result.Status = StatusFail
		result.Summary = "No DMARC record found"
		result.Detail = fmt.Sprintf("TXT records exist at %s but none start with v=DMARC1", dmarcDomain)
		result.Duration = time.Since(start)
		return result
	}

	result.Summary = dmarcRecord

	// Parse policy
	lower := strings.ToLower(dmarcRecord)
	if strings.Contains(lower, "p=none") {
		result.Status = StatusWarn
		result.Detail = "DMARC policy is p=none (monitoring only); consider p=quarantine or p=reject"
		result.Fix = "Upgrade DMARC policy: start with p=quarantine, then move to p=reject once you confirm legitimate mail passes SPF/DKIM."
	} else if strings.Contains(lower, "p=quarantine") {
		result.Status = StatusPass
		result.Detail = "DMARC policy is p=quarantine; consider upgrading to p=reject"
	} else if strings.Contains(lower, "p=reject") {
		result.Status = StatusPass
	} else {
		result.Status = StatusWarn
		result.Detail = "Could not determine DMARC policy"
	}

	// Check for rua (aggregate reporting)
	if !strings.Contains(lower, "rua=") {
		if result.Detail != "" {
			result.Detail += "; "
		}
		result.Detail += "No rua= tag — you won't receive aggregate DMARC reports"
	}

	result.Duration = time.Since(start)
	return result
}

// CheckPTR performs reverse DNS lookup on the given IP.
func CheckPTR(ctx context.Context, ip string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Reverse DNS (PTR)",
		Category: "dns",
	}

	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("No PTR record for %s", ip)
		result.Detail = err.Error()
		result.Fix = "Add a PTR record for your mail server IP. Contact your hosting provider — PTR records are set by the IP owner, not in your DNS zone. Without PTR, many servers will reject your mail."
		result.Duration = time.Since(start)
		return result
	}

	if len(names) == 0 {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("No PTR record for %s", ip)
		result.Duration = time.Since(start)
		return result
	}

	ptr := strings.TrimSuffix(names[0], ".")
	result.Status = StatusPass
	result.Summary = fmt.Sprintf("%s → %s", ip, ptr)
	result.Duration = time.Since(start)
	return result
}

// CheckDANE looks up TLSA records for the MX host using miekg/dns for proper type 52 queries.
func CheckDANE(ctx context.Context, mxHost string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "DANE/TLSA",
		Category: "dns",
	}

	tlsaDomain := fmt.Sprintf("_25._tcp.%s", mxHost)

	resp, err := QueryDNS(tlsaDomain, 52, timeout) // 52 = TLSA record type
	if err != nil || resp == nil {
		result.Status = StatusSkip
		result.Summary = "No DANE/TLSA record found (optional)"
		result.Detail = "DANE provides additional TLS authentication via DNS. Requires DNSSEC to be effective."
		result.Duration = time.Since(start)
		return result
	}

	var tlsaRecords []string
	for _, rr := range resp.Answer {
		if tlsa, ok := rr.(*dns.TLSA); ok {
			usage := tlsaUsageName(tlsa.Usage)
			selector := tlsaSelectorName(tlsa.Selector)
			matching := tlsaMatchingName(tlsa.MatchingType)
			certData := tlsa.Certificate
			if len(certData) > 16 {
				certData = certData[:16] + "..."
			}
			tlsaRecords = append(tlsaRecords, fmt.Sprintf("%s/%s/%s: %s", usage, selector, matching, certData))
		}
	}

	if len(tlsaRecords) == 0 {
		result.Status = StatusSkip
		result.Summary = "No DANE/TLSA record found (optional)"
		result.Detail = "DANE provides additional TLS authentication via DNS. Requires DNSSEC to be effective."
		result.Duration = time.Since(start)
		return result
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("%d TLSA record(s) at %s", len(tlsaRecords), tlsaDomain)
	result.Detail = strings.Join(tlsaRecords, "\n")
	result.Duration = time.Since(start)
	return result
}

func tlsaUsageName(u uint8) string {
	switch u {
	case 0:
		return "PKIX-TA"
	case 1:
		return "PKIX-EE"
	case 2:
		return "DANE-TA"
	case 3:
		return "DANE-EE"
	default:
		return fmt.Sprintf("usage-%d", u)
	}
}

func tlsaSelectorName(s uint8) string {
	switch s {
	case 0:
		return "Full"
	case 1:
		return "SPKI"
	default:
		return fmt.Sprintf("sel-%d", s)
	}
}

func tlsaMatchingName(m uint8) string {
	switch m {
	case 0:
		return "Exact"
	case 1:
		return "SHA-256"
	case 2:
		return "SHA-512"
	default:
		return fmt.Sprintf("match-%d", m)
	}
}

// CheckMTASTS checks for MTA-STS TXT record and fetches/validates the HTTPS policy.
func CheckMTASTS(ctx context.Context, domain string, timeout time.Duration, mxHosts []string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "MTA-STS",
		Category: "dns",
	}

	stsDomain := "_mta-sts." + domain
	txtRecords, err := net.DefaultResolver.LookupTXT(ctx, stsDomain)
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "No MTA-STS record found (optional)"
		result.Detail = "MTA-STS (RFC 8461) enforces TLS for inbound mail. Recommended but not required."
		result.Fix = "Add a TXT record at _mta-sts." + domain + " with v=STSv1; id=<unique>; then serve a policy at https://mta-sts." + domain + "/.well-known/mta-sts.txt"
		result.Duration = time.Since(start)
		return result
	}

	var stsRecord string
	for _, txt := range txtRecords {
		if strings.HasPrefix(txt, "v=STSv1") {
			stsRecord = txt
			break
		}
	}

	if stsRecord == "" {
		result.Status = StatusSkip
		result.Summary = "No MTA-STS record found (optional)"
		result.Duration = time.Since(start)
		return result
	}

	// Fetch the HTTPS policy
	policyURL := fmt.Sprintf("https://mta-sts.%s/.well-known/mta-sts.txt", domain)
	client := &httpClient{Timeout: timeout}
	policyBody, fetchErr := client.Get(policyURL)

	if fetchErr != nil {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("TXT record found (%s) but policy unreachable", stsRecord)
		result.Detail = fmt.Sprintf("Failed to fetch %s: %s", policyURL, fetchErr.Error())
		result.Fix = "Serve your MTA-STS policy at " + policyURL + " over HTTPS with a valid certificate."
		result.Duration = time.Since(start)
		return result
	}

	// Parse policy
	policy := parseMTASTSPolicy(policyBody)

	var details []string
	details = append(details, fmt.Sprintf("DNS: %s", stsRecord))

	if policy.mode == "" {
		result.Status = StatusWarn
		result.Summary = "MTA-STS policy fetched but missing mode"
		result.Detail = strings.Join(details, "\n")
		result.Duration = time.Since(start)
		return result
	}

	details = append(details, fmt.Sprintf("Mode: %s", policy.mode))
	if policy.maxAge != "" {
		details = append(details, fmt.Sprintf("Max-Age: %s", policy.maxAge))
	}
	if len(policy.mx) > 0 {
		details = append(details, fmt.Sprintf("MX patterns: %s", strings.Join(policy.mx, ", ")))
	}

	// Validate MX patterns match actual MX hosts
	if len(mxHosts) > 0 && len(policy.mx) > 0 {
		for _, host := range mxHosts {
			if !matchesMTASTSMX(host, policy.mx) {
				details = append(details, fmt.Sprintf("WARNING: MX host %s does not match any policy mx: pattern", host))
			}
		}
	}

	switch policy.mode {
	case "enforce":
		result.Status = StatusPass
		result.Summary = "MTA-STS policy: enforce"
	case "testing":
		result.Status = StatusWarn
		result.Summary = "MTA-STS policy: testing (not enforcing)"
		result.Fix = "Upgrade MTA-STS mode from 'testing' to 'enforce' once you confirm all senders can deliver over TLS."
	case "none":
		result.Status = StatusWarn
		result.Summary = "MTA-STS policy: none (disabled)"
	default:
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("MTA-STS policy: unknown mode '%s'", policy.mode)
	}

	result.Detail = strings.Join(details, "\n")
	result.Duration = time.Since(start)
	return result
}

type mtaSTSPolicy struct {
	version string
	mode    string
	maxAge  string
	mx      []string
}

func parseMTASTSPolicy(body string) mtaSTSPolicy {
	var p mtaSTSPolicy
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "version":
			p.version = val
		case "mode":
			p.mode = val
		case "max_age":
			p.maxAge = val
		case "mx":
			p.mx = append(p.mx, val)
		}
	}
	return p
}

// matchesMTASTSMX checks if a hostname matches any MTA-STS mx: pattern (supports wildcards).
func matchesMTASTSMX(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if strings.HasPrefix(pattern, "*.") {
			// Wildcard: *.example.com matches mail.example.com
			suffix := pattern[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) || host == pattern[2:] {
				return true
			}
		} else if host == pattern {
			return true
		}
	}
	return false
}

// CheckTLSRPT checks for TLS-RPT TXT record and validates the rua= reporting URI.
func CheckTLSRPT(ctx context.Context, domain string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "TLS-RPT",
		Category: "dns",
	}

	rptDomain := "_smtp._tls." + domain
	txtRecords, err := net.DefaultResolver.LookupTXT(ctx, rptDomain)
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "No TLS-RPT record found (optional)"
		result.Detail = "TLS-RPT (RFC 8460) enables TLS failure reporting. Recommended."
		result.Fix = "Add a TXT record at _smtp._tls." + domain + ": v=TLSRPTv1; rua=mailto:tls-reports@" + domain
		result.Duration = time.Since(start)
		return result
	}

	var rptRecord string
	for _, txt := range txtRecords {
		if strings.HasPrefix(txt, "v=TLSRPTv1") {
			rptRecord = txt
			break
		}
	}

	if rptRecord == "" {
		result.Status = StatusSkip
		result.Summary = "No TLS-RPT record found (optional)"
		result.Duration = time.Since(start)
		return result
	}

	// Parse rua= field
	ruaIdx := strings.Index(rptRecord, "rua=")
	if ruaIdx < 0 {
		result.Status = StatusWarn
		result.Summary = "TLS-RPT record missing rua= reporting address"
		result.Detail = rptRecord
		result.Fix = "Add a rua= field to your TLS-RPT record: v=TLSRPTv1; rua=mailto:tls-reports@" + domain
		result.Duration = time.Since(start)
		return result
	}

	ruaValue := rptRecord[ruaIdx+4:]
	// Value ends at semicolon or end of string
	if idx := strings.Index(ruaValue, ";"); idx >= 0 {
		ruaValue = ruaValue[:idx]
	}
	ruaValue = strings.TrimSpace(ruaValue)

	var details []string
	details = append(details, rptRecord)

	if strings.HasPrefix(ruaValue, "mailto:") {
		addr := ruaValue[7:]
		if strings.Contains(addr, "@") {
			details = append(details, fmt.Sprintf("Reports sent to: %s", addr))
			result.Status = StatusPass
			result.Summary = fmt.Sprintf("TLS-RPT configured, reports to %s", addr)
		} else {
			result.Status = StatusWarn
			result.Summary = "TLS-RPT rua= has malformed mailto address"
		}
	} else if strings.HasPrefix(ruaValue, "https://") {
		details = append(details, fmt.Sprintf("Reports posted to: %s", ruaValue))
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("TLS-RPT configured, reports to HTTPS endpoint")
	} else {
		result.Status = StatusWarn
		result.Summary = "TLS-RPT rua= must be mailto: or https://"
		result.Detail = fmt.Sprintf("Found: rua=%s", ruaValue)
		result.Duration = time.Since(start)
		return result
	}

	result.Detail = strings.Join(details, "\n")
	result.Duration = time.Since(start)
	return result
}

// CheckDNSSEC checks whether DNSSEC is enabled for the domain by examining the AD flag.
func CheckDNSSEC(ctx context.Context, domain string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "DNSSEC",
		Category: "dns",
	}

	resp, ad, err := QueryDNSSEC(domain, dns.TypeMX, timeout)
	if err != nil {
		result.Status = StatusWarn
		result.Summary = "Could not query DNSSEC status"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	if ad {
		result.Status = StatusPass
		result.Summary = "DNSSEC validated (AD flag set)"
		result.Detail = "DNS responses are cryptographically authenticated. This protects SPF, DKIM, DMARC, and DANE records from spoofing."
	} else {
		// Check if DNSKEY records exist (domain has DNSSEC but our resolver doesn't validate)
		dnskeyResp, err := QueryDNS(domain, dns.TypeDNSKEY, timeout)
		if err == nil && dnskeyResp != nil && len(dnskeyResp.Answer) > 0 {
			result.Status = StatusWarn
			result.Summary = "DNSKEY records present but AD flag not set"
			result.Detail = "The domain appears to have DNSSEC configured, but the resolver did not set the Authenticated Data flag. This may indicate a broken DNSSEC chain or an unvalidating resolver."
		} else {
			result.Status = StatusWarn
			result.Summary = "DNSSEC not enabled"
			result.Detail = "Without DNSSEC, an attacker can spoof DNS responses and undermine SPF, DKIM, DMARC, and DANE entirely. DNSSEC is required for DANE to be effective."
			result.Fix = "Enable DNSSEC at your domain registrar and DNS provider. Most registrars support one-click DNSSEC enabling."
		}
	}

	_ = resp // used for AD flag check above
	result.Duration = time.Since(start)
	return result
}

// CheckCAA looks up Certificate Authority Authorization records.
func CheckCAA(ctx context.Context, domain string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "CAA Records",
		Category: "dns",
	}

	resp, err := QueryDNS(domain, dns.TypeCAA, timeout)
	if err != nil || resp == nil {
		result.Status = StatusSkip
		result.Summary = "No CAA records found (optional)"
		result.Detail = "CAA records specify which Certificate Authorities can issue certificates for your domain."
		result.Duration = time.Since(start)
		return result
	}

	var records []string
	for _, rr := range resp.Answer {
		if caa, ok := rr.(*dns.CAA); ok {
			records = append(records, fmt.Sprintf("%s %s (flags=%d)", caa.Tag, caa.Value, caa.Flag))
		}
	}

	if len(records) == 0 {
		result.Status = StatusSkip
		result.Summary = "No CAA records found (optional)"
		result.Detail = "CAA records restrict which CAs can issue certs for your domain. Without them, any CA can issue a certificate."
		result.Fix = "Add CAA records to restrict certificate issuance: " + domain + " CAA 0 issue \"letsencrypt.org\""
		result.Duration = time.Since(start)
		return result
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("%d CAA record(s)", len(records))
	result.Detail = strings.Join(records, "\n")
	result.Duration = time.Since(start)
	return result
}

// CheckBIMI looks up Brand Indicators for Message Identification records.
func CheckBIMI(ctx context.Context, domain string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "BIMI Record",
		Category: "dns",
	}

	bimiDomain := "default._bimi." + domain
	txtRecords, err := net.DefaultResolver.LookupTXT(ctx, bimiDomain)
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "No BIMI record found (optional)"
		result.Detail = "BIMI displays your brand logo next to emails in supported clients (Gmail, Apple Mail). Requires DMARC p=quarantine or p=reject."
		result.Duration = time.Since(start)
		return result
	}

	var bimiRecord string
	for _, txt := range txtRecords {
		if strings.HasPrefix(txt, "v=BIMI1") {
			bimiRecord = txt
			break
		}
	}

	if bimiRecord == "" {
		result.Status = StatusSkip
		result.Summary = "No BIMI record found (optional)"
		result.Duration = time.Since(start)
		return result
	}

	var details []string
	details = append(details, bimiRecord)

	// Parse l= (logo URL)
	if lIdx := strings.Index(bimiRecord, "l="); lIdx >= 0 {
		lValue := bimiRecord[lIdx+2:]
		if idx := strings.IndexAny(lValue, "; "); idx >= 0 {
			lValue = lValue[:idx]
		}
		if lValue != "" {
			details = append(details, fmt.Sprintf("Logo: %s", lValue))
		}
	}

	// Parse a= (VMC/authority URL)
	if aIdx := strings.Index(bimiRecord, "a="); aIdx >= 0 {
		aValue := bimiRecord[aIdx+2:]
		if idx := strings.IndexAny(aValue, "; "); idx >= 0 {
			aValue = aValue[:idx]
		}
		if aValue != "" {
			details = append(details, fmt.Sprintf("VMC: %s", aValue))
		}
	}

	result.Status = StatusPass
	result.Summary = "BIMI record found"
	result.Detail = strings.Join(details, "\n")
	result.Duration = time.Since(start)
	return result
}

// CheckFCrDNS performs forward-confirmed reverse DNS: verifies that the PTR hostname
// resolves back to the original IP address.
func CheckFCrDNS(ctx context.Context, ip string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Forward-Confirmed rDNS",
		Category: "dns",
	}

	// Step 1: Reverse lookup
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("No PTR record for %s (cannot verify FCrDNS)", ip)
		result.Duration = time.Since(start)
		return result
	}

	ptrHostname := strings.TrimSuffix(names[0], ".")

	// Step 2: Forward lookup — resolve PTR hostname back to IP
	addrs, err := net.DefaultResolver.LookupHost(ctx, ptrHostname)
	if err != nil {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("PTR %s does not resolve forward", ptrHostname)
		result.Detail = err.Error()
		result.Fix = "Ensure the PTR hostname has an A/AAAA record that resolves back to the original IP. Both the PTR and forward records must agree."
		result.Duration = time.Since(start)
		return result
	}

	// Step 3: Check if original IP is in the forward results
	for _, addr := range addrs {
		if addr == ip {
			result.Status = StatusPass
			result.Summary = fmt.Sprintf("FCrDNS verified: %s → %s → %s", ip, ptrHostname, ip)
			result.Duration = time.Since(start)
			return result
		}
	}

	result.Status = StatusFail
	result.Summary = fmt.Sprintf("FCrDNS mismatch: %s → %s → %s (expected %s)", ip, ptrHostname, strings.Join(addrs, ","), ip)
	result.Fix = "The PTR hostname resolves to a different IP than the original. Update either the PTR record or the A record so they match."
	result.Duration = time.Since(start)
	return result
}

// CheckIPv6Readiness checks if the MX host has IPv6 connectivity.
func CheckIPv6Readiness(ctx context.Context, mxHost string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "IPv6 Readiness",
		Category: "dns",
	}

	// Check for AAAA records
	ips, err := net.DefaultResolver.LookupHost(ctx, mxHost)
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "Cannot resolve MX host"
		result.Duration = time.Since(start)
		return result
	}

	var ipv6Addrs []string
	for _, ip := range ips {
		if strings.Contains(ip, ":") {
			ipv6Addrs = append(ipv6Addrs, ip)
		}
	}

	if len(ipv6Addrs) == 0 {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("No AAAA record for %s", mxHost)
		result.Detail = "IPv6 connectivity is increasingly important for mail delivery. Major providers like Gmail prefer IPv6 when available."
		result.Fix = "Add an AAAA record for your mail server hostname pointing to its IPv6 address."
		result.Duration = time.Since(start)
		return result
	}

	// Check IPv6 PTR
	var details []string
	details = append(details, fmt.Sprintf("AAAA: %s", strings.Join(ipv6Addrs, ", ")))

	for _, ipv6 := range ipv6Addrs {
		names, err := net.DefaultResolver.LookupAddr(ctx, ipv6)
		if err != nil || len(names) == 0 {
			details = append(details, fmt.Sprintf("No PTR for %s", ipv6))
			result.Status = StatusWarn
			result.Fix = "Add PTR records for your IPv6 addresses. Missing IPv6 PTR can cause mail rejection."
		} else {
			details = append(details, fmt.Sprintf("PTR %s → %s", ipv6, strings.TrimSuffix(names[0], ".")))
		}
	}

	if result.Status != StatusWarn {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("IPv6 ready: %d AAAA record(s) with PTR", len(ipv6Addrs))
	} else {
		result.Summary = fmt.Sprintf("IPv6 partially ready: %d AAAA record(s) but missing PTR", len(ipv6Addrs))
	}
	result.Detail = strings.Join(details, "\n")
	result.Duration = time.Since(start)
	return result
}

// CheckAutoconfig checks for Mozilla Autoconfig and Microsoft Autodiscover service endpoints.
func CheckAutoconfig(ctx context.Context, domain string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Client Autoconfig",
		Category: "dns",
	}

	var found []string
	var details []string

	// Check SRV records for IMAP and submission
	srvChecks := []struct {
		service string
		label   string
	}{
		{"_imaps._tcp." + domain, "IMAPS SRV"},
		{"_submission._tcp." + domain, "Submission SRV"},
		{"_imap._tcp." + domain, "IMAP SRV"},
		{"_pop3s._tcp." + domain, "POP3S SRV"},
	}

	for _, srv := range srvChecks {
		_, addrs, err := net.DefaultResolver.LookupSRV(ctx, "", "", srv.service)
		if err == nil && len(addrs) > 0 {
			target := strings.TrimSuffix(addrs[0].Target, ".")
			found = append(found, srv.label)
			details = append(details, fmt.Sprintf("%s → %s:%d", srv.label, target, addrs[0].Port))
		}
	}

	// Check Mozilla autoconfig URL
	client := &httpClient{Timeout: timeout}
	autoconfigURL := fmt.Sprintf("https://autoconfig.%s/mail/config-v1.1.xml", domain)
	body, err := client.Get(autoconfigURL)
	if err == nil && strings.Contains(body, "clientConfig") {
		found = append(found, "Mozilla Autoconfig")
		details = append(details, fmt.Sprintf("Mozilla: %s", autoconfigURL))
	}

	// Check Microsoft Autodiscover URL
	autodiscoverURL := fmt.Sprintf("https://autodiscover.%s/autodiscover/autodiscover.xml", domain)
	body, err = client.Get(autodiscoverURL)
	if err == nil && (strings.Contains(body, "Autodiscover") || strings.Contains(body, "autodiscover")) {
		found = append(found, "Microsoft Autodiscover")
		details = append(details, fmt.Sprintf("Microsoft: %s", autodiscoverURL))
	}

	if len(found) == 0 {
		result.Status = StatusSkip
		result.Summary = "No autoconfig/autodiscover endpoints found (optional)"
		result.Detail = "Client autoconfig helps email clients like Thunderbird and Outlook automatically configure server settings."
		result.Fix = "Add SRV records for _imaps._tcp and _submission._tcp, or host autoconfig XML at autoconfig." + domain + "/mail/config-v1.1.xml"
		result.Duration = time.Since(start)
		return result
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("%d autoconfig method(s): %s", len(found), strings.Join(found, ", "))
	result.Detail = strings.Join(details, "\n")
	result.Duration = time.Since(start)
	return result
}

// ResolveMXIPs resolves the IPs for a given MX hostname, preferring IPv4.
func ResolveMXIPs(ctx context.Context, host string) []string {
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	// Sort IPv4 addresses first (needed for DNSBL lookups)
	var ipv4, ipv6 []string
	for _, ip := range ips {
		if strings.Contains(ip, ":") {
			ipv6 = append(ipv6, ip)
		} else {
			ipv4 = append(ipv4, ip)
		}
	}
	return append(ipv4, ipv6...)
}

// FirstIPv4 returns the first IPv4 address from a list, or empty string.
func FirstIPv4(ips []string) string {
	for _, ip := range ips {
		if !strings.Contains(ip, ":") {
			return ip
		}
	}
	return ""
}
