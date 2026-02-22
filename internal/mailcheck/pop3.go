package mailcheck

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"
)

// CheckPOP3S connects to port 995 and checks availability.
func CheckPOP3S(host string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "POP3S Port 995",
		Category: "pop3",
	}

	addr := fmt.Sprintf("%s:995", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	})
	if err != nil {
		result.Status = StatusSkip
		result.Summary = fmt.Sprintf("Cannot connect to %s (POP3 may not be enabled)", addr)
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
		result.Summary = "Connected but no POP3 greeting received"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}

	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "+OK") {
		result.Status = StatusWarn
		result.Summary = fmt.Sprintf("Unexpected POP3 greeting: %s", line)
		result.Duration = time.Since(start)
		return result
	}

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("POP3 ready: %s", truncate(line, 80))
	result.Duration = time.Since(start)
	return result
}

// POP3Login connects to POP3S, logs in, and runs STAT.
func POP3Login(host, user, pass string, timeout time.Duration) CheckResult {
	start := time.Now()
	result := CheckResult{
		Name:     "POP3 Login",
		Category: "pop3",
	}

	addr := fmt.Sprintf("%s:995", host)
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName: host,
	})
	if err != nil {
		result.Status = StatusFail
		result.Summary = "Cannot connect to POP3S"
		result.Detail = err.Error()
		result.Fix = "Ensure POP3S is enabled on port 995. Dovecot: service pop3-login { inet_listener pop3s { port = 995, ssl = yes } }"
		result.Duration = time.Since(start)
		return result
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout * 3))
	reader := bufio.NewReader(conn)

	// Read greeting
	greeting, err := reader.ReadString('\n')
	if err != nil {
		result.Status = StatusFail
		result.Summary = "No POP3 greeting"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	greeting = strings.TrimRight(greeting, "\r\n")

	if !strings.HasPrefix(greeting, "+OK") {
		result.Status = StatusFail
		result.Summary = fmt.Sprintf("Bad POP3 greeting: %s", greeting)
		result.Duration = time.Since(start)
		return result
	}

	// USER
	fmt.Fprintf(conn, "USER %s\r\n", user)
	userResp, err := reader.ReadString('\n')
	if err != nil {
		result.Status = StatusFail
		result.Summary = "POP3 USER command failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	userResp = strings.TrimRight(userResp, "\r\n")

	if !strings.HasPrefix(userResp, "+OK") {
		result.Status = StatusFail
		result.Summary = "POP3 USER rejected"
		result.Detail = userResp
		result.Duration = time.Since(start)
		return result
	}

	// PASS
	fmt.Fprintf(conn, "PASS %s\r\n", pass)
	passResp, err := reader.ReadString('\n')
	if err != nil {
		result.Status = StatusFail
		result.Summary = "POP3 PASS command failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	passResp = strings.TrimRight(passResp, "\r\n")

	if !strings.HasPrefix(passResp, "+OK") {
		result.Status = StatusFail
		result.Summary = "POP3 login rejected"
		result.Detail = passResp
		result.Fix = "Check credentials. Dovecot: verify auth_mechanisms includes 'plain' and passdb is configured correctly. Check dovecot logs for auth failures."
		result.Duration = time.Since(start)
		return result
	}

	// STAT
	fmt.Fprintf(conn, "STAT\r\n")
	statResp, err := reader.ReadString('\n')
	if err != nil {
		result.Status = StatusWarn
		result.Summary = "Logged in but STAT failed"
		result.Detail = err.Error()
		result.Duration = time.Since(start)
		return result
	}
	statResp = strings.TrimRight(statResp, "\r\n")

	// QUIT
	fmt.Fprintf(conn, "QUIT\r\n")

	result.Status = StatusPass
	result.Summary = fmt.Sprintf("Login successful: %s", statResp)
	result.Duration = time.Since(start)
	return result
}
