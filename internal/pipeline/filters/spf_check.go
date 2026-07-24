package filters

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

// SPF evaluation (RFC 7208).
//
// This evaluator implements the check_host() algorithm with support for the
// all, ip4, ip6, a, mx, ptr, exists, and include mechanisms and the redirect
// modifier, macro expansion, and the RFC 7208 §4.6.4 processing limits (max 10
// DNS-querying terms, max 2 void lookups, max 10 MX/PTR hosts per term). Every
// DNS call is made through a context-aware resolver so a slow or hostile
// authoritative server cannot stall the pipeline past the deadline.

// SPF result strings, as emitted in Authentication-Results and consumed by the
// DMARC filter.
const (
	spfPass      = "pass"
	spfFail      = "fail"
	spfSoftfail  = "softfail"
	spfNeutral   = "neutral"
	spfNone      = "none"
	spfTempError = "temperror"
	spfPermError = "permerror"
)

// RFC 7208 §4.6.4 processing limits.
const (
	spfMaxLookups     = 10 // terms (a, mx, ptr, exists, include, redirect) that cause DNS queries
	spfMaxVoidLookups = 2  // lookups returning NXDOMAIN or an empty answer
	spfMaxMXHosts     = 10 // MX hosts resolved for a single mx term
	spfMaxPTRHosts    = 10 // names resolved for a single ptr term
)

// spfLookupTimeout bounds the total wall-clock time spent resolving DNS for a
// single SPF evaluation, so a slow or hostile authoritative server cannot stall
// the pipeline. RFC 7208 §4.6.4 recommends evaluation complete well within this
// bound. It is a package var so tests can shorten it.
var spfLookupTimeout = 10 * time.Second

// spfResolver is the context-aware DNS resolver abstraction used by the SPF
// evaluator. Its method set matches *net.Resolver, so the system resolver can
// be used directly, and tests can substitute a stub. Every method takes a
// context so a slow or hostile authoritative server cannot stall evaluation
// past the caller's deadline.
type spfResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// spfDefaultResolver is a package var so tests can stub DNS. It defaults to the
// system resolver.
var spfDefaultResolver spfResolver = net.DefaultResolver

// spfCheckFilter verifies sender IP against SPF DNS records.
type spfCheckFilter struct{}

func init() {
	pipeline.DefaultRegistry.Register("spf_check", NewSPFCheck)
}

func NewSPFCheck(_ []byte) (pipeline.Filter, error) {
	return &spfCheckFilter{}, nil
}

func (f *spfCheckFilter) Name() string              { return "spf_check" }
func (f *spfCheckFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *spfCheckFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	none := func(detail string) (*pipeline.FilterResult, error) {
		return &pipeline.FilterResult{
			Type:   pipeline.FilterTypeAction,
			Action: pipeline.ActionContinue,
			Log:    pipeline.FilterLog{Filter: "spf_check", Result: spfNone, Detail: detail},
		}, nil
	}

	clientIP := email.Envelope.ClientIP
	if clientIP == "" {
		return none("no client IP to check")
	}

	// Determine the identity to check (RFC 7208 §2.4): normally the MAIL FROM
	// address. For a null sender (<>) the check uses the HELO identity
	// (postmaster@<helo>). As a last resort, fall back to the header From so a
	// message with neither an envelope sender nor a HELO name is not silently
	// unchecked.
	sender := email.Envelope.MailFrom
	helo := email.Envelope.Helo
	var domain string
	switch {
	case sender != "":
		parts := strings.SplitN(sender, "@", 2)
		if len(parts) != 2 || parts[1] == "" {
			return none("invalid sender format")
		}
		domain = parts[1]
	case helo != "":
		sender = "postmaster@" + helo
		domain = helo
	case len(email.Headers.From) > 0 && email.Headers.From[0].Address != "":
		sender = email.Headers.From[0].Address
		parts := strings.SplitN(sender, "@", 2)
		if len(parts) != 2 || parts[1] == "" {
			return none("invalid From address")
		}
		domain = parts[1]
	default:
		return none("no sender or HELO to check")
	}

	result, detail := checkSPF(ctx, clientIP, domain, sender, helo)

	// Write Authentication-Results header for downstream filters (DMARC).
	authResult := fmt.Sprintf("spf=%s (%s) smtp.mailfrom=%s", result, detail, sender)
	if email.Headers.Raw == nil {
		email.Headers.Raw = make(map[string][]string)
	}
	email.Headers.Raw["Authentication-Results"] = append(
		email.Headers.Raw["Authentication-Results"],
		authResult,
	)

	if email.Headers.Extra == nil {
		email.Headers.Extra = make(map[string]string)
	}
	if existing := email.Headers.Extra["Authentication-Results"]; existing != "" {
		email.Headers.Extra["Authentication-Results"] = existing + "; " + authResult
	} else {
		email.Headers.Extra["Authentication-Results"] = authResult
	}

	// SPF alone doesn't reject (DMARC decides).
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "spf_check",
			Result: result,
			Detail: detail,
		},
	}, nil
}

// checkSPF evaluates the SPF policy for the given client IP against domain,
// using the sender (local@domain) and HELO name for macro expansion. It bounds
// total DNS time by spfLookupTimeout.
func checkSPF(ctx context.Context, clientIP, domain, sender, helo string) (string, string) {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return spfPermError, "invalid client IP"
	}
	if domain == "" {
		return spfNone, "no domain to check"
	}

	ctx, cancel := context.WithTimeout(ctx, spfLookupTimeout)
	defer cancel()

	local, senderDom := splitSender(sender, domain)
	e := &spfEval{
		resolver:    spfDefaultResolver,
		ip:          ip,
		ipIsV4:      ip.To4() != nil,
		sender:      local + "@" + senderDom,
		senderLocal: local,
		senderDom:   senderDom,
		helo:        helo,
	}
	return e.checkHost(ctx, domain)
}

// spfEval holds the mutable state of one check_host() evaluation, including the
// shared DNS-lookup and void-lookup counters that enforce RFC 7208 §4.6.4
// across the whole (possibly recursive) evaluation.
type spfEval struct {
	resolver    spfResolver
	ip          net.IP
	ipIsV4      bool
	sender      string // full local@domain used for the %{s} macro
	senderLocal string // %{l}
	senderDom   string // %{o}
	helo        string // %{h}
	lookups     int
	voids       int
}

func splitSender(sender, fallbackDomain string) (local, domain string) {
	if i := strings.LastIndex(sender, "@"); i >= 0 {
		local, domain = sender[:i], sender[i+1:]
	} else {
		local, domain = sender, fallbackDomain
	}
	if local == "" {
		local = "postmaster"
	}
	if domain == "" {
		domain = fallbackDomain
	}
	return local, domain
}

// countLookup charges one DNS-querying term against the §4.6.4 limit. It
// returns a non-empty reason when the limit is exceeded (evaluation must then
// permerror).
func (e *spfEval) countLookup() string {
	e.lookups++
	if e.lookups > spfMaxLookups {
		return fmt.Sprintf("exceeded %d DNS-lookup limit", spfMaxLookups)
	}
	return ""
}

// countVoid charges one void lookup (NXDOMAIN or empty answer) against the
// §4.6.4 limit.
func (e *spfEval) countVoid() string {
	e.voids++
	if e.voids > spfMaxVoidLookups {
		return fmt.Sprintf("exceeded %d void-lookup limit", spfMaxVoidLookups)
	}
	return ""
}

// checkHost implements RFC 7208 §4: fetch the SPF record for domain and
// evaluate it. It returns an SPF result string and a human-readable detail.
func (e *spfEval) checkHost(ctx context.Context, domain string) (string, string) {
	domain = strings.TrimSuffix(strings.TrimSpace(domain), ".")
	if domain == "" || len(domain) > 253 {
		return spfNone, "invalid domain"
	}

	record, res, detail := e.fetchRecord(ctx, domain)
	if res != "" {
		return res, detail
	}
	return e.evaluate(ctx, domain, record)
}

// fetchRecord returns the single SPF record for domain, or an early result
// (none/temperror/permerror) when there is no usable record.
func (e *spfEval) fetchRecord(ctx context.Context, domain string) (record, res, detail string) {
	txts, err := e.resolver.LookupTXT(ctx, domain)
	if err != nil {
		if isNotFound(err) {
			return "", spfNone, "no SPF record (nxdomain)"
		}
		return "", spfTempError, "TXT lookup failed: " + err.Error()
	}

	var found []string
	for _, t := range txts {
		if t == "v=spf1" || strings.HasPrefix(t, "v=spf1 ") {
			found = append(found, t)
		}
	}
	switch len(found) {
	case 0:
		return "", spfNone, "no SPF record found"
	case 1:
		return found[0], "", ""
	default:
		return "", spfPermError, "multiple SPF records published"
	}
}

// evaluate walks the terms of an SPF record left to right (RFC 7208 §4.6.2).
// The first matching mechanism sets the result via its qualifier; the redirect
// modifier is applied only if no mechanism matched and no all mechanism is
// present.
func (e *spfEval) evaluate(ctx context.Context, domain, record string) (string, string) {
	terms := strings.Fields(record)
	if len(terms) > 0 {
		terms = terms[1:] // drop the v=spf1 version token
	}

	var redirect string
	sawAll := false

	for _, term := range terms {
		if name, value, ok := splitModifier(term); ok {
			switch name {
			case "redirect":
				if redirect != "" {
					return spfPermError, "duplicate redirect modifier"
				}
				redirect = value
			case "exp":
				// Explanation string; does not affect the result.
			default:
				// Unknown modifiers are ignored (RFC 7208 §6).
			}
			continue
		}

		qual, name, value := splitMechanism(term)

		var (
			matched     bool
			res, detail string
		)
		switch name {
		case "all":
			sawAll = true
			matched = true
		case "ip4":
			var err error
			matched, err = matchIP4Term(e.ip, value)
			if err != nil {
				return spfPermError, "malformed term: " + term
			}
		case "ip6":
			var err error
			matched, err = matchIP6Term(e.ip, value)
			if err != nil {
				return spfPermError, "malformed term: " + term
			}
		case "a":
			matched, res, detail = e.matchA(ctx, domain, value)
		case "mx":
			matched, res, detail = e.matchMX(ctx, domain, value)
		case "exists":
			matched, res, detail = e.matchExists(ctx, domain, value)
		case "ptr":
			matched, res, detail = e.matchPTR(ctx, domain, value)
		case "include":
			matched, res, detail = e.matchInclude(ctx, domain, value)
		default:
			return spfPermError, "unknown mechanism: " + term
		}

		if res != "" {
			return res, detail
		}
		if matched {
			return qualifierResult(qual), "matched " + term
		}
	}

	if redirect != "" && !sawAll {
		return e.doRedirect(ctx, domain, redirect)
	}
	return spfNeutral, "no mechanism matched"
}

func (e *spfEval) matchA(ctx context.Context, domain, value string) (bool, string, string) {
	if over := e.countLookup(); over != "" {
		return false, spfPermError, over
	}
	spec, cidr4, cidr6, err := splitDualCIDR(value)
	if err != nil {
		return false, spfPermError, "malformed a mechanism"
	}
	target := domain
	if spec != "" {
		if target, err = e.expand(spec, domain); err != nil {
			return false, spfPermError, err.Error()
		}
	}
	addrs, lookErr := e.resolver.LookupIPAddr(ctx, target)
	if lookErr != nil {
		if isNotFound(lookErr) {
			if over := e.countVoid(); over != "" {
				return false, spfPermError, over
			}
			return false, "", ""
		}
		return false, spfTempError, "a lookup failed: " + lookErr.Error()
	}
	if len(addrs) == 0 {
		if over := e.countVoid(); over != "" {
			return false, spfPermError, over
		}
	}
	for _, a := range addrs {
		if e.ipMatches(a.IP, cidr4, cidr6) {
			return true, "", ""
		}
	}
	return false, "", ""
}

func (e *spfEval) matchMX(ctx context.Context, domain, value string) (bool, string, string) {
	if over := e.countLookup(); over != "" {
		return false, spfPermError, over
	}
	spec, cidr4, cidr6, err := splitDualCIDR(value)
	if err != nil {
		return false, spfPermError, "malformed mx mechanism"
	}
	target := domain
	if spec != "" {
		if target, err = e.expand(spec, domain); err != nil {
			return false, spfPermError, err.Error()
		}
	}
	mxs, lookErr := e.resolver.LookupMX(ctx, target)
	if lookErr != nil {
		if isNotFound(lookErr) {
			if over := e.countVoid(); over != "" {
				return false, spfPermError, over
			}
			return false, "", ""
		}
		return false, spfTempError, "mx lookup failed: " + lookErr.Error()
	}
	if len(mxs) == 0 {
		if over := e.countVoid(); over != "" {
			return false, spfPermError, over
		}
	}
	if len(mxs) > spfMaxMXHosts {
		return false, spfPermError, fmt.Sprintf("mx term resolves more than %d hosts", spfMaxMXHosts)
	}
	for _, mx := range mxs {
		host := strings.TrimSuffix(mx.Host, ".")
		addrs, err := e.resolver.LookupIPAddr(ctx, host)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return false, spfTempError, "mx host lookup failed: " + err.Error()
		}
		for _, a := range addrs {
			if e.ipMatches(a.IP, cidr4, cidr6) {
				return true, "", ""
			}
		}
	}
	return false, "", ""
}

func (e *spfEval) matchExists(ctx context.Context, domain, value string) (bool, string, string) {
	if over := e.countLookup(); over != "" {
		return false, spfPermError, over
	}
	target, err := e.expand(value, domain)
	if err != nil {
		return false, spfPermError, err.Error()
	}
	addrs, lookErr := e.resolver.LookupIPAddr(ctx, target)
	if lookErr != nil {
		if isNotFound(lookErr) {
			if over := e.countVoid(); over != "" {
				return false, spfPermError, over
			}
			return false, "", ""
		}
		return false, spfTempError, "exists lookup failed: " + lookErr.Error()
	}
	if len(addrs) == 0 {
		if over := e.countVoid(); over != "" {
			return false, spfPermError, over
		}
		return false, "", ""
	}
	// exists matches whenever the expanded name resolves to any address.
	return true, "", ""
}

func (e *spfEval) matchPTR(ctx context.Context, domain, value string) (bool, string, string) {
	if over := e.countLookup(); over != "" {
		return false, spfPermError, over
	}
	target := domain
	if value != "" {
		var err error
		if target, err = e.expand(value, domain); err != nil {
			return false, spfPermError, err.Error()
		}
	}
	names, err := e.resolver.LookupAddr(ctx, e.ip.String())
	if err != nil {
		if isNotFound(err) {
			if over := e.countVoid(); over != "" {
				return false, spfPermError, over
			}
			return false, "", ""
		}
		return false, spfTempError, "ptr lookup failed: " + err.Error()
	}
	if len(names) > spfMaxPTRHosts {
		names = names[:spfMaxPTRHosts]
	}
	target = strings.ToLower(strings.TrimSuffix(target, "."))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		// Forward-confirm the PTR name resolves back to the client IP.
		addrs, err := e.resolver.LookupIPAddr(ctx, name)
		if err != nil {
			continue
		}
		confirmed := false
		for _, a := range addrs {
			if a.IP.Equal(e.ip) {
				confirmed = true
				break
			}
		}
		if !confirmed {
			continue
		}
		if name == target || strings.HasSuffix(name, "."+target) {
			return true, "", ""
		}
	}
	return false, "", ""
}

func (e *spfEval) matchInclude(ctx context.Context, domain, value string) (bool, string, string) {
	if over := e.countLookup(); over != "" {
		return false, spfPermError, over
	}
	target, err := e.expand(value, domain)
	if err != nil {
		return false, spfPermError, err.Error()
	}
	res, detail := e.checkHost(ctx, target)
	switch res {
	case spfPass:
		return true, "", ""
	case spfFail, spfSoftfail, spfNeutral:
		return false, "", ""
	case spfTempError:
		return false, spfTempError, "include:" + target + " " + detail
	default: // permerror, none — an include target with no record is a permerror
		return false, spfPermError, "include:" + target + " " + detail
	}
}

func (e *spfEval) doRedirect(ctx context.Context, domain, value string) (string, string) {
	if over := e.countLookup(); over != "" {
		return spfPermError, over
	}
	target, err := e.expand(value, domain)
	if err != nil {
		return spfPermError, err.Error()
	}
	res, detail := e.checkHost(ctx, target)
	if res == spfNone {
		return spfPermError, "redirect=" + target + " has no SPF record"
	}
	return res, detail
}

// ipMatches reports whether the target address covers the client IP under the
// given dual-CIDR prefix lengths (cidr4/cidr6, -1 meaning host-exact).
func (e *spfEval) ipMatches(target net.IP, cidr4, cidr6 int) bool {
	if e.ipIsV4 {
		t := target.To4()
		if t == nil {
			return false
		}
		bits := 32
		if cidr4 >= 0 {
			bits = cidr4
		}
		mask := net.CIDRMask(bits, 32)
		return t.Mask(mask).Equal(e.ip.To4().Mask(mask))
	}
	// Client is IPv6.
	if target.To4() != nil {
		return false
	}
	t := target.To16()
	if t == nil {
		return false
	}
	bits := 128
	if cidr6 >= 0 {
		bits = cidr6
	}
	mask := net.CIDRMask(bits, 128)
	return t.Mask(mask).Equal(e.ip.To16().Mask(mask))
}

// splitMechanism separates the optional leading qualifier, the lowercased
// mechanism name, and the remaining value (the part after ":", or a bare
// "/cidr" suffix).
func splitMechanism(term string) (qual byte, name, value string) {
	qual = '+'
	s := term
	if len(s) > 0 && (s[0] == '+' || s[0] == '-' || s[0] == '~' || s[0] == '?') {
		qual = s[0]
		s = s[1:]
	}
	i := 0
	for i < len(s) && isNameChar(s[i]) {
		i++
	}
	name = strings.ToLower(s[:i])
	rest := s[i:]
	if strings.HasPrefix(rest, ":") {
		value = rest[1:]
	} else {
		value = rest // "" or "/cidr..."
	}
	return qual, name, value
}

// splitModifier detects a name=value modifier term (RFC 7208 §6). A mechanism
// never has an unqualified "name=" prefix, so this cleanly distinguishes the
// two.
func splitModifier(term string) (name, value string, ok bool) {
	eq := strings.IndexByte(term, '=')
	if eq <= 0 {
		return "", "", false
	}
	name = term[:eq]
	if !isAlpha(name[0]) {
		return "", "", false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(isAlpha(c) || isDigit(c) || c == '-' || c == '_' || c == '.') {
			return "", "", false
		}
	}
	return strings.ToLower(name), term[eq+1:], true
}

// splitDualCIDR splits an a/mx value into its domain-spec and optional
// dual-CIDR prefix lengths (/cidr4, //cidr6, or /cidr4//cidr6). A domain-spec
// never contains "/", so the first "/" marks the CIDR portion.
func splitDualCIDR(value string) (spec string, cidr4, cidr6 int, err error) {
	cidr4, cidr6 = -1, -1
	idx := strings.IndexByte(value, '/')
	if idx < 0 {
		return value, cidr4, cidr6, nil
	}
	spec = value[:idx]
	cidrPart := value[idx:] // begins with '/'

	if strings.HasPrefix(cidrPart, "//") {
		cidr6, err = parsePrefix(cidrPart[2:], 128)
		return spec, cidr4, cidr6, err
	}
	cidrPart = cidrPart[1:]
	if j := strings.Index(cidrPart, "//"); j >= 0 {
		if cidr4, err = parsePrefix(cidrPart[:j], 32); err != nil {
			return spec, cidr4, cidr6, err
		}
		cidr6, err = parsePrefix(cidrPart[j+2:], 128)
		return spec, cidr4, cidr6, err
	}
	cidr4, err = parsePrefix(cidrPart, 32)
	return spec, cidr4, cidr6, err
}

func parsePrefix(s string, max int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > max {
		return -1, fmt.Errorf("invalid CIDR length %q", s)
	}
	return n, nil
}

func matchIP4Term(clientIP net.IP, value string) (bool, error) {
	if value == "" {
		return false, errors.New("empty ip4")
	}
	if !strings.Contains(value, "/") {
		value += "/32"
	}
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return false, err
	}
	if network.IP.To4() == nil {
		return false, errors.New("ip4 mechanism with non-IPv4 network")
	}
	return network.Contains(clientIP), nil
}

func matchIP6Term(clientIP net.IP, value string) (bool, error) {
	if value == "" {
		return false, errors.New("empty ip6")
	}
	if !strings.Contains(value, "/") {
		value += "/128"
	}
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return false, err
	}
	return network.Contains(clientIP), nil
}

func qualifierResult(q byte) string {
	switch q {
	case '-':
		return spfFail
	case '~':
		return spfSoftfail
	case '?':
		return spfNeutral
	default: // '+'
		return spfPass
	}
}

// isNotFound reports whether err is a DNS "no such host"/NXDOMAIN, which SPF
// treats as an authoritative negative rather than a transient failure.
func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	return false
}

func isAlpha(c byte) bool    { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isDigit(c byte) bool    { return c >= '0' && c <= '9' }
func isNameChar(c byte) bool { return isAlpha(c) || isDigit(c) }

// expand performs RFC 7208 §7 macro expansion of a domain-spec relative to the
// current domain.
func (e *spfEval) expand(spec, domain string) (string, error) {
	if !strings.Contains(spec, "%") {
		return spec, nil
	}
	var b strings.Builder
	for i := 0; i < len(spec); {
		c := spec[i]
		if c != '%' {
			b.WriteByte(c)
			i++
			continue
		}
		i++
		if i >= len(spec) {
			return "", errors.New("malformed macro: trailing %")
		}
		switch spec[i] {
		case '%':
			b.WriteByte('%')
			i++
		case '_':
			b.WriteByte(' ')
			i++
		case '-':
			b.WriteString("%20")
			i++
		case '{':
			end := strings.IndexByte(spec[i:], '}')
			if end < 0 {
				return "", errors.New("malformed macro: unterminated %{")
			}
			val, err := e.expandMacro(spec[i+1:i+end], domain)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
			i += end + 1
		default:
			return "", fmt.Errorf("malformed macro: %%%c", spec[i])
		}
	}
	return b.String(), nil
}

// expandMacro expands a single macro expression (the text between %{ and }),
// applying the digit/reverse/delimiter transformers of RFC 7208 §7.1.
func (e *spfEval) expandMacro(expr, domain string) (string, error) {
	if expr == "" {
		return "", errors.New("empty macro")
	}
	letter := expr[0]
	urlEscape := letter >= 'A' && letter <= 'Z'
	base, err := e.macroValue(letter|0x20, domain)
	if err != nil {
		return "", err
	}

	rest := expr[1:]
	j := 0
	numStr := ""
	for j < len(rest) && isDigit(rest[j]) {
		numStr += string(rest[j])
		j++
	}
	reverse := false
	if j < len(rest) && (rest[j] == 'r' || rest[j] == 'R') {
		reverse = true
		j++
	}
	delims := "."
	if j < len(rest) {
		for k := j; k < len(rest); k++ {
			if !strings.ContainsRune(".-+,/_=", rune(rest[k])) {
				return "", fmt.Errorf("invalid macro transformer %q", rest[j:])
			}
		}
		delims = rest[j:]
	}

	parts := splitOn(base, delims)
	if reverse {
		for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
			parts[l], parts[r] = parts[r], parts[l]
		}
	}
	if numStr != "" {
		n, _ := strconv.Atoi(numStr)
		if n == 0 {
			return "", errors.New("macro digit must be non-zero")
		}
		if n < len(parts) {
			parts = parts[len(parts)-n:]
		}
	}
	out := strings.Join(parts, ".")
	if urlEscape {
		out = urlEscapeMacro(out)
	}
	return out, nil
}

func (e *spfEval) macroValue(letter byte, domain string) (string, error) {
	switch letter {
	case 's':
		return e.sender, nil
	case 'l':
		return e.senderLocal, nil
	case 'o':
		return e.senderDom, nil
	case 'd':
		return domain, nil
	case 'i':
		return e.ipMacro(), nil
	case 'p':
		// The validated-domain macro is deprecated (RFC 7208 §7.3) and its
		// lookup is expensive; "unknown" is the sanctioned fallback when no
		// validated name is available.
		return "unknown", nil
	case 'v':
		if e.ipIsV4 {
			return "in-addr", nil
		}
		return "ip6", nil
	case 'h':
		if e.helo != "" {
			return e.helo, nil
		}
		return "unknown", nil
	default:
		return "", fmt.Errorf("unsupported macro letter %q", letter)
	}
}

// ipMacro renders the client IP for the %{i} macro: dotted-decimal for IPv4,
// and the dot-separated nibble form for IPv6 (RFC 7208 §7.1).
func (e *spfEval) ipMacro() string {
	if e.ipIsV4 {
		return e.ip.To4().String()
	}
	ip := e.ip.To16()
	const hexdigits = "0123456789abcdef"
	nibbles := make([]byte, 0, 63)
	for _, b := range ip {
		if len(nibbles) > 0 {
			nibbles = append(nibbles, '.')
		}
		nibbles = append(nibbles, hexdigits[b>>4], '.', hexdigits[b&0xf])
	}
	return string(nibbles)
}

// splitOn splits s on any byte in delims, preserving empty fields (unlike
// strings.FieldsFunc), as macro expansion requires.
func splitOn(s, delims string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(delims, s[i]) >= 0 {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// urlEscapeMacro percent-encodes everything but RFC 3986 unreserved
// characters, as required for uppercase (URL-escaped) macros.
func urlEscapeMacro(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isAlpha(c) || isDigit(c) || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0xf])
	}
	return b.String()
}
