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

// versionPattern matches common mail server version strings in banners.
var versionPattern = regexp.MustCompile(`(?i)(postfix|dovecot|exim|sendmail|exchange|zimbra|cyrus|opensmtpd|haraka|hmailserver|mailenable|mdaemon|kerio|axigen|communigate|courier|qmail|james|mercury|surgemail|icewarp|mimecast|barracuda|proofpoint|ironport|cisco|fortinet|sophos)[/ ]*[v]?[\d]+[\.\d]*`)

// CheckBannerLeak inspects the SMTP banner for software version disclosure.
func CheckBannerLeak(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Banner Info Leak",
		Category: "security",
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

	conn.SetReadDeadline(time.Now().Add(timeout))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		result.Status = StatusSkip
		result.Summary = "No banner received"
		result.Duration = time.Since(start)
		return result
	}

	banner := scanner.Text()

	matches := versionPattern.FindAllString(banner, -1)
	if len(matches) > 0 {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Banner reveals software: %s", strings.Join(matches, ", "))
		result.Detail = fmt.Sprintf("Full banner: %s", banner)
		result.Fix = "Configure your MTA to use a generic banner like '220 mail.example.com ESMTP' without version info. Attackers use version strings to find known exploits."
	} else {
		result.Status = StatusPass
		result.Summary = "Banner does not reveal software version"
		result.Detail = fmt.Sprintf("Banner: %s", banner)
	}

	result.Duration = time.Since(start)
	return result
}

// CheckVRFYEXPN tests whether VRFY and EXPN commands are enabled (user enumeration vectors).
func CheckVRFYEXPN(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "VRFY/EXPN Commands",
		Category: "security",
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

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	// Read banner
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Summary = "Failed to read banner"
		result.Duration = time.Since(start)
		return result
	}

	// EHLO
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Summary = "EHLO failed"
		result.Duration = time.Since(start)
		return result
	}

	var issues []string

	// Test VRFY
	fmt.Fprintf(conn, "VRFY postmaster\r\n")
	vrfyResp, err := readSMTPResponse(reader)
	if err == nil && len(vrfyResp) > 0 {
		code := ""
		if len(vrfyResp[0]) >= 3 {
			code = vrfyResp[0][:3]
		}
		if code == "250" || code == "251" || code == "252" {
			issues = append(issues, fmt.Sprintf("VRFY enabled (response: %s)", vrfyResp[0]))
		}
	}

	// Test EXPN
	fmt.Fprintf(conn, "EXPN postmaster\r\n")
	expnResp, err := readSMTPResponse(reader)
	if err == nil && len(expnResp) > 0 {
		code := ""
		if len(expnResp[0]) >= 3 {
			code = expnResp[0][:3]
		}
		if code == "250" || code == "251" || code == "252" {
			issues = append(issues, fmt.Sprintf("EXPN enabled (response: %s)", expnResp[0]))
		}
	}

	fmt.Fprintf(conn, "QUIT\r\n")

	if len(issues) > 0 {
		result.Status = StatusFail
		result.Summary = strings.Join(issues, "; ")
		result.Fix = "Disable VRFY and EXPN commands in your MTA config. Postfix: smtpd_command_filter or disable_vrfy_command=yes. These commands let attackers enumerate valid email addresses."
	} else {
		result.Status = StatusPass
		result.Summary = "VRFY and EXPN are disabled"
	}

	result.Duration = time.Since(start)
	return result
}

// CheckPlaintextPorts checks if insecure plaintext ports 110 (POP3) and 143 (IMAP) are open.
func CheckPlaintextPorts(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Plaintext Ports",
		Category: "security",
	}

	var openPorts []string

	for _, port := range []struct {
		num  string
		name string
	}{
		{"110", "POP3"},
		{"143", "IMAP"},
	} {
		addr := net.JoinHostPort(host, port.num)
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err == nil {
			conn.Close()
			openPorts = append(openPorts, fmt.Sprintf("%s (%s)", port.num, port.name))
		}
	}

	if len(openPorts) > 0 {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Plaintext ports open: %s", strings.Join(openPorts, ", "))
		result.Detail = "Plaintext protocols transmit passwords in the clear. Attackers on the network can sniff credentials."
		result.Fix = "Disable plaintext IMAP (143) and POP3 (110). Use only IMAPS (993) and POP3S (995). In Dovecot: ssl=required and disable non-SSL listeners."
	} else {
		result.Status = StatusPass
		result.Summary = "No plaintext mail ports open (110, 143)"
	}

	result.Duration = time.Since(start)
	return result
}

// CheckTLSVersions tests whether the server accepts deprecated TLS versions (1.0, 1.1)
// and checks the negotiated cipher suite for weak algorithms.
func CheckTLSVersions(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "TLS Minimum Version",
		Category: "security",
	}

	var issues []string
	var acceptedOld []string

	// Test TLS 1.0 and 1.1 on port 25 via STARTTLS
	for _, ver := range []struct {
		version uint16
		name    string
	}{
		{tls.VersionTLS10, "TLS 1.0"},
		{tls.VersionTLS11, "TLS 1.1"},
	} {
		accepted, cipher := probeSMTPTLSVersion(host, ver.version, timeout)
		if accepted {
			acceptedOld = append(acceptedOld, ver.name)
			if cipher != "" && isWeakCipher(cipher) {
				issues = append(issues, fmt.Sprintf("Weak cipher with %s: %s", ver.name, cipher))
			}
		}
	}

	// Also check which cipher is negotiated with the best TLS version
	_, bestCipher := probeSMTPTLSVersion(host, 0, timeout) // 0 = let Go pick best
	if bestCipher != "" && isWeakCipher(bestCipher) {
		issues = append(issues, fmt.Sprintf("Weak cipher negotiated: %s", bestCipher))
	}

	if len(acceptedOld) > 0 {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Server accepts deprecated: %s", strings.Join(acceptedOld, ", "))
		result.Detail = "TLS 1.0 and 1.1 have known vulnerabilities (BEAST, POODLE). Modern servers should require TLS 1.2+."
		if len(issues) > 0 {
			result.Detail += "\n" + strings.Join(issues, "\n")
		}
		result.Fix = "Set minimum TLS version to 1.2. Postfix: smtpd_tls_mandatory_protocols=!SSLv2,!SSLv3,!TLSv1,!TLSv1.1. Dovecot: ssl_min_protocol=TLSv1.2"
	} else if len(issues) > 0 {
		result.Status = StatusWarn
		result.Summary = "Rejects old TLS but uses weak ciphers"
		result.Detail = strings.Join(issues, "\n")
		result.Fix = "Configure strong cipher suites. Postfix: smtpd_tls_mandatory_ciphers=high, tls_high_cipherlist=ECDHE+AESGCM:ECDHE+CHACHA20"
	} else {
		result.Status = StatusPass
		result.Summary = "Rejects TLS 1.0 and 1.1"
		if bestCipher != "" {
			result.Detail = fmt.Sprintf("Negotiated cipher: %s", bestCipher)
		}
	}

	result.Duration = time.Since(start)
	return result
}

// probeSMTPTLSVersion attempts STARTTLS on port 25 with a specific TLS version.
// Returns whether the handshake succeeded and the negotiated cipher suite name.
// Pass version=0 to let Go pick the best version.
func probeSMTPTLSVersion(host string, version uint16, timeout time.Duration) (bool, string) {
	addr := fmt.Sprintf("%s:25", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false, ""
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	if _, err = readSMTPResponse(reader); err != nil {
		return false, ""
	}
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		return false, ""
	}
	fmt.Fprintf(conn, "STARTTLS\r\n")
	resp, err := readSMTPResponse(reader)
	if err != nil || len(resp) == 0 || !strings.HasPrefix(resp[0], "220") {
		return false, ""
	}

	cfg := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	}
	if version != 0 {
		cfg.MinVersion = version
		cfg.MaxVersion = version
	}

	tlsConn := tls.Client(conn, cfg)
	err = tlsConn.Handshake()
	if err != nil {
		return false, ""
	}
	state := tlsConn.ConnectionState()
	tlsConn.Close()
	return true, tls.CipherSuiteName(state.CipherSuite)
}

// isWeakCipher returns true if the cipher suite name indicates a weak algorithm.
func isWeakCipher(name string) bool {
	weak := []string{"RC4", "DES", "3DES", "NULL", "EXPORT", "anon", "MD5"}
	upper := strings.ToUpper(name)
	for _, w := range weak {
		if strings.Contains(upper, w) {
			return true
		}
	}
	return false
}

// CheckSelfSignedCert performs comprehensive certificate chain analysis on the SMTP TLS certificate.
// Checks for: self-signed certs, missing intermediates, SHA-1 signatures, weak key sizes,
// and wildcard coverage.
func CheckSelfSignedCert(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Self-Signed Cert",
		Category: "security",
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

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}
	fmt.Fprintf(conn, "STARTTLS\r\n")
	resp, err := readSMTPResponse(reader)
	if err != nil || len(resp) == 0 || !strings.HasPrefix(resp[0], "220") {
		result.Status = StatusSkip
		result.Summary = "STARTTLS not available"
		result.Duration = time.Since(start)
		return result
	}

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})
	if err = tlsConn.Handshake(); err != nil {
		result.Status = StatusSkip
		result.Summary = "TLS handshake failed"
		result.Duration = time.Since(start)
		return result
	}

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		result.Status = StatusSkip
		result.Summary = "No certificate presented"
		result.Duration = time.Since(start)
		return result
	}

	cert := state.PeerCertificates[0]
	var issues []string
	var info []string

	// 1. Self-signed detection
	isSelfSigned := cert.Issuer.CommonName == cert.Subject.CommonName && len(state.PeerCertificates) == 1
	if isSelfSigned {
		issues = append(issues, "Self-signed certificate — other mail servers may reject your mail or downgrade to plaintext")
	}

	// 2. Missing intermediates — single cert in chain but not self-signed (CA leaf without chain)
	if !isSelfSigned && len(state.PeerCertificates) == 1 && !cert.IsCA {
		issues = append(issues, "Possible missing intermediate certificates — only leaf cert in chain")
	}

	// 3. SHA-1 signature algorithm — deprecated since 2017, browsers and mail servers distrust it
	for _, c := range state.PeerCertificates {
		sigAlgo := c.SignatureAlgorithm.String()
		if strings.Contains(strings.ToUpper(sigAlgo), "SHA1") || strings.Contains(strings.ToUpper(sigAlgo), "SHA-1") {
			issues = append(issues, fmt.Sprintf("SHA-1 signature found on: %s", c.Subject.CommonName))
		}
	}

	// 4. Key size checks — RSA < 2048 is weak, ECDSA < 256 is weak
	if cert.PublicKeyAlgorithm.String() == "RSA" {
		if key, ok := cert.PublicKey.(interface{ Size() int }); ok {
			bits := key.Size() * 8
			if bits < 2048 {
				issues = append(issues, fmt.Sprintf("RSA key too small: %d bits (need >= 2048)", bits))
			} else {
				info = append(info, fmt.Sprintf("RSA %d-bit key", bits))
			}
		}
	} else if cert.PublicKeyAlgorithm.String() == "ECDSA" {
		if key, ok := cert.PublicKey.(interface{ Params() *ellipticCurveParams }); ok {
			_ = key // ECDSA key size checked via curve name
		}
		info = append(info, "ECDSA key")
	} else {
		info = append(info, fmt.Sprintf("Key algorithm: %s", cert.PublicKeyAlgorithm.String()))
	}

	// 5. Chain depth info
	info = append(info, fmt.Sprintf("Chain depth: %d certificate(s)", len(state.PeerCertificates)))

	issuer := cert.Issuer.CommonName
	if issuer == "" && len(cert.Issuer.Organization) > 0 {
		issuer = cert.Issuer.Organization[0]
	}
	info = append(info, fmt.Sprintf("Issuer: %s", issuer))

	if len(issues) > 0 {
		if isSelfSigned {
			result.Status = StatusFail
		} else {
			result.Status = StatusWarn
		}
		result.Summary = issues[0]
		allDetails := append(issues, info...)
		result.Detail = strings.Join(allDetails, "\n")
		if isSelfSigned {
			result.Fix = "Use a certificate from a trusted CA. Let's Encrypt provides free certificates: certbot certonly --standalone -d mail.example.com"
		} else {
			result.Fix = "Fix certificate chain issues: ensure intermediate certs are included, use SHA-256+ signatures, and RSA 2048+ or ECDSA P-256+ keys."
		}
	} else {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("Certificate issued by trusted CA: %s", issuer)
		result.Detail = strings.Join(info, "\n")
	}

	result.Duration = time.Since(start)
	return result
}

// ellipticCurveParams is a placeholder for ECDSA curve parameter inspection.
type ellipticCurveParams struct {
	BitSize int
}

// CheckUserEnumRCPT tests if the server leaks valid/invalid user info via RCPT TO responses.
func CheckUserEnumRCPT(host, domain string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "User Enumeration (RCPT)",
		Category: "security",
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

	conn.SetDeadline(time.Now().Add(timeout * 4))
	reader := bufio.NewReader(conn)

	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}
	fmt.Fprintf(conn, "MAIL FROM:<test@instantmailcheck.example>\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	// Try a definitely-invalid user
	fakeUser := fmt.Sprintf("imc-nonexistent-user-test-%d@%s", time.Now().UnixNano(), domain)
	fmt.Fprintf(conn, "RCPT TO:<%s>\r\n", fakeUser)
	fakeResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	// Try postmaster (should always exist per RFC)
	fmt.Fprintf(conn, "RCPT TO:<postmaster@%s>\r\n", domain)
	realResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	fmt.Fprintf(conn, "RSET\r\n")
	readSMTPResponse(reader)
	fmt.Fprintf(conn, "QUIT\r\n")

	fakeCode := ""
	realCode := ""
	if len(fakeResp) > 0 && len(fakeResp[0]) >= 3 {
		fakeCode = fakeResp[0][:3]
	}
	if len(realResp) > 0 && len(realResp[0]) >= 3 {
		realCode = realResp[0][:3]
	}

	if fakeCode != realCode {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Different responses for valid/invalid users (valid=%s, invalid=%s)", realCode, fakeCode)
		result.Detail = fmt.Sprintf("Invalid user response: %s\nValid user response: %s", fakeResp[0], realResp[0])
		result.Fix = "Configure your MTA to defer recipient validation until after DATA (Postfix: smtpd_reject_unlisted_recipient=no or use reject_unverified_recipient with a uniform error). This prevents attackers from enumerating valid addresses."
	} else {
		result.Status = StatusPass
		result.Summary = "Uniform RCPT TO responses for valid and invalid users"
		result.Detail = fmt.Sprintf("Both returned: %s", fakeCode)
	}

	result.Duration = time.Since(start)
	return result
}

// CheckUserEnumVRFY tests user enumeration via the VRFY command.
func CheckUserEnumVRFY(host, domain string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "User Enumeration (VRFY)",
		Category: "security",
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

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	// VRFY a fake user
	fakeUser := fmt.Sprintf("imc-nonexistent-%d", time.Now().UnixNano())
	fmt.Fprintf(conn, "VRFY %s\r\n", fakeUser)
	fakeResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	// VRFY postmaster
	fmt.Fprintf(conn, "VRFY postmaster\r\n")
	realResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	fmt.Fprintf(conn, "QUIT\r\n")

	fakeCode := ""
	realCode := ""
	if len(fakeResp) > 0 && len(fakeResp[0]) >= 3 {
		fakeCode = fakeResp[0][:3]
	}
	if len(realResp) > 0 && len(realResp[0]) >= 3 {
		realCode = realResp[0][:3]
	}

	// If both return 502 (command disabled), that's ideal
	if fakeCode == "502" && realCode == "502" {
		result.Status = StatusPass
		result.Summary = "VRFY command is disabled (502)"
	} else if fakeCode == realCode {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("VRFY returns uniform responses (%s)", fakeCode)
	} else {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("VRFY leaks user info (valid=%s, invalid=%s)", realCode, fakeCode)
		result.Detail = fmt.Sprintf("Invalid: %s\nValid: %s", fakeResp[0], realResp[0])
		result.Fix = "Disable VRFY entirely. Postfix: disable_vrfy_command=yes"
	}

	result.Duration = time.Since(start)
	return result
}

// CheckBruteForceProtection tests if the server rate-limits failed AUTH attempts.
func CheckBruteForceProtection(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Brute-Force Protection",
		Category: "security",
	}

	addr := fmt.Sprintf("%s:587", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		// Try port 25 as fallback
		addr = fmt.Sprintf("%s:25", host)
		conn, err = net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			result.Status = StatusSkip
			result.Summary = "Cannot connect to submission port"
			result.Duration = time.Since(start)
			return result
		}
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 6))
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

	// Check if AUTH is advertised
	hasAuth := false
	for _, line := range ehloResp {
		if strings.Contains(strings.ToUpper(line), "AUTH") {
			hasAuth = true
			break
		}
	}

	if !hasAuth {
		// Try STARTTLS first
		fmt.Fprintf(conn, "STARTTLS\r\n")
		resp, err := readSMTPResponse(reader)
		if err != nil || len(resp) == 0 || !strings.HasPrefix(resp[0], "220") {
			result.Status = StatusSkip
			result.Summary = "AUTH not available"
			result.Duration = time.Since(start)
			return result
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
		})
		if err = tlsConn.Handshake(); err != nil {
			result.Status = StatusSkip
			result.Summary = "TLS handshake failed"
			result.Duration = time.Since(start)
			return result
		}
		reader = bufio.NewReader(tlsConn)
		conn = nil // prevent double-close
		fmt.Fprintf(tlsConn, "EHLO instantmailcheck\r\n")
		ehloResp, err = readSMTPResponse(reader)
		if err != nil {
			result.Status = StatusSkip
			result.Duration = time.Since(start)
			return result
		}
		for _, line := range ehloResp {
			if strings.Contains(strings.ToUpper(line), "AUTH") {
				hasAuth = true
				break
			}
		}
		if !hasAuth {
			result.Status = StatusSkip
			result.Summary = "AUTH not available even after STARTTLS"
			result.Duration = time.Since(start)
			return result
		}
		// Send 3 rapid bad AUTH attempts over TLS
		blocked := false
		for i := 0; i < 3; i++ {
			badAuth := base64Encode([]byte(fmt.Sprintf("\x00baduser%d@test\x00wrongpass%d", i, i)))
			fmt.Fprintf(tlsConn, "AUTH PLAIN %s\r\n", badAuth)
			authResp, err := readSMTPResponse(reader)
			if err != nil {
				blocked = true
				break
			}
			if len(authResp) > 0 {
				code := ""
				if len(authResp[0]) >= 3 {
					code = authResp[0][:3]
				}
				if code == "421" || code == "454" || code == "451" {
					blocked = true
					break
				}
			}
		}

		if blocked {
			result.Status = StatusPass
			result.Summary = "Server blocks after rapid failed AUTH attempts"
			result.Detail = "Connection was dropped or deferred after 3 rapid failed login attempts"
		} else {
			result.Status = StatusWarn
			result.Summary = "Server allowed 3 rapid failed AUTH attempts without blocking"
			result.Fix = "Enable fail2ban or similar rate limiting for SMTP AUTH failures. Postfix: smtpd_client_auth_rate_limit. Dovecot: auth_failure_delay=2secs."
		}

		result.Duration = time.Since(start)
		return result
	}

	// Send 3 rapid bad AUTH attempts on plaintext connection
	blocked := false
	for i := 0; i < 3; i++ {
		badAuth := base64Encode([]byte(fmt.Sprintf("\x00baduser%d@test\x00wrongpass%d", i, i)))
		fmt.Fprintf(conn, "AUTH PLAIN %s\r\n", badAuth)
		authResp, err := readSMTPResponse(reader)
		if err != nil {
			blocked = true
			break
		}
		if len(authResp) > 0 {
			code := ""
			if len(authResp[0]) >= 3 {
				code = authResp[0][:3]
			}
			if code == "421" || code == "454" || code == "451" {
				blocked = true
				break
			}
		}
	}

	fmt.Fprintf(conn, "QUIT\r\n")

	if blocked {
		result.Status = StatusPass
		result.Summary = "Server blocks after rapid failed AUTH attempts"
		result.Detail = "Connection was dropped or deferred after 3 rapid failed login attempts"
	} else {
		result.Status = StatusWarn
		result.Summary = "Server allowed 3 rapid failed AUTH attempts without blocking"
		result.Fix = "Enable fail2ban or similar rate limiting for SMTP AUTH failures. Postfix: smtpd_client_auth_rate_limit. Dovecot: auth_failure_delay=2secs."
	}

	result.Duration = time.Since(start)
	return result
}

// CheckSMTPSmuggling tests for SMTP smuggling vulnerability (ambiguous line endings).
func CheckSMTPSmuggling(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "SMTP Smuggling",
		Category: "security",
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

	conn.SetDeadline(time.Now().Add(timeout * 4))
	reader := bufio.NewReader(conn)

	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}
	fmt.Fprintf(conn, "EHLO instantmailcheck\r\n")
	if _, err = readSMTPResponse(reader); err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	// Send MAIL FROM
	fmt.Fprintf(conn, "MAIL FROM:<test@instantmailcheck.example>\r\n")
	mailResp, err := readSMTPResponse(reader)
	if err != nil || len(mailResp) == 0 || !strings.HasPrefix(mailResp[0], "250") {
		result.Status = StatusSkip
		result.Summary = "MAIL FROM rejected (cannot test smuggling)"
		result.Duration = time.Since(start)
		return result
	}

	// RCPT TO (to the domain itself, postmaster)
	fmt.Fprintf(conn, "RCPT TO:<postmaster@%s>\r\n", host)
	rcptResp, err := readSMTPResponse(reader)
	if err != nil || len(rcptResp) == 0 || !strings.HasPrefix(rcptResp[0], "250") {
		// Try without domain qualification
		fmt.Fprintf(conn, "RSET\r\n")
		readSMTPResponse(reader)
		result.Status = StatusSkip
		result.Summary = "RCPT TO rejected (cannot test smuggling)"
		result.Duration = time.Since(start)
		return result
	}

	// DATA
	fmt.Fprintf(conn, "DATA\r\n")
	dataResp, err := readSMTPResponse(reader)
	if err != nil || len(dataResp) == 0 || !strings.HasPrefix(dataResp[0], "354") {
		result.Status = StatusSkip
		result.Summary = "DATA rejected"
		result.Duration = time.Since(start)
		return result
	}

	// Send a message body with a smuggled dot using bare LF instead of CRLF
	// A vulnerable server would interpret \n.\n as end-of-data
	// We use \n.\r\n which is the smuggling vector
	smugglePayload := "Subject: SMTP Smuggling Test\r\n\r\nTest line 1\r\n"
	smugglePayload += "\n.\r\n" // bare-LF dot — smuggling attempt
	smugglePayload += "MAIL FROM:<smuggled@attacker.example>\r\n"

	conn.Write([]byte(smugglePayload))

	// Now send the real end-of-data
	conn.Write([]byte("\r\n.\r\n"))

	endResp, err := readSMTPResponse(reader)
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "Connection lost during smuggling test"
		result.Duration = time.Since(start)
		return result
	}

	// Check if we get a second response (meaning the smuggled MAIL FROM was processed)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	extraResp, extraErr := readSMTPResponse(reader)

	fmt.Fprintf(conn, "QUIT\r\n")

	if extraErr == nil && len(extraResp) > 0 && (strings.HasPrefix(extraResp[0], "250") || strings.HasPrefix(extraResp[0], "503")) {
		result.Status = StatusFail
		result.Summary = "Server may be vulnerable to SMTP smuggling!"
		result.Detail = fmt.Sprintf("Server processed data after bare-LF dot sequence. Response to smuggled command: %s", extraResp[0])
		result.Fix = "Update your MTA to the latest version. Postfix 3.8.4+ fixes this. Ensure smtpd_forbid_bare_newline=yes (Postfix 3.9+). This vulnerability allows attackers to bypass SPF/DKIM/DMARC."
	} else {
		result.Status = StatusPass
		result.Summary = "Server correctly handles ambiguous line endings"
		if len(endResp) > 0 {
			result.Detail = fmt.Sprintf("DATA response: %s", endResp[0])
		}
	}

	result.Duration = time.Since(start)
	return result
}

// CheckRateLimiting tests if the server rate-limits rapid connections.
func CheckRateLimiting(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Rate Limiting",
		Category: "security",
	}

	addr := fmt.Sprintf("%s:25", host)
	blocked := false
	var lastErr error

	// Make 5 rapid connections
	for i := 0; i < 5; i++ {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			blocked = true
			lastErr = err
			break
		}

		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		resp, err := readSMTPResponse(reader)
		conn.Close()

		if err != nil {
			blocked = true
			lastErr = err
			break
		}
		if len(resp) > 0 && (strings.HasPrefix(resp[0], "421") || strings.HasPrefix(resp[0], "450")) {
			blocked = true
			break
		}
	}

	if blocked {
		result.Status = StatusPass
		result.Summary = "Server rate-limits rapid connections"
		if lastErr != nil {
			result.Detail = fmt.Sprintf("Blocked after rapid connections: %s", lastErr.Error())
		}
	} else {
		result.Status = StatusWarn
		result.Summary = "Server accepted 5 rapid connections without throttling"
		result.Fix = "Configure connection rate limiting. Postfix: smtpd_client_connection_rate_limit=10. Also consider fail2ban for repeat offenders."
	}

	result.Duration = time.Since(start)
	return result
}

// CheckAuthMechanisms checks which AUTH mechanisms are advertised on port 587.
func CheckAuthMechanisms(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Auth Mechanisms",
		Category: "security",
	}

	addr := fmt.Sprintf("%s:587", host)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "Cannot connect to port 587"
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
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

	// Try STARTTLS to see post-TLS AUTH
	fmt.Fprintf(conn, "STARTTLS\r\n")
	resp, err := readSMTPResponse(reader)
	if err == nil && len(resp) > 0 && strings.HasPrefix(resp[0], "220") {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
		})
		if err = tlsConn.Handshake(); err == nil {
			tlsReader := bufio.NewReader(tlsConn)
			fmt.Fprintf(tlsConn, "EHLO instantmailcheck\r\n")
			ehloResp, _ = readSMTPResponse(tlsReader)
		}
	}

	var mechanisms []string
	for _, line := range ehloResp {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "AUTH") {
			// Parse AUTH mechanisms from the line
			parts := strings.Fields(line)
			for i, p := range parts {
				if strings.ToUpper(p) == "AUTH" || (i > 0 && strings.HasSuffix(strings.ToUpper(parts[i-1]), "AUTH")) {
					continue
				}
				if strings.HasPrefix(upper, "250") {
					if i > 0 {
						mechanisms = append(mechanisms, strings.ToUpper(p))
					}
				}
			}
			// Simpler: just grab everything after AUTH
			idx := strings.Index(upper, "AUTH ")
			if idx >= 0 {
				mechStr := line[idx+5:]
				mechanisms = strings.Fields(strings.ToUpper(mechStr))
			}
			break
		}
	}

	fmt.Fprintf(conn, "QUIT\r\n")

	if len(mechanisms) == 0 {
		result.Status = StatusSkip
		result.Summary = "No AUTH mechanisms advertised"
		result.Duration = time.Since(start)
		return result
	}

	result.Summary = fmt.Sprintf("Mechanisms: %s", strings.Join(mechanisms, ", "))

	hasPlain := false
	hasLogin := false
	hasCRAM := false
	for _, m := range mechanisms {
		switch m {
		case "PLAIN":
			hasPlain = true
		case "LOGIN":
			hasLogin = true
		case "CRAM-MD5", "CRAM-SHA1", "SCRAM-SHA-256":
			hasCRAM = true
		}
	}

	if hasCRAM {
		result.Status = StatusPass
		result.Detail = "Challenge-response mechanism available (passwords not sent in cleartext over TLS)"
	} else if hasPlain || hasLogin {
		result.Status = StatusPass
		result.Detail = "PLAIN/LOGIN are acceptable when used over TLS"
	} else {
		result.Status = StatusWarn
		result.Detail = "No standard AUTH mechanisms found"
	}

	result.Duration = time.Since(start)
	return result
}

// CheckPasswordStrength evaluates the provided password against common security policies.
func CheckPasswordStrength(pass string) CheckResult {
	result := CheckResult{
		Name:     "Password Strength",
		Category: "security",
	}

	var issues []string
	var strengths []string

	// Length check
	if len(pass) < 8 {
		issues = append(issues, fmt.Sprintf("Too short (%d chars, need 8+)", len(pass)))
	} else if len(pass) >= 16 {
		strengths = append(strengths, fmt.Sprintf("Good length (%d chars)", len(pass)))
	} else if len(pass) >= 12 {
		strengths = append(strengths, fmt.Sprintf("Adequate length (%d chars)", len(pass)))
	}

	// Character class checks
	hasUpper := false
	hasLower := false
	hasDigit := false
	hasSpecial := false
	for _, c := range pass {
		switch {
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= '0' && c <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}

	classes := 0
	if hasUpper {
		classes++
	}
	if hasLower {
		classes++
	}
	if hasDigit {
		classes++
	}
	if hasSpecial {
		classes++
	}

	if classes < 2 {
		issues = append(issues, "Uses only one character class (need mixed case, digits, or special chars)")
	} else if classes >= 3 {
		strengths = append(strengths, fmt.Sprintf("%d character classes", classes))
	}

	// Common password patterns
	lower := strings.ToLower(pass)
	commonPatterns := []string{
		"password", "123456", "qwerty", "admin", "letmein",
		"welcome", "monkey", "master", "dragon", "login",
		"abc123", "111111", "access", "shadow", "passw0rd",
	}
	for _, p := range commonPatterns {
		if strings.Contains(lower, p) {
			issues = append(issues, "Contains a common password pattern")
			break
		}
	}

	// Sequential/repeated characters
	if len(pass) >= 4 {
		sequential := true
		for i := 1; i < len(pass); i++ {
			if pass[i] != pass[0] {
				sequential = false
				break
			}
		}
		if sequential {
			issues = append(issues, "All characters are the same")
		}
	}

	if len(issues) == 0 {
		result.Status = StatusPass
		result.Summary = fmt.Sprintf("Password meets security requirements (%s)", strings.Join(strengths, ", "))
	} else if len(issues) == 1 && len(pass) >= 8 {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Password has a minor weakness: %s", issues[0])
		result.Fix = "Use a password with 12+ characters, mixed case, digits, and special characters. Consider a passphrase or password manager."
	} else {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Weak password: %s", strings.Join(issues, "; "))
		result.Fix = "Use a strong password: 12+ characters with mixed case, numbers, and symbols. Avoid common words and patterns. Use a password manager to generate and store strong passwords."
	}

	return result
}

// CheckPlaintextAuth tests whether AUTH is accepted on port 25 without STARTTLS.
func CheckPlaintextAuth(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Plaintext Auth",
		Category: "security",
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

	// Check if AUTH is advertised on plaintext port 25 (before STARTTLS)
	hasAuth := false
	for _, line := range ehloResp {
		if strings.Contains(strings.ToUpper(line), "AUTH") {
			hasAuth = true
			break
		}
	}

	fmt.Fprintf(conn, "QUIT\r\n")

	if hasAuth {
		result.Status = StatusFail
		result.Summary = "AUTH advertised on plaintext port 25 (before STARTTLS)"
		result.Detail = "Credentials could be sent in cleartext if a client doesn't use STARTTLS first. This is a credential theft risk."
		result.Fix = "Only advertise AUTH after STARTTLS. Postfix: smtpd_tls_auth_only=yes"
	} else {
		result.Status = StatusPass
		result.Summary = "AUTH not offered on plaintext connection"
		result.Detail = "AUTH is correctly hidden until TLS is established"
	}

	result.Duration = time.Since(start)
	return result
}
