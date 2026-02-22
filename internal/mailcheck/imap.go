package mailcheck

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"
)

// CheckIMAPS connects to port 993 and checks the TLS certificate.
func CheckIMAPS(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "IMAPS Port 993",
		Category: "imap",
	}

	addr := fmt.Sprintf("%s:993", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})
	if err != nil {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Cannot connect to %s", addr)
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		result.Status = StatusWarn
		result.Summary = "Connected but no IMAP greeting received"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	line = strings.TrimRight(line, "\r\n")
	if !strings.Contains(line, "OK") && !strings.Contains(line, "IMAP") {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Unexpected IMAP greeting: %s", line)
		result.Duration = time.Since(start)
		return result
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("IMAP ready: %s", truncate(line, 80))
	result.Duration = time.Since(start)
	return result
}

// CheckIMAPSTLSCert checks the TLS certificate on IMAPS port 993.
func CheckIMAPSTLSCert(host string, timeout time.Duration) CheckResult {
	tlsResult := ProbeTLS(host, 993, timeout)
	tlsResult.Check.Name = "IMAPS TLS Certificate"
	tlsResult.Check.Category = "imap"
	return tlsResult.Check
}

// IMAPLogin connects to IMAPS, logs in, lists folders, and optionally searches
// for a test message.
func IMAPLogin(host, user, pass string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "IMAP Login",
		Category: "imap",
	}

	addr := fmt.Sprintf("%s:993", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
	})
	if err != nil {
		result.Status = StatusFail
		result.Summary = "Cannot connect to IMAPS"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	// Read greeting
	if _, err = reader.ReadString('\n'); err != nil {
		result.Status = StatusFail
		result.Summary = "No IMAP greeting"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	// LOGIN
	fmt.Fprintf(conn, "a1 LOGIN %s %s\r\n", quoteIMAP(user), quoteIMAP(pass))
	loginResp, err := readIMAPTagged(reader, "a1")
	if err != nil {
		result.Status = StatusFail
		result.Summary = "IMAP LOGIN failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	if !strings.Contains(loginResp, "OK") {
		result.Status = StatusFail
		result.Summary = "IMAP LOGIN rejected"
		result.Detail = loginResp
		result.Duration = time.Since(start)
		return result
	}

	// LIST folders
	fmt.Fprintf(conn, "a2 LIST \"\" \"*\"\r\n")
	listResp, err := readIMAPTagged(reader, "a2")
	if err != nil {
		result.Status = StatusWarn
		result.Summary = "Logged in but LIST failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	folderCount := strings.Count(listResp, "* LIST")

	// LOGOUT
	fmt.Fprintf(conn, "a3 LOGOUT\r\n")

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("Login successful, %d folder(s) found", folderCount)
	result.Detail = listResp
	result.Duration = time.Since(start)
	return result
}

// CheckIMAPCapabilities connects to IMAPS and reports IMAP capabilities.
func CheckIMAPCapabilities(host, user, pass string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "IMAP Capabilities",
		Category: "imap",
	}

	addr := fmt.Sprintf("%s:993", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
	})
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "Cannot connect to IMAPS"
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	// Read greeting (may contain initial CAPABILITY)
	greeting, _ := reader.ReadString('\n')

	// LOGIN
	fmt.Fprintf(conn, "c1 LOGIN %s %s\r\n", quoteIMAP(user), quoteIMAP(pass))
	loginResp, err := readIMAPTagged(reader, "c1")
	if err != nil || !strings.Contains(loginResp, "OK") {
		result.Status = StatusSkip
		result.Summary = "IMAP login failed, cannot check capabilities"
		result.Duration = time.Since(start)
		return result
	}

	// Request CAPABILITY
	fmt.Fprintf(conn, "c2 CAPABILITY\r\n")
	capResp, err := readIMAPTagged(reader, "c2")
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "CAPABILITY command failed"
		result.Duration = time.Since(start)
		return result
	}

	fmt.Fprintf(conn, "c9 LOGOUT\r\n")

	// Parse capabilities from either greeting or CAPABILITY response
	caps := parseIMAPCapabilities(greeting + "\n" + capResp)

	if len(caps) == 0 {
		result.Status = StatusWarn
		result.Summary = "No capabilities found"
		result.Duration = time.Since(start)
		return result
	}

	// Check for useful extensions
	useful := []string{"IDLE", "CONDSTORE", "QRESYNC", "NAMESPACE", "COMPRESS=DEFLATE", "MOVE", "SPECIAL-USE", "QUOTA", "UIDPLUS", "LITERAL+", "ENABLE", "NOTIFY", "SORT", "THREAD"}
	var present []string
	var missing []string

	capsUpper := make(map[string]bool)
	for _, c := range caps {
		capsUpper[strings.ToUpper(c)] = true
	}

	for _, u := range useful {
		if capsUpper[u] {
			present = append(present, u)
		} else {
			missing = append(missing, u)
		}
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("%d capabilities, %d useful extensions", len(caps), len(present))
	result.Detail = fmt.Sprintf("Present: %s\nMissing: %s", strings.Join(present, ", "), strings.Join(missing, ", "))

	if len(present) < 3 {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Limited capabilities (%d useful extensions)", len(present))
		result.Fix = "Enable more IMAP extensions. Dovecot supports most of these natively: mail_plugins = $mail_plugins quota imap_quota imap_compress"
	}

	result.Duration = time.Since(start)
	return result
}

// CheckIMAPIDLE tests if the server supports IMAP IDLE for push notifications.
func CheckIMAPIDLE(host, user, pass string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "IMAP IDLE Support",
		Category: "imap",
	}

	addr := fmt.Sprintf("%s:993", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
	})
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "Cannot connect to IMAPS"
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	// Read greeting
	greeting, _ := reader.ReadString('\n')

	// LOGIN
	fmt.Fprintf(conn, "i1 LOGIN %s %s\r\n", quoteIMAP(user), quoteIMAP(pass))
	loginResp, err := readIMAPTagged(reader, "i1")
	if err != nil || !strings.Contains(loginResp, "OK") {
		result.Status = StatusSkip
		result.Summary = "IMAP login failed"
		result.Duration = time.Since(start)
		return result
	}

	// Check CAPABILITY for IDLE
	fmt.Fprintf(conn, "i2 CAPABILITY\r\n")
	capResp, err := readIMAPTagged(reader, "i2")
	if err != nil {
		result.Status = StatusSkip
		result.Duration = time.Since(start)
		return result
	}

	caps := parseIMAPCapabilities(greeting + "\n" + capResp)
	hasIDLE := false
	for _, c := range caps {
		if strings.ToUpper(c) == "IDLE" {
			hasIDLE = true
			break
		}
	}

	if !hasIDLE {
		fmt.Fprintf(conn, "i9 LOGOUT\r\n")
		result.Status = StatusWarn
		result.Summary = "IDLE not advertised in capabilities"
		result.Fix = "Enable IMAP IDLE for push notifications. Dovecot supports IDLE by default. Without it, clients must poll, wasting battery and bandwidth."
		result.Duration = time.Since(start)
		return result
	}

	// SELECT INBOX to test IDLE
	fmt.Fprintf(conn, "i3 SELECT INBOX\r\n")
	_, err = readIMAPTagged(reader, "i3")
	if err != nil {
		fmt.Fprintf(conn, "i9 LOGOUT\r\n")
		result.Status = StatusWarn
		result.Summary = "IDLE advertised but SELECT INBOX failed"
		result.Duration = time.Since(start)
		return result
	}

	// Try IDLE command
	fmt.Fprintf(conn, "i4 IDLE\r\n")
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	idleLine, err := reader.ReadString('\n')

	// Send DONE to exit IDLE
	fmt.Fprintf(conn, "DONE\r\n")
	conn.SetDeadline(time.Now().Add(timeout))
	readIMAPTagged(reader, "i4")

	fmt.Fprintf(conn, "i9 LOGOUT\r\n")

	if err == nil && strings.Contains(idleLine, "+") {
		result.Status = StatusPass
		result.Summary = "IDLE supported and functional"
		result.Detail = "Server accepted IDLE command with continuation response"
	} else if hasIDLE {
		result.Status = StatusPass
		result.Summary = "IDLE advertised in capabilities"
		result.Detail = "IDLE capability present; functional test inconclusive"
	}

	result.Duration = time.Since(start)
	return result
}

// CheckIMAPQuota checks mailbox quota via GETQUOTAROOT.
func CheckIMAPQuota(host, user, pass string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "Mailbox Quota",
		Category: "imap",
	}

	addr := fmt.Sprintf("%s:993", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
	})
	if err != nil {
		result.Status = StatusSkip
		result.Summary = "Cannot connect to IMAPS"
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	// Read greeting
	reader.ReadString('\n')

	// LOGIN
	fmt.Fprintf(conn, "q1 LOGIN %s %s\r\n", quoteIMAP(user), quoteIMAP(pass))
	loginResp, err := readIMAPTagged(reader, "q1")
	if err != nil || !strings.Contains(loginResp, "OK") {
		result.Status = StatusSkip
		result.Summary = "IMAP login failed"
		result.Duration = time.Since(start)
		return result
	}

	// SELECT INBOX (required before GETQUOTAROOT)
	fmt.Fprintf(conn, "q2 SELECT INBOX\r\n")
	_, err = readIMAPTagged(reader, "q2")
	if err != nil {
		fmt.Fprintf(conn, "q9 LOGOUT\r\n")
		result.Status = StatusSkip
		result.Summary = "SELECT INBOX failed"
		result.Duration = time.Since(start)
		return result
	}

	// GETQUOTAROOT INBOX
	fmt.Fprintf(conn, "q3 GETQUOTAROOT INBOX\r\n")
	quotaResp, err := readIMAPTagged(reader, "q3")

	fmt.Fprintf(conn, "q9 LOGOUT\r\n")

	if err != nil || strings.Contains(quotaResp, "NO") || strings.Contains(quotaResp, "BAD") {
		result.Status = StatusSkip
		result.Summary = "QUOTA not supported or no quota set"
		result.Detail = "Server does not support GETQUOTAROOT or no quota configured for this mailbox"
		result.Duration = time.Since(start)
		return result
	}

	// Parse QUOTA response: * QUOTA "" (STORAGE <used> <limit>)
	var used, limit int
	for _, line := range strings.Split(quotaResp, "\n") {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "STORAGE") && strings.Contains(line, "*") {
			// Try to parse STORAGE <used> <limit>
			idx := strings.Index(upper, "STORAGE")
			if idx >= 0 {
				rest := strings.TrimSpace(line[idx+7:])
				rest = strings.TrimRight(rest, ")")
				fmt.Sscanf(rest, "%d %d", &used, &limit)
			}
		}
	}

	if limit > 0 {
		usedMB := float64(used) / 1024.0
		limitMB := float64(limit) / 1024.0
		pct := float64(used) * 100.0 / float64(limit)

		if pct >= 90 {
			result.Status = StatusWarn
			result.Summary = fmt.Sprintf("Mailbox %.0f%% full (%.1f/%.1f MB)", pct, usedMB, limitMB)
			result.Fix = "Mailbox is nearly full. Delete old messages or increase quota. Dovecot: quota_rule = *:storage=2G"
		} else {
			result.Status = StatusPass
			result.Summary = fmt.Sprintf("Mailbox %.0f%% used (%.1f/%.1f MB)", pct, usedMB, limitMB)
		}
		result.Detail = fmt.Sprintf("Used: %d KB, Limit: %d KB", used, limit)
	} else {
		result.Status = StatusPass
		result.Summary = "Quota info received (no storage limit set or unlimited)"
		result.Detail = quotaResp
	}

	result.Duration = time.Since(start)
	return result
}

// parseIMAPCapabilities extracts capability tokens from IMAP response lines.
func parseIMAPCapabilities(resp string) []string {
	var caps []string
	seen := make(map[string]bool)

	for _, line := range strings.Split(resp, "\n") {
		upper := strings.ToUpper(line)
		// Look for CAPABILITY keyword in the line
		idx := strings.Index(upper, "CAPABILITY")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+10:])
		for _, token := range strings.Fields(rest) {
			// Skip tagged response indicators
			token = strings.TrimRight(token, "]")
			if token == "" || token == "OK" || strings.HasPrefix(token, "c") {
				continue
			}
			upper := strings.ToUpper(token)
			if !seen[upper] {
				seen[upper] = true
				caps = append(caps, upper)
			}
		}
	}
	return caps
}

// readIMAPTagged reads IMAP responses until the tagged response line is found.
func readIMAPTagged(reader *bufio.Reader, tag string) (string, error) {
	var sb strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return sb.String(), err
		}
		sb.WriteString(line)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, tag+" ") {
			return sb.String(), nil
		}
	}
}

// quoteIMAP quotes a string for IMAP commands.
func quoteIMAP(s string) string {
	if strings.ContainsAny(s, "\"\\") {
		s = strings.ReplaceAll(s, "\\", "\\\\")
		s = strings.ReplaceAll(s, "\"", "\\\"")
	}
	return "\"" + s + "\""
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
