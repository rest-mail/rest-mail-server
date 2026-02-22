package mailcheck

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"
)

// CheckSMTPBanner connects to port 25 and reads the SMTP banner.
func CheckSMTPBanner(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "SMTP Banner",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:25", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Cannot connect to %s", addr)
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(timeout))
	scanner := bufio.NewScanner(conn)
	if scanner.Scan() {
		banner := scanner.Text()
		result.Status = StatusPass
		result.Summary = banner
	} else {
		result.Status = StatusFail
		result.Summary = "No SMTP banner received"
		if scanner.Err() != nil {
			result.Detail = scanner.Err().Error()
		}
	}

	result.Duration = time.Since(start)
	return result
}

// CheckSTARTTLS connects to port 25, sends EHLO, and checks for STARTTLS support.
func CheckSTARTTLS(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "SMTP STARTTLS",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:25", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Cannot connect to %s", addr)
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 2))
	reader := bufio.NewReader(conn)

	// Read banner
	_, err = readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "Failed to read SMTP banner"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// Send EHLO
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	ehloResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "EHLO failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// Check for STARTTLS
	hasSTARTTLS := false
	for _, line := range ehloResp {
		if strings.Contains(strings.ToUpper(line), "STARTTLS") {
			hasSTARTTLS = true
			break
		}
	}

	if !hasSTARTTLS {
		result.Status = StatusFail
		result.Summary = "STARTTLS not advertised"
		result.Detail = "Server does not support STARTTLS on port 25. Mail will be sent in plaintext."
		result.Fix = "Enable STARTTLS on port 25. Postfix: smtpd_tls_security_level=may. Without STARTTLS, all email is transmitted in plaintext."
		result.Duration = time.Since(start)
		return result
	}

	// Try upgrading to TLS
	fmt.Fprintf(conn, "STARTTLS\r\n")
	startTLSResp, err := readSMTPResponse(reader)
	if err != nil || len(startTLSResp) == 0 || !strings.HasPrefix(startTLSResp[0], "220") {
		result.Status = StatusWarn
		result.Summary = "STARTTLS advertised but upgrade failed"
		if err != nil {
			result.Detail = err.Error()
		}
		result.Duration = time.Since(start)
		return result
	}

	// Upgrade to TLS
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})
	err = tlsConn.Handshake()
	if err != nil {
		result.Status = StatusWarn
		result.Summary = "STARTTLS handshake failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	defer tlsConn.Close()

	state := tlsConn.ConnectionState()
	result.Status = StatusPass
	result.Summary = fmt.Sprintf("STARTTLS supported, %s", tlsVersionName(state.Version))
	result.Duration = time.Since(start)
	return result
}

// CheckSMTPTLSCert checks the TLS certificate presented after STARTTLS on port 25.
func CheckSMTPTLSCert(host string, timeout time.Duration) CheckResult {
	tlsResult := ProbeTLS(host, 25, timeout)
	// ProbeTLS does direct TLS, but port 25 uses STARTTLS. We need a custom approach.
	// Instead, do STARTTLS and then inspect the cert.
	start := time.Now()
	result := CheckResult{
		Name:     "SMTP TLS Certificate",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:25", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Cannot connect to %s", addr)
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	// Read banner
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusFail
		result.Summary = "Failed to read SMTP banner"
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
		result.Summary = "STARTTLS not available"
		result.Duration = time.Since(start)
		return result
	}

	// TLS handshake
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})
	if err = tlsConn.Handshake(); err != nil {
		result.Status = StatusFail
		result.Summary = "TLS handshake failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// Inspect certificate — reuse the same logic as ProbeTLS
	_ = tlsResult // suppress unused
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		result.Status = StatusFail
		result.Summary = "No certificate presented after STARTTLS"
		result.Duration = time.Since(start)
		return result
	}

	cert := state.PeerCertificates[0]
	daysUntilExpiry := int(time.Until(cert.NotAfter).Hours() / 24)

	issuer := cert.Issuer.CommonName
	if issuer == "" && len(cert.Issuer.Organization) > 0 {
		issuer = cert.Issuer.Organization[0]
	}

	var details []string
	details = append(details, fmt.Sprintf("TLS %s, %s", tlsVersionName(state.Version), tls.CipherSuiteName(state.CipherSuite)))
	details = append(details, fmt.Sprintf("Issuer: %s", issuer))
	details = append(details, fmt.Sprintf("Expires: %s (%d days)", cert.NotAfter.Format("2006-01-02"), daysUntilExpiry))
	if len(cert.DNSNames) > 0 {
		details = append(details, fmt.Sprintf("SANs: %s", strings.Join(cert.DNSNames, ", ")))
	}

	// Verify
	if err = verifyCert(host, state); err != nil {
		result.Status = StatusFail
		result.Summary = "Certificate verification failed"
		result.Fix = "Ensure your SMTP certificate is issued by a trusted CA and covers the MX hostname. Let's Encrypt: certbot certonly -d mail.example.com. Include the full certificate chain."
		details = append(details, fmt.Sprintf("Verify error: %s", err.Error()))
	} else if daysUntilExpiry < 7 {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Certificate expires in %d days!", daysUntilExpiry)
		result.Fix = "Renew your TLS certificate immediately. Let's Encrypt: certbot renew. Set up automatic renewal via cron or systemd timer."
	} else {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("Valid certificate, %s", tlsVersionName(state.Version))
	}

	result.Detail = strings.Join(details, "\n")
	result.Duration = time.Since(start)
	return result
}

// CheckSubmission checks port 587 for STARTTLS and AUTH requirement.
func CheckSubmission(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Submission Port 587",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:587", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Cannot connect to %s", addr)
		result.Detail = err.Error()
		result.Fix = "Enable the submission service on port 587. Postfix: uncomment 'submission' in master.cf. This port is required for authenticated mail sending by clients."
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 2))
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
	ehloResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "EHLO failed on port 587"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	hasSTARTTLS := false
	hasAUTH := false
	for _, line := range ehloResp {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "STARTTLS") {
			hasSTARTTLS = true
		}
		if strings.Contains(upper, "AUTH") {
			hasAUTH = true
		}
	}

	var parts []string
	if hasSTARTTLS {
		parts = append(parts, "STARTTLS supported")
	} else {
		parts = append(parts, "STARTTLS NOT supported")
	}
	if hasAUTH {
		parts = append(parts, "AUTH required")
	} else {
		parts = append(parts, "AUTH not advertised (may require STARTTLS first)")
	}

	if hasSTARTTLS {
		result.Status = StatusPass
	} else {
		result.Status = StatusWarn
		result.Fix = "Enable STARTTLS on port 587. Postfix: in master.cf submission line, add -o smtpd_tls_security_level=encrypt. Without TLS, client credentials are sent in plaintext."
	}
	result.Summary = strings.Join(parts, ", ")
	result.Duration = time.Since(start)
	return result
}

// CheckOpenRelay tests whether the server is an open relay by attempting to
// relay mail to an external address without authentication.
func CheckOpenRelay(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Open Relay Test",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:25", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		result.Status = StatusError
		result.Summary = "Cannot connect to test open relay"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	// Read banner
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusError
		result.Summary = "Failed to read banner"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// EHLO
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusError
		result.Summary = "EHLO failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// MAIL FROM with a fake sender
	fmt.Fprintf(conn, "MAIL FROM:<test@instantmailcheck.example>\r\n")
	mailResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusError
		result.Summary = "MAIL FROM failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	if len(mailResp) > 0 && !strings.HasPrefix(mailResp[0], "250") {
		// Server rejected MAIL FROM — good, it's strict
		result.Status = StatusPass
		result.Summary = "Server rejected unauthenticated MAIL FROM"
		result.Detail = mailResp[0]
		result.Duration = time.Since(start)
		return result
	}

	// RCPT TO with an external address
	fmt.Fprintf(conn, "RCPT TO:<relay-test@example.com>\r\n")
	rcptResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusError
		result.Summary = "RCPT TO failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// Clean up
	fmt.Fprintf(conn, "RSET\r\n")
	readSMTPResponse(reader)
	fmt.Fprintf(conn, "QUIT\r\n")

	if len(rcptResp) > 0 && strings.HasPrefix(rcptResp[0], "250") {
		result.Status = StatusFail
		result.Summary = "SERVER IS AN OPEN RELAY!"
		result.Detail = "The server accepted a relay request to an external address without authentication. This is a critical security issue."
		result.Fix = "URGENT: Restrict relaying immediately. Postfix: smtpd_relay_restrictions=permit_mynetworks,permit_sasl_authenticated,reject_unauth_destination. Your server will be used to send spam and get blacklisted."
	} else {
		result.Status = StatusPass
		result.Summary = "Not an open relay"
		if len(rcptResp) > 0 {
			result.Detail = rcptResp[0]
		}
	}

	result.Duration = time.Since(start)
	return result
}

// SMTPSendTest sends a test email via port 25 (unauthenticated) to the target.
func SMTPSendTest(mxHost, sendTo string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "SMTP Send Test",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:25", mxHost)
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
		result.Summary = "Failed to read banner"
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

	// MAIL FROM
	fmt.Fprintf(conn, "MAIL FROM:<instantmailcheck@instantmailcheck.example>\r\n")
	mailResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "MAIL FROM rejected"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	if len(mailResp) > 0 && !strings.HasPrefix(mailResp[0], "250") {
		result.Status = StatusFail
		result.Summary = "MAIL FROM rejected"
		result.Detail = mailResp[0]
		result.Duration = time.Since(start)
		return result
	}

	// RCPT TO
	fmt.Fprintf(conn, "RCPT TO:<%s>\r\n", sendTo)
	rcptResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "RCPT TO rejected"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	if len(rcptResp) > 0 && !strings.HasPrefix(rcptResp[0], "250") {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("RCPT TO <%s> rejected", sendTo)
		result.Detail = rcptResp[0]
		result.Duration = time.Since(start)
		return result
	}

	// DATA
	fmt.Fprintf(conn, "DATA\r\n")
	dataResp, err := readSMTPResponse(reader)
	if err != nil || (len(dataResp) > 0 && !strings.HasPrefix(dataResp[0], "354")) {
		result.Status = StatusFail
		result.Summary = "DATA command rejected"
		if len(dataResp) > 0 {
			result.Detail = dataResp[0]
		}
		result.Duration = time.Since(start)
		return result
	}

	// Send message
	msg := fmt.Sprintf("From: instantmailcheck@instantmailcheck.example\r\n"+
		"To: %s\r\n"+
		"Subject: Instant Mail Check Test\r\n"+
		"Date: %s\r\n"+
		"Message-ID: <instantmailcheck-%d@instantmailcheck.example>\r\n"+
		"\r\n"+
		"This is a test message from Instant Mail Check.\r\n"+
		"https://restmail.io\r\n",
		sendTo, time.Now().Format(time.RFC1123Z), time.Now().UnixNano())

	fmt.Fprintf(conn, "%s\r\n.\r\n", msg)
	endResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusFail
		result.Summary = "Message delivery failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	fmt.Fprintf(conn, "QUIT\r\n")

	if len(endResp) > 0 && strings.HasPrefix(endResp[0], "250") {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("Test message accepted for delivery to %s", sendTo)
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

// CheckSMTPS connects to port 465 (implicit TLS) and checks the TLS certificate.
func CheckSMTPS(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "SMTPS Port 465",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:465", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "Port 465 not available (optional)"
		result.Detail = "SMTPS port 465 (RFC 8314) uses implicit TLS for email submission. Not all servers enable it."
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		result.Status = StatusWarn
		result.Summary = "Connected to 465 with TLS but no SMTP banner"
		result.Duration = time.Since(start)
		return result
	}

	line = strings.TrimRight(line, "\r\n")
	state := conn.ConnectionState()

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("SMTPS available: %s (%s)", truncateSMTP(line, 60), tlsVersionName(state.Version))
	result.Duration = time.Since(start)
	return result
}

// CheckSMTPExtensions parses EHLO response for SIZE, PIPELINING, and REQUIRETLS.
func CheckSMTPExtensions(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "SMTP Extensions",
		Category: "smtp",
	}

	addr := fmt.Sprintf("%s:25", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "Cannot connect to port 25"
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 2))
	reader := bufio.NewReader(conn)

	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	ehloResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	fmt.Fprintf(conn, "QUIT\r\n")

	var extensions []string
	var sizeLimit string
	hasPipelining := false
	hasRequireTLS := false
	has8BitMIME := false
	hasChunking := false
	hasSMTPUTF8 := false

	for _, line := range ehloResp {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "SIZE") {
			// Parse SIZE limit
			parts := strings.Fields(line)
			for i, p := range parts {
				if strings.ToUpper(p) == "SIZE" && i+1 < len(parts) {
					sizeLimit = parts[i+1]
				} else if strings.HasPrefix(strings.ToUpper(p), "SIZE") && strings.Contains(p, " ") {
					// SIZE might be followed by the limit on same token after stripping prefix
				}
			}
			// Also try splitting on the 250- prefix
			if idx := strings.Index(upper, "SIZE"); idx >= 0 {
				rest := strings.TrimSpace(line[idx+4:])
				if rest != "" {
					sizeLimit = rest
				}
			}
			extensions = append(extensions, "SIZE")
		}
		if strings.Contains(upper, "PIPELINING") {
			hasPipelining = true
			extensions = append(extensions, "PIPELINING")
		}
		if strings.Contains(upper, "REQUIRETLS") {
			hasRequireTLS = true
			extensions = append(extensions, "REQUIRETLS")
		}
		if strings.Contains(upper, "8BITMIME") {
			has8BitMIME = true
			extensions = append(extensions, "8BITMIME")
		}
		if strings.Contains(upper, "CHUNKING") {
			hasChunking = true
			extensions = append(extensions, "CHUNKING")
		}
		if strings.Contains(upper, "SMTPUTF8") {
			hasSMTPUTF8 = true
			extensions = append(extensions, "SMTPUTF8")
		}
	}

	if len(extensions) == 0 {
		result.Status = StatusWarn
		result.Summary = "No notable SMTP extensions found"
		result.Duration = time.Since(start)
		return result
	}

	var details []string
	if sizeLimit != "" {
		// Convert to MB for readability
		details = append(details, fmt.Sprintf("SIZE: max message %s bytes", sizeLimit))
	}
	if hasPipelining {
		details = append(details, "PIPELINING: supported (faster delivery)")
	}
	if hasRequireTLS {
		details = append(details, "REQUIRETLS: supported (RFC 8689)")
	}
	if has8BitMIME {
		details = append(details, "8BITMIME: supported")
	}
	if hasChunking {
		details = append(details, "CHUNKING: supported")
	}
	if hasSMTPUTF8 {
		details = append(details, "SMTPUTF8: supported (international addresses)")
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("%d extensions: %s", len(extensions), strings.Join(extensions, ", "))
	result.Detail = strings.Join(details, "\n")

	result.Duration = time.Since(start)
	return result
}

func truncateSMTP(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// readSMTPResponse reads a multi-line SMTP response.
func readSMTPResponse(reader *bufio.Reader) ([]string, error) {
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return lines, err
		}
		line = strings.TrimRight(line, "\r\n")
		lines = append(lines, line)
		// Multi-line responses have a dash after the code (e.g., "250-")
		// Final line has a space (e.g., "250 ")
		if len(line) >= 4 && line[3] == ' ' {
			break
		}
		if len(line) < 4 {
			break
		}
	}
	return lines, nil
}

// verifyCert verifies a TLS certificate from a connection state.
func verifyCert(host string, state tls.ConnectionState) error {
	if len(state.PeerCertificates) == 0 {
		return fmt.Errorf("no certificates presented")
	}

	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}

	opts := x509.VerifyOptions{
		DNSName:       host,
		Roots:         roots,
		Intermediates: x509.NewCertPool(),
	}
	for _, ic := range state.PeerCertificates[1:] {
		opts.Intermediates.AddCert(ic)
	}

	_, err = state.PeerCertificates[0].Verify(opts)
	return err
}
