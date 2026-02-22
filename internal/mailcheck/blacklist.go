package mailcheck

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// DNSBLs is the list of DNS-based blackhole lists to check (IP-based).
var DNSBLs = []string{
	"zen.spamhaus.org",
	"b.barracudacentral.org",
	"bl.spamcop.net",
	"dnsbl.sorbs.net",
	"spam.dnsbl.sorbs.net",
	"bl.mailspike.net",
	"dnsbl-1.uceprotect.net",
	"psbl.surriel.com",
	"all.s5h.net",
	"rbl.interserver.net",
	"dyna.spamrats.com",
	"noptr.spamrats.com",
}

// DomainBLs is the list of domain-based blackhole lists to check.
var DomainBLs = []string{
	"dbl.spamhaus.org",
	"multi.surbl.org",
	"black.uribl.com",
}

// CheckBlacklists checks the given IP against multiple DNSBLs.
func CheckBlacklists(ctx context.Context, ip string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "IP Blacklists",
		Category: "reputation",
	}

	// Reverse the IP for DNSBL queries
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		result.Status = StatusSkip
		result.Summary = fmt.Sprintf("Cannot check blacklists for non-IPv4 address: %s", ip)
		result.Duration = time.Since(start)
		return result
	}
	reversed := fmt.Sprintf("%s.%s.%s.%s", parts[3], parts[2], parts[1], parts[0])

	type blResult struct {
		name   string
		listed bool
		err    error
	}

	var wg sync.WaitGroup
	results := make(chan blResult, len(DNSBLs))

	for _, bl := range DNSBLs {
		wg.Add(1)
		go func(bl string) {
			defer wg.Done()
			query := fmt.Sprintf("%s.%s", reversed, bl)
			addrs, err := net.DefaultResolver.LookupHost(ctx, query)
			if err != nil {
				results <- blResult{name: bl, listed: false, err: nil}
				return
			}
			// A response (typically 127.0.0.x) means the IP is listed
			listed := len(addrs) > 0
			results <- blResult{name: bl, listed: listed, err: nil}
		}(bl)
	}

	wg.Wait()
	close(results)

	var listed []string
	var clean []string
	var errors []string
	for r := range results {
		if r.err != nil {
			errors = append(errors, fmt.Sprintf("%s: %s", r.name, r.err.Error()))
		} else if r.listed {
			listed = append(listed, r.name)
		} else {
			clean = append(clean, r.name)
		}
	}

	total := len(DNSBLs)
	if len(listed) > 0 {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("LISTED on %d/%d blacklists: %s", len(listed), total, strings.Join(listed, ", "))
		result.Fix = "Your IP is blacklisted. Visit each blacklist's website to request delisting. Common causes: sending spam, open relay, compromised server, or shared IP with a spammer. Fix the root cause first or delisting will be temporary."
	} else {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("Clean on all %d blacklists checked", len(clean))
	}

	if len(errors) > 0 {
		result.Detail = fmt.Sprintf("Lookup errors: %s", strings.Join(errors, "; "))
	}

	result.Duration = time.Since(start)
	return result
}

// CheckDomainBlacklists checks the given domain against domain-based blackhole lists.
func CheckDomainBlacklists(ctx context.Context, domain string) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Domain Blacklists",
		Category: "reputation",
	}

	type blResult struct {
		name   string
		listed bool
	}

	var wg sync.WaitGroup
	results := make(chan blResult, len(DomainBLs))

	for _, bl := range DomainBLs {
		wg.Add(1)
		go func(bl string) {
			defer wg.Done()
			query := fmt.Sprintf("%s.%s", domain, bl)
			addrs, err := net.DefaultResolver.LookupHost(ctx, query)
			if err != nil {
				results <- blResult{name: bl, listed: false}
				return
			}
			// A response means the domain is listed (filter out NXDOMAIN-like results)
			listed := false
			for _, addr := range addrs {
				if strings.HasPrefix(addr, "127.") {
					listed = true
					break
				}
			}
			results <- blResult{name: bl, listed: listed}
		}(bl)
	}

	wg.Wait()
	close(results)

	var listed []string
	var clean int
	for r := range results {
		if r.listed {
			listed = append(listed, r.name)
		} else {
			clean++
		}
	}

	total := len(DomainBLs)
	if len(listed) > 0 {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Domain LISTED on %d/%d domain blacklists: %s", len(listed), total, strings.Join(listed, ", "))
		result.Fix = "Your domain is on a domain blacklist. This is usually caused by hosting spam-related content or having your domain appear in spam emails. Check the specific list for delisting procedures."
	} else {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("Clean on all %d domain blacklists", clean)
	}

	result.Duration = time.Since(start)
	return result
}
