package mailcheck

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// privateIPPattern matches RFC 1918 and link-local addresses.
var privateIPPattern = regexp.MustCompile(`(?:^10\.|^172\.(?:1[6-9]|2\d|3[01])\.|^192\.168\.|^127\.|^169\.254\.|^fc00:|^fe80:)`)

// AnalyzeHeaders parses email headers from a raw message and returns check results.
func AnalyzeHeaders(rawMessage string, domain string) []CheckResult {
	var results []CheckResult

	results = append(results, analyzeAuthResults(rawMessage, domain))
	results = append(results, analyzeReceivedChain(rawMessage))
	results = append(results, analyzeDKIMSignature(rawMessage))
	results = append(results, analyzeSPFAlignment(rawMessage, domain))
	results = append(results, analyzeSpamScore(rawMessage))
	results = append(results, analyzeARC(rawMessage))

	return results
}

// analyzeAuthResults checks the Authentication-Results header for SPF/DKIM/DMARC pass.
func analyzeAuthResults(rawMessage string, domain string) CheckResult {
	result := CheckResult{
		Name:     "Header Analysis",
		Category: "headers",
	}

	authResults := extractHeader(rawMessage, "Authentication-Results")
	if authResults == "" {
		result.Status = StatusWarn
		result.Summary = "No Authentication-Results header found"
		result.Detail = "This header is added by the receiving server to show SPF/DKIM/DMARC results."
		result.Fix = "Ensure your MTA adds Authentication-Results headers. This is critical for debugging deliverability."
		return result
	}

	lower := strings.ToLower(authResults)

	var issues []string
	var passes []string

	// SPF
	if strings.Contains(lower, "spf=pass") {
		passes = append(passes, "SPF=pass")
	} else if strings.Contains(lower, "spf=") {
		spfResult := extractSubfield(lower, "spf=")
		issues = append(issues, fmt.Sprintf("SPF=%s", spfResult))
	}

	// DKIM
	if strings.Contains(lower, "dkim=pass") {
		passes = append(passes, "DKIM=pass")
	} else if strings.Contains(lower, "dkim=") {
		dkimResult := extractSubfield(lower, "dkim=")
		issues = append(issues, fmt.Sprintf("DKIM=%s", dkimResult))
	} else {
		issues = append(issues, "DKIM=missing")
	}

	// DMARC
	if strings.Contains(lower, "dmarc=pass") {
		passes = append(passes, "DMARC=pass")
	} else if strings.Contains(lower, "dmarc=") {
		dmarcResult := extractSubfield(lower, "dmarc=")
		issues = append(issues, fmt.Sprintf("DMARC=%s", dmarcResult))
	}

	if len(issues) > 0 {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Auth issues: %s", strings.Join(issues, ", "))
		if len(passes) > 0 {
			result.Summary += fmt.Sprintf(" (passed: %s)", strings.Join(passes, ", "))
		}
		result.Detail = fmt.Sprintf("Authentication-Results: %s", authResults)
		result.Fix = "Fix failing authentication checks. SPF: ensure sending IP is in SPF record. DKIM: ensure signing is configured. DMARC: ensure SPF or DKIM aligns with From domain."
	} else if len(passes) > 0 {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("All auth checks passed: %s", strings.Join(passes, ", "))
		result.Detail = fmt.Sprintf("Authentication-Results: %s", authResults)
	} else {
		result.Status = StatusWarn
		result.Summary = "Authentication-Results header present but no SPF/DKIM/DMARC results found"
		result.Detail = authResults
	}

	return result
}

// analyzeReceivedChain checks the Received headers for internal IP leakage and hop count.
func analyzeReceivedChain(rawMessage string) CheckResult {
	result := CheckResult{
		Name:     "Received Chain",
		Category: "headers",
	}

	receivedHeaders := extractAllHeaders(rawMessage, "Received")
	if len(receivedHeaders) == 0 {
		result.Status = StatusWarn
		result.Summary = "No Received headers found"
		return result
	}

	hopCount := len(receivedHeaders)
	var leakedIPs []string

	// Check each Received header for private IP addresses
	ipPattern := regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	for _, header := range receivedHeaders {
		matches := ipPattern.FindAllString(header, -1)
		for _, ip := range matches {
			parsed := net.ParseIP(ip)
			if parsed != nil && privateIPPattern.MatchString(ip) {
				leakedIPs = append(leakedIPs, ip)
			}
		}
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("%d hop(s)", hopCount))

	if len(leakedIPs) > 0 {
		// Deduplicate
		seen := make(map[string]bool)
		var unique []string
		for _, ip := range leakedIPs {
			if !seen[ip] {
				seen[ip] = true
				unique = append(unique, ip)
			}
		}
		result.Status = StatusWarn
		parts = append(parts, fmt.Sprintf("internal IPs leaked: %s", strings.Join(unique, ", ")))
		result.Fix = "Configure your MTA to strip or sanitize Received headers that expose internal network topology. Postfix: header_checks to remove internal Received headers."
	} else {
		result.Status = StatusPass
	}

	result.Summary = strings.Join(parts, "; ")
	if hopCount > 5 {
		result.Detail = "High hop count may indicate unnecessary relaying or misconfiguration"
		if result.Status == StatusPass {
			result.Status = StatusWarn
		}
	}

	return result
}

// analyzeDKIMSignature checks if a DKIM-Signature header is present.
func analyzeDKIMSignature(rawMessage string) CheckResult {
	result := CheckResult{
		Name:     "DKIM Signature",
		Category: "headers",
	}

	dkimSig := extractHeader(rawMessage, "DKIM-Signature")
	if dkimSig == "" {
		result.Status = StatusFail
		result.Summary = "No DKIM-Signature header on sent message"
		result.Detail = "Messages without DKIM signatures are more likely to be flagged as spam."
		result.Fix = "Configure DKIM signing in your MTA. Use opendkim or rspamd for Postfix. Ensure the signing domain matches your From domain."
		return result
	}

	// Parse key fields
	lower := strings.ToLower(dkimSig)
	var info []string

	if d := extractSubfield(lower, "d="); d != "" {
		info = append(info, fmt.Sprintf("d=%s", d))
	}
	if s := extractSubfield(lower, "s="); s != "" {
		info = append(info, fmt.Sprintf("s=%s", s))
	}
	if a := extractSubfield(lower, "a="); a != "" {
		info = append(info, fmt.Sprintf("a=%s", a))
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("DKIM-Signature present (%s)", strings.Join(info, ", "))

	return result
}

// analyzeSPFAlignment checks if the envelope sender (Return-Path) domain aligns
// with the From header domain, which is required for DMARC SPF alignment.
func analyzeSPFAlignment(rawMessage string, domain string) CheckResult {
	result := CheckResult{
		Name:     "SPF Alignment",
		Category: "headers",
	}

	// Extract Return-Path (envelope sender)
	returnPath := extractHeader(rawMessage, "Return-Path")
	if returnPath == "" {
		result.Status = StatusWarn
		result.Summary = "No Return-Path header found"
		result.Detail = "Return-Path is set by the receiving server from the MAIL FROM envelope address."
		return result
	}

	// Extract From header
	from := extractHeader(rawMessage, "From")
	if from == "" {
		result.Status = StatusWarn
		result.Summary = "No From header found"
		return result
	}

	// Extract domains from both
	rpDomain := extractDomainFromAddress(returnPath)
	fromDomain := extractDomainFromAddress(from)

	if rpDomain == "" || fromDomain == "" {
		result.Status = StatusWarn
		result.Summary = "Could not parse domains from Return-Path or From"
		result.Detail = fmt.Sprintf("Return-Path: %s, From: %s", returnPath, from)
		return result
	}

	rpDomain = strings.ToLower(rpDomain)
	fromDomain = strings.ToLower(fromDomain)

	// Strict alignment: exact domain match
	if rpDomain == fromDomain {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("SPF aligned (strict): %s", fromDomain)
		result.Detail = fmt.Sprintf("Return-Path domain (%s) matches From domain (%s)", rpDomain, fromDomain)
		return result
	}

	// Relaxed alignment: organizational domain match (parent domain)
	rpOrg := organizationalDomain(rpDomain)
	fromOrg := organizationalDomain(fromDomain)
	if rpOrg == fromOrg {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("SPF aligned (relaxed): org domain %s", rpOrg)
		result.Detail = fmt.Sprintf("Return-Path (%s) and From (%s) share organizational domain (%s)", rpDomain, fromDomain, rpOrg)
		return result
	}

	result.Status = StatusFail
	result.Summary = fmt.Sprintf("SPF misaligned: Return-Path=%s, From=%s", rpDomain, fromDomain)
	result.Detail = "DMARC requires either SPF or DKIM to align with the From domain. SPF alignment means the envelope sender domain must match the From domain."
	result.Fix = "Ensure your MAIL FROM envelope address uses the same domain as your From header. In Postfix: check mydomain and myorigin settings."
	return result
}

// analyzeARC checks for Authenticated Received Chain headers on a received message.
func analyzeARC(rawMessage string) CheckResult {
	result := CheckResult{
		Name:     "ARC Chain",
		Category: "headers",
	}

	arcSeal := extractAllHeaders(rawMessage, "ARC-Seal")
	arcMsgSig := extractAllHeaders(rawMessage, "ARC-Message-Signature")
	arcAuthResults := extractAllHeaders(rawMessage, "ARC-Authentication-Results")

	if len(arcSeal) == 0 && len(arcMsgSig) == 0 && len(arcAuthResults) == 0 {
		result.Status = StatusSkip
		result.Summary = "No ARC headers found (optional)"
		result.Detail = "ARC (Authenticated Received Chain) preserves authentication results across forwarding hops. It's added by intermediaries, not the origin server."
		return result
	}

	var details []string
	details = append(details, fmt.Sprintf("ARC-Seal: %d instance(s)", len(arcSeal)))
	details = append(details, fmt.Sprintf("ARC-Message-Signature: %d instance(s)", len(arcMsgSig)))
	details = append(details, fmt.Sprintf("ARC-Authentication-Results: %d instance(s)", len(arcAuthResults)))

	// Check chain validity indicator from the last ARC-Seal
	if len(arcSeal) > 0 {
		lastSeal := arcSeal[len(arcSeal)-1]
		lower := strings.ToLower(lastSeal)
		if cv := extractSubfield(lower, "cv="); cv != "" {
			details = append(details, fmt.Sprintf("Chain validation: cv=%s", cv))
			if cv == "pass" {
				result.Status = StatusPass
				result.Summary = fmt.Sprintf("ARC chain present (%d hop(s)), cv=pass", len(arcSeal))
			} else if cv == "none" {
				result.Status = StatusPass
				result.Summary = fmt.Sprintf("ARC chain present (%d hop(s)), cv=none (first hop)", len(arcSeal))
			} else {
				result.Status = StatusWarn
				result.Summary = fmt.Sprintf("ARC chain present but cv=%s", cv)
			}
		} else {
			result.Status = StatusPass
			result.Summary = fmt.Sprintf("ARC chain present (%d hop(s))", len(arcSeal))
		}
	} else {
		result.Status = StatusPass
		result.Summary = "Partial ARC headers found"
	}

	result.Detail = strings.Join(details, "\n")
	return result
}

// extractDomainFromAddress extracts the domain from an email address or angle-bracket address.
func extractDomainFromAddress(addr string) string {
	// Handle <user@domain> format
	if idx := strings.LastIndex(addr, "<"); idx >= 0 {
		addr = addr[idx+1:]
		if end := strings.Index(addr, ">"); end >= 0 {
			addr = addr[:end]
		}
	}
	// Handle user@domain format
	if idx := strings.LastIndex(addr, "@"); idx >= 0 {
		return strings.TrimSpace(addr[idx+1:])
	}
	return ""
}

// organizationalDomain returns the registrable domain (e.g., "sub.example.com" -> "example.com").
// This is a simplified version that handles common cases.
func organizationalDomain(domain string) string {
	parts := strings.Split(domain, ".")
	if len(parts) <= 2 {
		return domain
	}
	// Handle common multi-part TLDs (co.uk, com.au, etc.)
	if len(parts) >= 3 {
		tld2 := parts[len(parts)-2] + "." + parts[len(parts)-1]
		multiPartTLDs := []string{"co.uk", "org.uk", "ac.uk", "com.au", "com.br", "co.jp", "co.nz", "co.za", "com.mx", "co.in"}
		for _, mt := range multiPartTLDs {
			if tld2 == mt {
				if len(parts) >= 4 {
					return parts[len(parts)-3] + "." + tld2
				}
				return domain
			}
		}
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

// analyzeSpamScore estimates the spam-likeness of a message based on common triggers.
func analyzeSpamScore(rawMessage string) CheckResult {
	result := CheckResult{
		Name:     "Spam Score Estimate",
		Category: "headers",
	}

	var issues []string
	var good []string

	// Split headers from body
	headerEnd := strings.Index(rawMessage, "\r\n\r\n")
	if headerEnd < 0 {
		headerEnd = strings.Index(rawMessage, "\n\n")
	}
	headers := rawMessage
	body := ""
	if headerEnd >= 0 {
		headers = rawMessage[:headerEnd]
		body = rawMessage[headerEnd:]
	}

	// 1. Check for required headers
	if extractHeader(rawMessage, "Date") == "" {
		issues = append(issues, "Missing Date header")
	} else {
		good = append(good, "Date present")
	}

	if extractHeader(rawMessage, "Message-ID") == "" && extractHeader(rawMessage, "Message-Id") == "" {
		issues = append(issues, "Missing Message-ID header")
	} else {
		good = append(good, "Message-ID present")
	}

	if extractHeader(rawMessage, "MIME-Version") == "" {
		issues = append(issues, "Missing MIME-Version header (not multipart)")
	}

	// 2. Check Subject
	subject := extractHeader(rawMessage, "Subject")
	if subject == "" {
		issues = append(issues, "Missing Subject header")
	} else {
		if subject == strings.ToUpper(subject) && len(subject) > 3 {
			issues = append(issues, "Subject is ALL CAPS")
		}
		if strings.Contains(subject, "!!!") || strings.Contains(subject, "$$$") || strings.Contains(subject, "***") {
			issues = append(issues, "Subject contains spam-like punctuation")
		}
		if len(subject) > 150 {
			issues = append(issues, "Subject is very long (>150 chars)")
		}
	}

	// 3. Check From header format
	from := extractHeader(rawMessage, "From")
	if from != "" {
		if !strings.Contains(from, "@") {
			issues = append(issues, "From header has no email address")
		} else {
			good = append(good, "From has valid address")
		}
	}

	// 4. Check body characteristics
	bodyTrimmed := strings.TrimSpace(body)
	if len(bodyTrimmed) < 20 {
		issues = append(issues, "Very short or empty body")
	}

	// 5. Check for X-Spam headers (indicates the receiving server scored it)
	xSpamStatus := extractHeader(rawMessage, "X-Spam-Status")
	if xSpamStatus != "" {
		lower := strings.ToLower(xSpamStatus)
		if strings.Contains(lower, "yes") {
			issues = append(issues, fmt.Sprintf("X-Spam-Status: %s", xSpamStatus))
		} else {
			good = append(good, "X-Spam-Status: No")
		}
	}

	xSpamScore := extractHeader(rawMessage, "X-Spam-Score")
	if xSpamScore != "" {
		result.Detail = fmt.Sprintf("X-Spam-Score: %s", xSpamScore)
	}

	// 6. Check for Content-Type (text/plain vs text/html)
	contentType := extractHeader(headers, "Content-Type")
	if contentType != "" && strings.Contains(strings.ToLower(contentType), "text/html") {
		// HTML-only is a minor spam signal
		if !strings.Contains(strings.ToLower(headers), "multipart/alternative") {
			issues = append(issues, "HTML-only message (no text/plain alternative)")
		}
	}

	// Calculate score
	if len(issues) == 0 {
		result.Status = StatusPass
		result.Summary = "No spam triggers detected"
		if len(good) > 0 {
			result.Detail = strings.Join(good, ", ")
		}
	} else if len(issues) <= 2 {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Minor spam triggers: %s", strings.Join(issues, "; "))
		result.Fix = "Review message composition: ensure all required headers are present, avoid ALL CAPS subjects, include a meaningful body."
	} else {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Multiple spam triggers (%d issues): %s", len(issues), strings.Join(issues, "; "))
		result.Fix = "Your message has multiple characteristics that spam filters flag. Ensure Date, Message-ID, MIME-Version headers are present. Use a descriptive subject without excessive punctuation. Include meaningful body content."
	}

	return result
}

// extractHeader extracts the first occurrence of a header from raw email text.
func extractHeader(raw string, name string) string {
	lines := strings.Split(raw, "\n")
	prefix := strings.ToLower(name) + ":"
	var value strings.Builder
	found := false

	for _, line := range lines {
		if found {
			// Continuation line (starts with whitespace)
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				value.WriteString(" ")
				value.WriteString(strings.TrimSpace(line))
				continue
			}
			break
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), prefix) {
			found = true
			value.WriteString(strings.TrimSpace(line[len(prefix):]))
		}
	}

	return strings.TrimSpace(value.String())
}

// extractAllHeaders extracts all occurrences of a header.
func extractAllHeaders(raw string, name string) []string {
	lines := strings.Split(raw, "\n")
	prefix := strings.ToLower(name) + ":"
	var results []string
	var current strings.Builder
	inHeader := false

	for _, line := range lines {
		if inHeader {
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				current.WriteString(" ")
				current.WriteString(strings.TrimSpace(line))
				continue
			}
			results = append(results, strings.TrimSpace(current.String()))
			current.Reset()
			inHeader = false
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), prefix) {
			inHeader = true
			current.WriteString(strings.TrimSpace(line[len(prefix):]))
		}
	}
	if inHeader {
		results = append(results, strings.TrimSpace(current.String()))
	}

	return results
}

// extractSubfield extracts a value after a key= in a header value.
func extractSubfield(headerValue, key string) string {
	idx := strings.Index(headerValue, key)
	if idx < 0 {
		return ""
	}
	rest := headerValue[idx+len(key):]
	// Value ends at semicolon, space, or end of string
	end := strings.IndexAny(rest, "; \t\r\n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// RoundTripTest sends a message via authenticated SMTP and fetches it via IMAP,
// measuring end-to-end latency and analyzing headers.
func RoundTripTest(host, user, pass, domain string, timeout time.Duration) []CheckResult {
	start := time.Now()

	// Generate a unique subject for this test
	testID := fmt.Sprintf("imc-roundtrip-%d", time.Now().UnixNano())
	subject := fmt.Sprintf("Instant Mail Check Round-Trip Test %s", testID)

	// Step 1: Send via authenticated SMTP
	sendResult := CheckResult{
		Name:     "Email Round-Trip",
		Category: "roundtrip",
	}

	sendErr := sendTestEmail(host, user, pass, user, subject, testID, timeout)
	if sendErr != nil {
		sendResult.Status = StatusFail
		sendResult.Summary = fmt.Sprintf("Failed to send test email: %s", sendErr.Error())
		sendResult.Duration = time.Since(start)
		return []CheckResult{sendResult}
	}

	// Step 2: Poll IMAP for the message (up to 30 seconds)
	pollTimeout := 30 * time.Second
	if timeout*3 > pollTimeout {
		pollTimeout = timeout * 3
	}

	rawMessage, fetchErr := pollIMAPForMessage(host, user, pass, subject, pollTimeout)
	roundTripDuration := time.Since(start)

	if fetchErr != nil {
		sendResult.Status = StatusWarn
		sendResult.Summary = fmt.Sprintf("Sent OK but could not fetch via IMAP within %s: %s", pollTimeout, fetchErr.Error())
		sendResult.Duration = roundTripDuration
		return []CheckResult{sendResult}
	}

	sendResult.Status = StatusPass
	sendResult.Summary = fmt.Sprintf("Round-trip completed in %s", formatDuration(roundTripDuration))
	sendResult.Duration = roundTripDuration

	// Step 3: Analyze headers
	results := []CheckResult{sendResult}
	headerResults := AnalyzeHeaders(rawMessage, domain)
	results = append(results, headerResults...)

	return results
}

// sendTestEmail sends a test email via authenticated SMTP on port 587.
func sendTestEmail(host, user, pass, to, subject, testID string, timeout time.Duration) error {
	result := AuthSMTPSend(host, user, pass, to, timeout)
	if result.Status == StatusFail {
		return fmt.Errorf("%s: %s", result.Summary, result.Detail)
	}
	return nil
}

// pollIMAPForMessage polls IMAP for a message with the given subject.
func pollIMAPForMessage(host, user, pass, subject string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		msg, err := fetchLatestIMAPMessage(host, user, pass, 10*time.Second)
		if err == nil && strings.Contains(msg, subject) {
			return msg, nil
		}
		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("message not found within timeout")
}

// fetchLatestIMAPMessage connects to IMAP and fetches the latest message headers+body.
func fetchLatestIMAPMessage(host, user, pass string, timeout time.Duration) (string, error) {
	addr := fmt.Sprintf("%s:993", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
	})
	if err != nil {
		return "", err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	bufReader := bufio.NewReader(conn)

	// Read greeting
	if _, err = bufReader.ReadString('\n'); err != nil {
		return "", err
	}

	// LOGIN
	fmt.Fprintf(conn, "a1 LOGIN %s %s\r\n", quoteIMAP(user), quoteIMAP(pass))
	loginResp, err := readIMAPTagged(bufReader, "a1")
	if err != nil || !strings.Contains(loginResp, "OK") {
		return "", fmt.Errorf("IMAP login failed: %s", loginResp)
	}

	// SELECT INBOX
	fmt.Fprintf(conn, "a2 SELECT INBOX\r\n")
	selectResp, err := readIMAPTagged(bufReader, "a2")
	if err != nil {
		return "", fmt.Errorf("SELECT INBOX failed: %s", err)
	}

	// Find EXISTS count
	exists := 0
	for _, line := range strings.Split(selectResp, "\n") {
		if strings.Contains(line, "EXISTS") {
			fmt.Sscanf(strings.TrimSpace(line), "* %d EXISTS", &exists)
		}
	}

	if exists == 0 {
		fmt.Fprintf(conn, "a9 LOGOUT\r\n")
		return "", fmt.Errorf("INBOX is empty")
	}

	// FETCH the latest message
	fmt.Fprintf(conn, "a3 FETCH %d (BODY[])\r\n", exists)
	fetchResp, err := readIMAPTagged(bufReader, "a3")
	if err != nil {
		fmt.Fprintf(conn, "a9 LOGOUT\r\n")
		return "", fmt.Errorf("FETCH failed: %s", err)
	}

	fmt.Fprintf(conn, "a9 LOGOUT\r\n")

	return fetchResp, nil
}
