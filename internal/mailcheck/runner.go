package mailcheck

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"time"
)

// dnsCtx creates a per-check DNS context with its own timeout.
func dnsCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

// Run executes all checks based on the provided options and returns a Report.
func Run(opts Options) *Report {
	report := &Report{
		Domain:    opts.Domain,
		Timestamp: time.Now(),
	}

	dnsTimeout := opts.Timeout
	if dnsTimeout < 10*time.Second {
		dnsTimeout = 10 * time.Second
	}

	// ── Tier 1: DNS Checks ──────────────────────────────────────────────

	// MX records (needed for subsequent checks)
	ctx, cancel := dnsCtx(dnsTimeout)
	mxResult, mxHosts := CheckMX(ctx, opts.Domain)
	cancel()
	report.Checks = append(report.Checks, mxResult)
	report.MXHosts = mxHosts

	// SPF
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckSPF(ctx, opts.Domain))
	cancel()

	// DKIM
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckDKIM(ctx, opts.Domain, opts.DKIMSelector))
	cancel()

	// DMARC
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckDMARC(ctx, opts.Domain))
	cancel()

	// MTA-STS
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckMTASTS(ctx, opts.Domain, dnsTimeout, mxHosts))
	cancel()

	// TLS-RPT
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckTLSRPT(ctx, opts.Domain))
	cancel()

	// DNSSEC
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckDNSSEC(ctx, opts.Domain, dnsTimeout))
	cancel()

	// CAA
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckCAA(ctx, opts.Domain, dnsTimeout))
	cancel()

	// BIMI
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckBIMI(ctx, opts.Domain))
	cancel()

	// Autoconfig/Autodiscover
	ctx, cancel = dnsCtx(dnsTimeout)
	report.Checks = append(report.Checks, CheckAutoconfig(ctx, opts.Domain, dnsTimeout))
	cancel()

	// If we have MX hosts, run connection-level checks on the primary MX
	if len(mxHosts) > 0 {
		primaryMX := mxHosts[0]

		// Resolve MX IPs for PTR and blacklist checks
		ctx, cancel = dnsCtx(dnsTimeout)
		mxIPs := ResolveMXIPs(ctx, primaryMX)
		cancel()

		// PTR check on first IP
		if len(mxIPs) > 0 {
			ctx, cancel = dnsCtx(dnsTimeout)
			report.Checks = append(report.Checks, CheckPTR(ctx, mxIPs[0]))
			cancel()

			// Forward-Confirmed rDNS
			ctx, cancel = dnsCtx(dnsTimeout)
			report.Checks = append(report.Checks, CheckFCrDNS(ctx, mxIPs[0]))
			cancel()
		} else {
			report.Checks = append(report.Checks, CheckResult{
				Name:     "Reverse DNS (PTR)",
				Category: "dns",
				Status:   StatusError,
				Summary:  fmt.Sprintf("Could not resolve IP for %s", primaryMX),
			})
		}

		// IPv6 readiness
		ctx, cancel = dnsCtx(dnsTimeout)
		report.Checks = append(report.Checks, CheckIPv6Readiness(ctx, primaryMX))
		cancel()

		// DANE/TLSA
		report.Checks = append(report.Checks, CheckDANE(ctx, primaryMX, dnsTimeout))

		// ── Tier 1: Connection Checks ───────────────────────────────────

		// SMTP banner on port 25
		report.Checks = append(report.Checks, CheckSMTPBanner(primaryMX, opts.Timeout))

		// STARTTLS on port 25
		report.Checks = append(report.Checks, CheckSTARTTLS(primaryMX, opts.Timeout))

		// SMTP TLS certificate (via STARTTLS)
		report.Checks = append(report.Checks, CheckSMTPTLSCert(primaryMX, opts.Timeout))

		// Submission port 587
		report.Checks = append(report.Checks, CheckSubmission(primaryMX, opts.Timeout))

		// SMTPS port 465 (implicit TLS)
		report.Checks = append(report.Checks, CheckSMTPS(primaryMX, opts.Timeout))

		// SMTP extensions (SIZE, PIPELINING, REQUIRETLS, etc.)
		report.Checks = append(report.Checks, CheckSMTPExtensions(primaryMX, opts.Timeout))

		// IMAPS port 993
		report.Checks = append(report.Checks, CheckIMAPS(primaryMX, opts.Timeout))

		// IMAPS TLS certificate
		report.Checks = append(report.Checks, CheckIMAPSTLSCert(primaryMX, opts.Timeout))

		// POP3S port 995
		report.Checks = append(report.Checks, CheckPOP3S(primaryMX, opts.Timeout))

		// Open relay test
		report.Checks = append(report.Checks, CheckOpenRelay(primaryMX, opts.Timeout))

		// ── Tier 1: Security Checks ────────────────────────────────────

		// Banner information leakage
		report.Checks = append(report.Checks, CheckBannerLeak(primaryMX, opts.Timeout))

		// VRFY/EXPN user enumeration
		report.Checks = append(report.Checks, CheckVRFYEXPN(primaryMX, opts.Timeout))

		// Plaintext ports (110, 143)
		report.Checks = append(report.Checks, CheckPlaintextPorts(primaryMX, opts.Timeout))

		// TLS minimum version
		report.Checks = append(report.Checks, CheckTLSVersions(primaryMX, opts.Timeout))

		// Self-signed certificate
		report.Checks = append(report.Checks, CheckSelfSignedCert(primaryMX, opts.Timeout))

		// Plaintext AUTH on port 25
		report.Checks = append(report.Checks, CheckPlaintextAuth(primaryMX, opts.Timeout))

		// Auth mechanisms on port 587
		report.Checks = append(report.Checks, CheckAuthMechanisms(primaryMX, opts.Timeout))

		// ── Tier 1: Reputation Checks ───────────────────────────────────

		if ipv4 := FirstIPv4(mxIPs); ipv4 != "" {
			ctx, cancel = dnsCtx(dnsTimeout)
			report.Checks = append(report.Checks, CheckBlacklists(ctx, ipv4))
			cancel()
		} else if len(mxIPs) > 0 {
			report.Checks = append(report.Checks, CheckResult{
				Name:     "IP Blacklists",
				Category: "reputation",
				Status:   StatusSkip,
				Summary:  fmt.Sprintf("No IPv4 address found for %s; DNSBL requires IPv4", mxHosts[0]),
			})
		}

		// Domain blacklists
		ctx, cancel = dnsCtx(dnsTimeout)
		report.Checks = append(report.Checks, CheckDomainBlacklists(ctx, opts.Domain))
		cancel()

		// ── Tier 2: Send Test ───────────────────────────────────────────

		if opts.SendTo != "" {
			report.Checks = append(report.Checks, SMTPSendTest(primaryMX, opts.SendTo, opts.Timeout))
		}

		// ── Tier 3: Authenticated Checks ────────────────────────────────

		if opts.User != "" && opts.Pass != "" {
			// Password strength
			report.Checks = append(report.Checks, CheckPasswordStrength(opts.Pass))

			// IMAP login
			report.Checks = append(report.Checks, IMAPLogin(primaryMX, opts.User, opts.Pass, opts.Timeout))

			// IMAP capabilities
			report.Checks = append(report.Checks, CheckIMAPCapabilities(primaryMX, opts.User, opts.Pass, opts.Timeout))

			// IMAP IDLE
			report.Checks = append(report.Checks, CheckIMAPIDLE(primaryMX, opts.User, opts.Pass, opts.Timeout))

			// Mailbox quota
			report.Checks = append(report.Checks, CheckIMAPQuota(primaryMX, opts.User, opts.Pass, opts.Timeout))

			// POP3 login
			report.Checks = append(report.Checks, POP3Login(primaryMX, opts.User, opts.Pass, opts.Timeout))

			// Authenticated SMTP send
			report.Checks = append(report.Checks, AuthSMTPSend(primaryMX, opts.User, opts.Pass, opts.User, opts.Timeout))

			// Round-trip test: send via 587, fetch via IMAP, analyze headers
			rtResults := RoundTripTest(primaryMX, opts.User, opts.Pass, opts.Domain, opts.Timeout)
			report.Checks = append(report.Checks, rtResults...)
		}

		// ── Tier 4: Exploit Simulation (--security-audit) ──────────────

		if opts.SecurityAudit {
			// User enumeration via RCPT TO
			report.Checks = append(report.Checks, CheckUserEnumRCPT(primaryMX, opts.Domain, opts.Timeout))

			// User enumeration via VRFY
			report.Checks = append(report.Checks, CheckUserEnumVRFY(primaryMX, opts.Domain, opts.Timeout))

			// Brute-force rate limiting
			report.Checks = append(report.Checks, CheckBruteForceProtection(primaryMX, opts.Timeout))

			// SMTP smuggling
			report.Checks = append(report.Checks, CheckSMTPSmuggling(primaryMX, opts.Timeout))

			// Connection rate limiting
			report.Checks = append(report.Checks, CheckRateLimiting(primaryMX, opts.Timeout))
		}
	} else {
		// No MX hosts — skip all connection checks
		skipNames := []string{
			"Reverse DNS (PTR)", "DANE/TLSA", "SMTP Banner", "SMTP STARTTLS",
			"SMTP TLS Certificate", "Submission Port 587", "IMAPS Port 993",
			"IMAPS TLS Certificate", "POP3S Port 995", "Open Relay Test", "IP Blacklists",
		}
		for _, name := range skipNames {
			report.Checks = append(report.Checks, CheckResult{
				Name:     name,
				Category: "skip",
				Status:   StatusSkip,
				Summary:  "Skipped — no MX records found",
			})
		}
	}

	report.CalculateScore()
	return report
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// AuthSMTPSend sends a test email via authenticated SMTP on port 587.
func AuthSMTPSend(host, user, pass, sendTo string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Authenticated SMTP Send",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:587", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Cannot connect to %s", addr)
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 4))
	reader := bufio.NewReader(conn)

	// Read banner
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusFail
		result.Summary = "Failed to read banner on port 587"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// EHLO
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusFail
		result.Summary = "EHLO failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// STARTTLS
	fmt.Fprintf(conn, "STARTTLS\r\n")
	resp, err := readSMTPResponse(reader)
	if err != nil || len(resp) == 0 || !strings.HasPrefix(resp[0], "220") {
		result.Status = StatusFail
		result.Summary = "STARTTLS failed on port 587"
		result.Duration = time.Since(start)
		return result
	}

	// Upgrade to TLS
	tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
	if err = tlsConn.Handshake(); err != nil {
		result.Status = StatusFail
		result.Summary = "TLS handshake failed on port 587"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	tlsReader := bufio.NewReader(tlsConn)

	// EHLO again after TLS
	fmt.Fprintf(tlsConn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(tlsReader); err != nil {
		result.Status = StatusFail
		result.Summary = "EHLO after STARTTLS failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// AUTH PLAIN
	authStr := fmt.Sprintf("\x00%s\x00%s", user, pass)
	encoded := base64Encode([]byte(authStr))
	fmt.Fprintf(tlsConn, "AUTH PLAIN %s\r\n", encoded)
	authResp, err := readSMTPResponse(tlsReader)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "AUTH PLAIN failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	if len(authResp) == 0 || !strings.HasPrefix(authResp[0], "235") {
		result.Status = StatusFail
		result.Summary = "Authentication rejected"
		if len(authResp) > 0 {
			result.Detail = authResp[0]
		}
		result.Duration = time.Since(start)
		return result
	}

	// MAIL FROM
	fmt.Fprintf(tlsConn, "MAIL FROM:<%s>\r\n", user)
	mailResp, err := readSMTPResponse(tlsReader)
	if err != nil || len(mailResp) == 0 || !strings.HasPrefix(mailResp[0], "250") {
		result.Status = StatusFail
		result.Summary = "MAIL FROM rejected"
		result.Duration = time.Since(start)
		return result
	}

	// RCPT TO
	fmt.Fprintf(tlsConn, "RCPT TO:<%s>\r\n", sendTo)
	rcptResp, err := readSMTPResponse(tlsReader)
	if err != nil || len(rcptResp) == 0 || !strings.HasPrefix(rcptResp[0], "250") {
		result.Status = StatusFail
		result.Summary = "RCPT TO rejected"
		result.Duration = time.Since(start)
		return result
	}

	// DATA
	fmt.Fprintf(tlsConn, "DATA\r\n")
	dataResp, err := readSMTPResponse(tlsReader)
	if err != nil || len(dataResp) == 0 || !strings.HasPrefix(dataResp[0], "354") {
		result.Status = StatusFail
		result.Summary = "DATA rejected"
		result.Duration = time.Since(start)
		return result
	}

	// Send message
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Instant Mail Check Auth Test\r\nDate: %s\r\nMessage-ID: <imc-auth-%d@instantmailcheck>\r\n\r\nAuthenticated test message from Instant Mail Check.\r\nhttps://restmail.io\r\n",
		user, sendTo, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())
	fmt.Fprintf(tlsConn, "%s\r\n.\r\n", msg)
	endResp, err := readSMTPResponse(tlsReader)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "Message delivery failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	fmt.Fprintf(tlsConn, "QUIT\r\n")

	if len(endResp) > 0 && strings.HasPrefix(endResp[0], "250") {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("Authenticated send to %s succeeded", sendTo)
		result.Detail = endResp[0]
	} else {
		result.Status = StatusWarn
		result.Summary = "Message may not have been accepted"
		if len(endResp) > 0 {
			result.Detail = endResp[0]
		}
	}

	result.Duration = time.Since(start)
	return result
}
