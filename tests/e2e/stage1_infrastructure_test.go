package e2e

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testStage1Infrastructure(t *testing.T) {
	t.Run("AllContainersHealthy", func(t *testing.T) {
		resp, err := httpClient.Get(apiBaseURL + "/api/health")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
	})

	t.Run("PostgresConnectivity", func(t *testing.T) {
		resp, err := httpClient.Get(apiBaseURL + "/api/test/db/domains")
		requireNoError(t, err)
		requireStatus(t, resp, http.StatusOK)
	})

	t.Run("SmtpReachable_Mail1", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", mail1SMTPAddr, 10*time.Second)
		if err != nil {
			t.Fatalf("cannot reach SMTP on mail1: %v", err)
		}
		defer conn.Close()

		buf := make([]byte, 512)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		n, _ := conn.Read(buf)
		greeting := string(buf[:n])
		if !strings.HasPrefix(greeting, "220") {
			t.Fatalf("expected 220 greeting from mail1 SMTP, got: %s", greeting)
		}
	})

	t.Run("SmtpReachable_Mail2", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", mail2SMTPAddr, 10*time.Second)
		if err != nil {
			t.Fatalf("cannot reach SMTP on mail2: %v", err)
		}
		defer conn.Close()

		buf := make([]byte, 512)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		n, _ := conn.Read(buf)
		greeting := string(buf[:n])
		if !strings.HasPrefix(greeting, "220") {
			t.Fatalf("expected 220 greeting from mail2 SMTP, got: %s", greeting)
		}
	})

	t.Run("SmtpReachable_Mail3_Gateway", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", mail3SMTPAddr, 10*time.Second)
		if err != nil {
			t.Fatalf("cannot reach SMTP on mail3 (gateway): %v", err)
		}
		defer conn.Close()

		buf := make([]byte, 512)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		n, _ := conn.Read(buf)
		greeting := string(buf[:n])
		if !strings.HasPrefix(greeting, "220") {
			t.Fatalf("expected 220 greeting from mail3 SMTP, got: %s", greeting)
		}
	})

	t.Run("ImapReachable_Mail1", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", mail1IMAPAddr, 10*time.Second)
		if err != nil {
			t.Fatalf("cannot reach IMAP on mail1: %v", err)
		}
		defer conn.Close()

		buf := make([]byte, 512)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		n, _ := conn.Read(buf)
		greeting := string(buf[:n])
		if !strings.Contains(greeting, "OK") {
			t.Fatalf("expected IMAP OK greeting from mail1, got: %s", greeting)
		}
	})

	t.Run("ImapReachable_Mail2", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", mail2IMAPAddr, 10*time.Second)
		if err != nil {
			t.Fatalf("cannot reach IMAP on mail2: %v", err)
		}
		defer conn.Close()

		buf := make([]byte, 512)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		n, _ := conn.Read(buf)
		greeting := string(buf[:n])
		if !strings.Contains(greeting, "OK") {
			t.Fatalf("expected IMAP OK greeting from mail2, got: %s", greeting)
		}
	})

	t.Run("DnsResolution", func(t *testing.T) {
		domains := []struct {
			name string
			ip   string
		}{
			{"mail1.test", "172.20.0.11"},
			{"mail2.test", "172.20.0.12"},
			{"mail3.test", "172.20.0.13"},
		}
		for _, d := range domains {
			addrs := resolveDomain(t, d.name)
			if len(addrs) == 0 {
				t.Logf("WARN: DNS for %s returned no results (may not be using internal DNS)", d.name)
				continue
			}
			found := false
			for _, a := range addrs {
				if a == d.ip {
					found = true
					break
				}
			}
			if !found {
				t.Logf("WARN: %s resolved to %v, expected %s", d.name, addrs, d.ip)
			}
		}
	})
}
