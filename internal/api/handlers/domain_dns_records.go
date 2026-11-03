package handlers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dns"
	"github.com/rest-mail/rest-mail-server/internal/domainrecords"
)

// domainPublishing is what the DNS check needs to work out the records a domain must publish.
type domainPublishing struct {
	// mailHost is GATEWAY_HOSTNAME, as autoconfig and RESTMAIL discovery use it; empty when it
	// is not a fully-qualified name, and then the records that name it have no value yet.
	mailHost  string
	masterKey string
	ports     dns.ClientPorts
}

// SetMailServer tells the DNS check which server the domain's records must point at: the
// settings `restmail-api dns` runs with, so the page and that command list the same records.
func (h *DomainHandler) SetMailServer(gatewayHostname, masterKey string, ports dns.ClientPorts) {
	h.publish = domainPublishing{masterKey: masterKey, ports: ports}
	if host, err := validateDomainName(gatewayHostname); err == nil {
		h.publish.mailHost = host
	}
}

const (
	statusOK        = "ok"
	statusMissing   = "missing"
	statusDifferent = "different"
	statusError     = "error"

	// dnsCheckBudget bounds one check's lookups, which run together, so a resolver that has
	// stopped answering costs this and no more.
	dnsCheckBudget = 10 * time.Second

	// unknownMailHost stands in for the mail host when the server has none configured, so the
	// records that would name it can be recognised. RFC 6761 §6.4 reserves .invalid.
	unknownMailHost = "mail-host-unknown.invalid"

	valueDiffers   = "The published value differs from the value to publish."
	lookupFailed   = "The lookup failed, so this record could not be checked. Try again in a moment."
	noMailHostSet  = "GATEWAY_HOSTNAME is not a full host name on this server, so there is no value to compare this record with. Ask the person who runs the server to set it."
	ownerOfAddress = "Whoever owns the address sets this, usually the hosting provider, not the domain's DNS."
)

// txtKind describes the TXT records the check tells apart: by the name they are published
// at and the text they start with, since other TXT records can share the name (RFC 7208 §4.5).
type txtKind struct {
	label, prefix string
	noRecord      string // what to say when there is nothing at the name
	otherRecords  string // what to say when only other TXT records are there
	onlyOne       string // why a second record of this kind is wrong
}

func (k txtKind) has(txt string) bool {
	return strings.HasPrefix(strings.ToLower(txt), strings.ToLower(k.prefix))
}

func txtKindOf(domain, name string) txtKind {
	switch {
	case name == domain:
		none := "No SPF record (v=spf1...) found in TXT records"
		return txtKind{label: "SPF", prefix: "v=spf1", noRecord: none, otherRecords: none,
			onlyOne: "More than one SPF record is published, and a domain may have only one (RFC 7208 §3.2)."}
	case strings.HasPrefix(name, "_dmarc."):
		none := "No DMARC record found"
		return txtKind{label: "DMARC", prefix: "v=DMARC1", noRecord: none, otherRecords: none,
			onlyOne: "More than one DMARC record is published, and a domain may have only one."}
	case strings.HasPrefix(name, "_mta-sts."):
		return txtKind{label: "MTA-STS", prefix: "v=STSv1", noRecord: "No MTA-STS TXT record found",
			otherRecords: "TXT records exist but none start with v=STSv1",
			onlyOne:      "More than one MTA-STS record is published, and senders treat that as none (RFC 8461 §3.1)."}
	case strings.HasPrefix(name, "_smtp._tls."):
		return txtKind{label: "TLS-RPT", prefix: "v=TLSRPTv1", noRecord: "No TLS-RPT TXT record found",
			otherRecords: "TXT records exist but none start with v=TLSRPTv1",
			onlyOne:      "More than one TLS-RPT record is published, and senders treat that as none (RFC 8460 §3)."}
	}
	selector, _, _ := strings.Cut(name, "._domainkey.")
	none := fmt.Sprintf("No DKIM record found for selector %q", selector)
	return txtKind{label: "DKIM", noRecord: none, otherRecords: none,
		onlyOne: "More than one TXT record is published under this selector, and it should hold only the key."}
}

// sameTXT reports whether two values of a TXT record mean the same: a tag list doesn't care
// about a trailing semicolon or the space around tags (RFC 6376 §3.2), and DKIM allows space
// inside the key too (§3.6.1).
func sameTXT(label, a, b string) bool {
	if label == "DKIM" {
		a, b = strings.Join(strings.Fields(a), ""), strings.Join(strings.Fields(b), "")
	}
	return canonicalTXT(a) == canonicalTXT(b)
}

func canonicalTXT(value string) string {
	var tags []string
	for _, tag := range strings.Split(value, ";") {
		if tag = strings.Join(strings.Fields(tag), " "); tag != "" {
			tags = append(tags, tag)
		}
	}
	return strings.Join(tags, "; ")
}

// dnsName is a name as the DNS compares it: without case or the final dot.
func dnsName(name string) string {
	if name == "." {
		return name
	}
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// absoluteLookup makes a lookup ask for the name exactly as given. A name without its final dot
// is relative, so the resolver also tries it under the host's search domains: a pod with ndots:5
// tries those first, and any host tries them after an NXDOMAIN, which is the normal answer for an
// optional record such as _mta-sts. The DNS check is about the public name, nothing else.
func absoluteLookup[T any](lookup func(context.Context, string) (T, error)) func(context.Context, string) (T, error) {
	return func(ctx context.Context, name string) (T, error) {
		if net.ParseIP(name) == nil && !strings.HasSuffix(name, ".") {
			name += "."
		}
		return lookup(ctx, name)
	}
}

func notFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// checkPublishedRecords looks up everything the domain must publish, at the same time and
// each on its own, and compares it with the list.
func (h *DomainHandler) checkPublishedRecords(ctx context.Context, domain models.Domain, name string) ([]DNSRecordCheck, error) {
	var policies []models.MTASTSPolicy
	if err := h.db.WithContext(ctx).Where("domain_id = ? AND active = ?", domain.ID, true).Limit(1).Find(&policies).Error; err != nil {
		return nil, fmt.Errorf("read the domain's MTA-STS policy: %w", err)
	}
	var rotating []models.DKIMRotationKey
	if err := h.db.WithContext(ctx).Where("domain_id = ?", domain.ID).Order("slot").Find(&rotating).Error; err != nil {
		return nil, fmt.Errorf("read the domain's rotating DKIM keys: %w", err)
	}

	host := h.publish.mailHost
	if host == "" {
		host = unknownMailHost
	}
	list := domainrecords.ForDomain(domain, host, "", h.publish.masterKey, h.publish.ports)
	signing := len(list.Records)
	list.Records = append(list.Records, domainrecords.Rotation(domain, rotating, h.publish.masterKey)...)
	if len(policies) > 0 {
		domainrecords.AddMTASTS(&list, domain, policies[0], host)
	}

	ctx, cancel := context.WithTimeout(ctx, dnsCheckBudget)
	defer cancel()

	// Each check is one slot, filled by its own lookup, so the order is the list's.
	var work []func() DNSRecordCheck
	for _, record := range list.Records {
		work = append(work, func() DNSRecordCheck { return h.checkRecord(ctx, name, record) })
	}
	if !listsSigningKey(list.Records[:signing], name) {
		work = append(work, func() DNSRecordCheck { return signingKeyProblem(domain, name) })
	}
	if len(policies) == 0 {
		work = append(work, func() DNSRecordCheck { return h.checkMTASTSWithoutPolicy(ctx, name) })
	} else if !listsType(list.Records, "CNAME") {
		work = append(work, func() DNSRecordCheck {
			return DNSRecordCheck{Type: "MTA-STS", Name: "_mta-sts." + name, Status: statusError,
				Message: "The domain's MTA-STS policy cannot be served, so it has no record to publish."}
		})
	}
	checks := make([]DNSRecordCheck, len(work))
	var wg sync.WaitGroup
	for i, check := range work {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checks[i] = check()
		}()
	}
	wg.Wait()
	if h.publish.mailHost != "" {
		checks = append(checks, h.checkMailHostAddress(ctx)...)
	}
	return checks, nil
}

func listsSigningKey(records []dns.DNSRecord, domain string) bool {
	for _, r := range records {
		if r.Type == "TXT" && txtKindOf(domain, r.Name).label == "DKIM" {
			return true
		}
	}
	return false
}

func listsType(records []dns.DNSRecord, typ string) bool {
	for _, r := range records {
		if r.Type == typ {
			return true
		}
	}
	return false
}

// signingKeyProblem is the DKIM line for a domain whose key gives no record to publish.
func signingKeyProblem(domain models.Domain, name string) DNSRecordCheck {
	if domain.DKIMSelector == "" || domain.DKIMPrivateKey == "" {
		return DNSRecordCheck{Type: "DKIM", Name: name, Status: statusMissing,
			Message: "The domain has no DKIM key, so its mail is not signed"}
	}
	return DNSRecordCheck{Type: "DKIM", Name: domain.DKIMSelector + "._domainkey." + name, Status: statusError,
		Message: "REST Mail could not read this domain's DKIM key, so it has no value to compare. Ask the person who runs the server."}
}

// checkRecord compares one listed record with what the DNS answers.
func (h *DomainHandler) checkRecord(ctx context.Context, domain string, record dns.DNSRecord) DNSRecordCheck {
	var check DNSRecordCheck
	switch record.Type {
	case "MX":
		check = h.checkMX(ctx, record)
	case "SRV":
		check = h.checkSRV(ctx, record)
	case "CNAME":
		check = h.checkCNAME(ctx, record)
	default:
		check = h.checkTXT(ctx, domain, record)
	}
	if strings.Contains(record.Value, unknownMailHost) {
		check.Expected, check.Status, check.Message = "", statusError, noMailHostSet
	}
	return check
}

func (h *DomainHandler) checkTXT(ctx context.Context, domain string, record dns.DNSRecord) DNSRecordCheck {
	kind := txtKindOf(domain, record.Name)
	check := DNSRecordCheck{Type: kind.label, Name: record.Name, Expected: record.Value}
	found, err := h.lookupTXT(ctx, record.Name)
	if err != nil && !notFound(err) {
		check.Status, check.Message = statusError, lookupFailed
		return check
	}
	var ofKind []string
	for _, txt := range found {
		if kind.has(txt) {
			ofKind = append(ofKind, txt)
		}
	}
	switch {
	case len(found) == 0:
		check.Status, check.Message = statusMissing, kind.noRecord
	case len(ofKind) == 0:
		check.Status, check.Message = statusMissing, kind.otherRecords
	case len(ofKind) > 1:
		check.Status, check.Message = statusDifferent, kind.onlyOne
	case !sameTXT(kind.label, ofKind[0], record.Value):
		check.Status, check.Message = statusDifferent, valueDiffers
	default:
		check.Status = statusOK
	}
	check.Value = strings.Join(ofKind, ", ")
	if check.Status == statusMissing && kind.label == "MTA-STS" {
		check.Message += ". Before publishing it, the policy host needs its HTTPS route and certificate."
	}
	return check
}

// checkMTASTSWithoutPolicy is the MTA-STS line for a domain with no active policy, which has
// nothing to publish: the record is optional, and one published anyway promises a policy no
// sender can fetch.
func (h *DomainHandler) checkMTASTSWithoutPolicy(ctx context.Context, domain string) DNSRecordCheck {
	name := "_mta-sts." + domain
	kind := txtKindOf(domain, name)
	check := DNSRecordCheck{Type: kind.label, Name: name, Status: statusMissing}
	found, err := h.lookupTXT(ctx, name)
	if err != nil && !notFound(err) {
		check.Status, check.Message = statusError, lookupFailed
		return check
	}
	if len(found) == 0 {
		check.Message = kind.noRecord + ". It is optional, and this domain has no active policy yet."
		return check
	}
	for _, txt := range found {
		if kind.has(txt) {
			check.Status, check.Value = statusDifferent, txt
			check.Message = "A record is published, but this domain has no active MTA-STS policy for senders to fetch."
			return check
		}
	}
	check.Message = kind.otherRecords
	return check
}

func (h *DomainHandler) checkMX(ctx context.Context, record dns.DNSRecord) DNSRecordCheck {
	check := DNSRecordCheck{Type: "MX", Name: record.Name, Expected: fmt.Sprintf("%d %s", record.Priority, record.Value)}
	found, err := h.lookupMX(ctx, record.Name)
	if err != nil && !notFound(err) {
		check.Status, check.Message = statusError, lookupFailed
		return check
	}
	if len(found) == 0 {
		check.Status, check.Message = statusMissing, "No MX records found"
		return check
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Pref < found[j].Pref })
	values := make([]string, len(found))
	ours := false
	for i, mx := range found {
		values[i] = fmt.Sprintf("%d %s", mx.Pref, dnsName(mx.Host))
		ours = ours || dnsName(mx.Host) == record.Value
	}
	check.Value = strings.Join(values, ", ")
	switch {
	case !ours:
		check.Status = statusDifferent
		check.Message = fmt.Sprintf("Mail for this domain goes to %s, not to this server.", dnsName(found[0].Host))
	case len(found) > 1:
		check.Status = statusDifferent
		check.Message = "More than one mail host is published, so some mail can go to the others."
	default:
		// Priority orders mail hosts, so with one host it changes nothing.
		check.Status = statusOK
	}
	return check
}

func (h *DomainHandler) checkSRV(ctx context.Context, record dns.DNSRecord) DNSRecordCheck {
	srvValue := func(priority, weight, port int, target string) string {
		return fmt.Sprintf("%d %d %d %s", priority, weight, port, target)
	}
	check := DNSRecordCheck{Type: "SRV", Name: record.Name,
		Expected: srvValue(record.Priority, record.Weight, record.Port, record.Value)}
	found, err := h.lookupSRV(ctx, record.Name)
	if err != nil && !notFound(err) {
		check.Status, check.Message = statusError, lookupFailed
		return check
	}
	if len(found) == 0 {
		check.Status, check.Message = statusMissing, "No SRV record found"
		return check
	}
	values := make([]string, len(found))
	for i, srv := range found {
		values[i] = srvValue(int(srv.Priority), int(srv.Weight), int(srv.Port), dnsName(srv.Target))
	}
	check.Value = strings.Join(values, ", ")
	if len(found) == 1 && values[0] == check.Expected {
		check.Status = statusOK
		return check
	}
	check.Status, check.Message = statusDifferent, valueDiffers
	return check
}

func (h *DomainHandler) checkCNAME(ctx context.Context, record dns.DNSRecord) DNSRecordCheck {
	check := DNSRecordCheck{Type: "CNAME", Name: record.Name, Expected: record.Value}
	found, err := h.lookupCNAME(ctx, record.Name)
	if err != nil && !notFound(err) {
		check.Status, check.Message = statusError, lookupFailed
		return check
	}
	// With no CNAME, a resolver answers with the name itself, when it has an address.
	if err != nil || dnsName(found) == record.Name {
		check.Status, check.Message = statusMissing, "No CNAME record found for the MTA-STS policy host"
		return check
	}
	check.Value = dnsName(found)
	if check.Value != record.Value {
		check.Status, check.Message = statusDifferent, valueDiffers
		return check
	}
	check.Status = statusOK
	return check
}

// checkMailHostAddress checks the mail host's A record and each address's reverse record.
// MailRecords leaves both out without the server's public address, which the server does not
// know about itself (mail_records.go). So there's no address to compare, and the check says
// that. It takes the address from the DNS, and the reverse record is compared with the mail
// host as the list has it.
func (h *DomainHandler) checkMailHostAddress(ctx context.Context) []DNSRecordCheck {
	host := h.publish.mailHost
	a := DNSRecordCheck{Type: "A", Name: host}
	ips, err := h.lookupIPv4(ctx, host)
	if err != nil && !notFound(err) {
		a.Status, a.Message = statusError, lookupFailed
		return []DNSRecordCheck{a}
	}
	if len(ips) == 0 {
		a.Status = statusMissing
		a.Message = "The mail host has no A record. The person who runs the server publishes it."
		return []DNSRecordCheck{a, {Type: "PTR", Name: host, Expected: host, Status: statusMissing,
			Message: "The mail host has no address to look up the reverse record of."}}
	}
	addresses := make([]string, len(ips))
	for i, ip := range ips {
		addresses[i] = ip.String()
	}
	a.Value = strings.Join(addresses, ", ")
	a.Status = statusOK
	a.Message = "REST Mail does not know its own public address, so this shows only that the mail host has one. The person who runs the server publishes it."

	checks := []DNSRecordCheck{a}
	ptrs := make([]DNSRecordCheck, len(addresses))
	var wg sync.WaitGroup
	for i, address := range addresses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ptrs[i] = h.checkReverse(ctx, address, host)
		}()
	}
	wg.Wait()
	return append(checks, ptrs...)
}

func (h *DomainHandler) checkReverse(ctx context.Context, address, host string) DNSRecordCheck {
	check := DNSRecordCheck{Type: "PTR", Name: address, Expected: host}
	names, err := h.lookupAddr(ctx, address)
	if err != nil && !notFound(err) {
		check.Status, check.Message = statusError, lookupFailed
		return check
	}
	if len(names) == 0 {
		check.Status, check.Message = statusMissing, "No reverse record found. "+ownerOfAddress
		return check
	}
	values := make([]string, len(names))
	check.Status, check.Message = statusDifferent, valueDiffers+" "+ownerOfAddress
	for i, name := range names {
		values[i] = dnsName(name)
		if values[i] == host {
			check.Status, check.Message = statusOK, ""
		}
	}
	check.Value = strings.Join(values, ", ")
	return check
}
