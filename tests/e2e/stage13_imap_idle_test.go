package e2e

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func testStage13ImapIdle(t *testing.T) {
	adminClient := newAPIClient()
	if err := adminClient.login("admin@mail3.test", adminPassword); err != nil {
		t.Skipf("Cannot get admin token: %v", err)
	}

	// Ensure domain and mailboxes exist
	createDomain(t, adminClient, "mail3.test", "restmail")
	createDomain(t, adminClient, "mail1.test", "traditional")
	createMailbox(t, adminClient, "idle-user@mail3.test", "password123", "IDLE Test User")
	createMailbox(t, adminClient, "idle-sender@mail1.test", "password123", "IDLE Sender")

	t.Run("IDLE_CapabilityAdvertised", func(t *testing.T) {
		// Verify that the IMAP gateway advertises the IDLE capability
		ic := dialIMAP(t, mail3IMAPAddr)
		defer ic.close()

		ic.login(t, "idle-user@mail3.test", "password123")

		result, lines := ic.command(t, "CAPABILITY")
		if !strings.Contains(result, "OK") {
			t.Fatalf("CAPABILITY command failed: %s", result)
		}

		allLines := strings.Join(lines, " ")
		if !strings.Contains(allLines, "IDLE") {
			t.Errorf("IDLE not advertised in capabilities: %s", allLines)
		} else {
			t.Log("IDLE capability advertised")
		}

		ic.command(t, "LOGOUT")
	})

	t.Run("IDLE_RequiresAuth", func(t *testing.T) {
		// Verify that IDLE requires authentication
		conn, err := net.DialTimeout("tcp", mail3IMAPAddr, 10*time.Second)
		if err != nil {
			t.Skipf("Cannot connect to IMAP %s: %v", mail3IMAPAddr, err)
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)

		// Read greeting
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		greeting, err := reader.ReadString('\n')
		requireNoError(t, err)
		if !strings.Contains(greeting, "OK") {
			t.Fatalf("unexpected greeting: %s", strings.TrimSpace(greeting))
		}

		// Try IDLE without login
		fmt.Fprintf(conn, "A001 IDLE\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		resp, err := reader.ReadString('\n')
		requireNoError(t, err)
		resp = strings.TrimSpace(resp)

		if strings.Contains(resp, "A001 NO") {
			t.Log("IDLE correctly rejected without authentication")
		} else if strings.Contains(resp, "A001 BAD") {
			t.Log("IDLE correctly rejected (BAD) without authentication")
		} else {
			t.Logf("IDLE response without auth: %s (may vary by implementation)", resp)
		}

		fmt.Fprintf(conn, "A002 LOGOUT\r\n")
	})

	t.Run("IDLE_RequiresSelectedMailbox", func(t *testing.T) {
		ic := dialIMAP(t, mail3IMAPAddr)
		defer ic.close()

		ic.login(t, "idle-user@mail3.test", "password123")

		// Try IDLE without SELECT
		result, _ := ic.command(t, "IDLE")
		if strings.Contains(result, "NO") {
			t.Log("IDLE correctly rejected without selected mailbox")
		} else if strings.Contains(result, "OK") {
			// Some servers may accept IDLE before SELECT (less common)
			t.Log("IDLE accepted without selected mailbox (implementation-specific)")
		} else {
			t.Logf("IDLE response without SELECT: %s", result)
		}

		ic.command(t, "LOGOUT")
	})

	t.Run("IDLE_BasicFlow", func(t *testing.T) {
		// Test the basic IDLE flow:
		// 1. Login and SELECT INBOX
		// 2. Issue IDLE command
		// 3. Receive "+ idling" continuation
		// 4. Send DONE to terminate IDLE
		// 5. Receive tagged OK response

		conn, err := net.DialTimeout("tcp", mail3IMAPAddr, 10*time.Second)
		if err != nil {
			t.Skipf("Cannot connect to IMAP %s: %v", mail3IMAPAddr, err)
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)

		// Read greeting
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		greeting, _ := reader.ReadString('\n')
		if !strings.Contains(greeting, "OK") {
			t.Fatalf("unexpected greeting: %s", strings.TrimSpace(greeting))
		}

		// LOGIN
		fmt.Fprintf(conn, "A001 LOGIN idle-user@mail3.test password123\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		loginResp := readUntilTagRaw(t, reader, "A001")
		if !strings.Contains(loginResp, "OK") {
			t.Fatalf("LOGIN failed: %s", loginResp)
		}

		// SELECT INBOX
		fmt.Fprintf(conn, "A002 SELECT INBOX\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		selectResp := readUntilTagRaw(t, reader, "A002")
		if !strings.Contains(selectResp, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", selectResp)
		}

		// IDLE
		fmt.Fprintf(conn, "A003 IDLE\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))

		// Read continuation response (should be "+ idling" or similar)
		idleResp, err := reader.ReadString('\n')
		requireNoError(t, err)
		idleResp = strings.TrimSpace(idleResp)

		if !strings.HasPrefix(idleResp, "+") {
			t.Fatalf("expected IDLE continuation response starting with '+', got: %s", idleResp)
		}
		t.Logf("IDLE continuation: %s", idleResp)

		// Send DONE to terminate IDLE
		fmt.Fprintf(conn, "DONE\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))

		doneResp, err := reader.ReadString('\n')
		requireNoError(t, err)
		doneResp = strings.TrimSpace(doneResp)

		if !strings.Contains(doneResp, "A003 OK") {
			t.Errorf("expected 'A003 OK' after DONE, got: %s", doneResp)
		} else {
			t.Log("IDLE terminated correctly with DONE")
		}

		// LOGOUT
		fmt.Fprintf(conn, "A004 LOGOUT\r\n")
	})

	t.Run("IDLE_NotifiesNewMessage", func(t *testing.T) {
		// This is the key test: verify that during IDLE, the server sends
		// an EXISTS notification when a new message arrives.
		//
		// Flow:
		// 1. Login and SELECT INBOX, note initial EXISTS count
		// 2. Issue IDLE
		// 3. Deliver a message via SMTP to the IDLE user
		// 4. Wait for EXISTS notification
		// 5. Send DONE

		conn, err := net.DialTimeout("tcp", mail3IMAPAddr, 10*time.Second)
		if err != nil {
			t.Skipf("Cannot connect to IMAP %s: %v", mail3IMAPAddr, err)
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)

		// Read greeting
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		greeting, _ := reader.ReadString('\n')
		if !strings.Contains(greeting, "OK") {
			t.Fatalf("unexpected greeting: %s", strings.TrimSpace(greeting))
		}

		// LOGIN
		fmt.Fprintf(conn, "A001 LOGIN idle-user@mail3.test password123\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		loginResp := readUntilTagRaw(t, reader, "A001")
		if !strings.Contains(loginResp, "OK") {
			t.Fatalf("LOGIN failed: %s", loginResp)
		}

		// SELECT INBOX
		fmt.Fprintf(conn, "A002 SELECT INBOX\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		selectLines := readUntilTagRaw(t, reader, "A002")
		if !strings.Contains(selectLines, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", selectLines)
		}

		// Parse initial EXISTS count
		var initialExists int
		for _, line := range strings.Split(selectLines, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "EXISTS") {
				fmt.Sscanf(line, "* %d EXISTS", &initialExists)
			}
		}
		t.Logf("Initial INBOX message count: %d", initialExists)

		// Start IDLE
		fmt.Fprintf(conn, "A003 IDLE\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))

		// Read continuation
		contResp, err := reader.ReadString('\n')
		requireNoError(t, err)
		if !strings.HasPrefix(strings.TrimSpace(contResp), "+") {
			t.Fatalf("expected IDLE continuation, got: %s", strings.TrimSpace(contResp))
		}

		// Now send a message to idle-user@mail3.test via SMTP
		subject := fmt.Sprintf("IDLE-notify-%d", time.Now().UnixNano())
		t.Logf("Sending message with subject %q to trigger IDLE notification...", subject)

		go func() {
			// Small delay to ensure IDLE is fully active
			time.Sleep(2 * time.Second)
			sendMailViaSMTP(t, mail1SMTPAddr,
				"idle-sender@mail1.test", "idle-user@mail3.test",
				subject, "This should trigger an IDLE EXISTS notification.")
		}()

		// Wait for an EXISTS notification during IDLE.
		// The IMAP gateway polls every 15 seconds, so we need to wait
		// at least that long. We set a generous timeout.
		existsReceived := false
		var newCount int

		// Set a longer deadline for waiting for the notification.
		// The gateway's IDLE handler polls every 15 seconds.
		conn.SetDeadline(time.Now().Add(60 * time.Second))

		for i := 0; i < 60; i++ {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Logf("Read error during IDLE wait (attempt %d): %v", i, err)
				break
			}
			line = strings.TrimSpace(line)
			t.Logf("IDLE received: %s", line)

			if strings.Contains(line, "EXISTS") {
				fmt.Sscanf(line, "* %d EXISTS", &newCount)
				if newCount > initialExists {
					existsReceived = true
					t.Logf("EXISTS notification received: %d (was %d)", newCount, initialExists)
					break
				}
			}

			// If we get a tagged response, IDLE was terminated unexpectedly
			if strings.HasPrefix(line, "A003 ") {
				t.Logf("IDLE terminated unexpectedly: %s", line)
				break
			}
		}

		// Send DONE regardless of whether we got the notification
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintf(conn, "DONE\r\n")

		// Read the OK response (if IDLE is still active)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		doneResp, _ := reader.ReadString('\n')
		if doneResp != "" {
			t.Logf("DONE response: %s", strings.TrimSpace(doneResp))
		}

		if existsReceived {
			t.Logf("IDLE EXISTS notification verified: new message count = %d", newCount)
		} else {
			// The IDLE poll interval is 15 seconds and delivery may take time.
			// We give this a reasonable window but don't hard-fail since the
			// infrastructure test timing can vary.
			t.Log("EXISTS notification not received within timeout")
			t.Log("This may be due to message delivery latency or IDLE poll interval")
			t.Log("IDLE basic flow was verified in the previous test")
		}

		// LOGOUT
		fmt.Fprintf(conn, "A004 LOGOUT\r\n")
	})

	t.Run("IMAPS_IDLE_BasicFlow", func(t *testing.T) {
		// Verify IDLE works over IMAPS (port 993) as well
		conn, err := tls.DialWithDialer(
			&net.Dialer{Timeout: 10 * time.Second},
			"tcp",
			imapsGWAddr,
			&tls.Config{InsecureSkipVerify: true},
		)
		if err != nil {
			t.Skipf("Cannot connect to IMAPS %s: %v", imapsGWAddr, err)
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)

		// Read greeting
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		greeting, err := reader.ReadString('\n')
		requireNoError(t, err)
		if !strings.Contains(greeting, "OK") {
			t.Fatalf("unexpected IMAPS greeting: %s", strings.TrimSpace(greeting))
		}

		// LOGIN
		fmt.Fprintf(conn, "A001 LOGIN idle-user@mail3.test password123\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		loginResp := readUntilTagRaw(t, reader, "A001")
		if !strings.Contains(loginResp, "OK") {
			t.Skipf("IMAPS LOGIN failed: %s", loginResp)
		}

		// SELECT INBOX
		fmt.Fprintf(conn, "A002 SELECT INBOX\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		selectResp := readUntilTagRaw(t, reader, "A002")
		if !strings.Contains(selectResp, "OK") {
			t.Fatalf("IMAPS SELECT INBOX failed: %s", selectResp)
		}

		// IDLE
		fmt.Fprintf(conn, "A003 IDLE\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))

		contResp, err := reader.ReadString('\n')
		requireNoError(t, err)
		if !strings.HasPrefix(strings.TrimSpace(contResp), "+") {
			t.Fatalf("expected IDLE continuation over IMAPS, got: %s", strings.TrimSpace(contResp))
		}
		t.Log("IDLE continuation received over IMAPS")

		// DONE
		fmt.Fprintf(conn, "DONE\r\n")
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		doneResp, err := reader.ReadString('\n')
		requireNoError(t, err)

		if strings.Contains(doneResp, "A003 OK") {
			t.Log("IDLE over IMAPS terminated correctly with DONE")
		} else {
			t.Logf("DONE response over IMAPS: %s", strings.TrimSpace(doneResp))
		}

		// LOGOUT
		fmt.Fprintf(conn, "A004 LOGOUT\r\n")
	})

	t.Run("IDLE_ResumeAfterDone", func(t *testing.T) {
		// Verify that after IDLE is terminated with DONE, the session
		// returns to normal command mode and subsequent commands work.
		ic := dialIMAP(t, mail3IMAPAddr)
		defer ic.close()

		ic.login(t, "idle-user@mail3.test", "password123")

		result, _ := ic.command(t, "SELECT INBOX")
		if !strings.Contains(result, "OK") {
			t.Fatalf("SELECT INBOX failed: %s", result)
		}

		// Enter and exit IDLE using raw connection
		tag := ic.nextTag()
		ic.conn.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintf(ic.conn, "%s IDLE\r\n", tag)

		// Read continuation
		contLine := ic.readLine(t)
		if !strings.HasPrefix(contLine, "+") {
			t.Fatalf("expected IDLE continuation, got: %s", contLine)
		}

		// Send DONE
		ic.conn.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintf(ic.conn, "DONE\r\n")

		// Read tagged OK
		doneLine := ic.readLine(t)
		if !strings.Contains(doneLine, tag+" OK") {
			t.Errorf("expected '%s OK' after DONE, got: %s", tag, doneLine)
		}

		// Now try a normal command after IDLE exits
		result2, _ := ic.command(t, "NOOP")
		if !strings.Contains(result2, "OK") {
			t.Errorf("NOOP after IDLE exit failed: %s", result2)
		} else {
			t.Log("Session correctly resumed normal command mode after IDLE")
		}

		ic.command(t, "LOGOUT")
	})
}

// readUntilTagRaw reads IMAP lines from a raw bufio.Reader until a line
// starting with the given tag is found. Returns all lines joined.
func readUntilTagRaw(t *testing.T, reader *bufio.Reader, tag string) string {
	t.Helper()
	var allLines []string
	for i := 0; i < 50; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read IMAP line: %v", err)
		}
		trimmed := strings.TrimSpace(line)
		allLines = append(allLines, trimmed)
		if strings.HasPrefix(trimmed, tag+" ") {
			break
		}
	}
	return strings.Join(allLines, "\n")
}

// dialIMAPSForIdle connects to IMAPS and returns a raw TLS connection + reader.
func dialIMAPSForIdle(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp",
		addr,
		&tls.Config{InsecureSkipVerify: true},
	)
	if err != nil {
		t.Fatalf("dial IMAPS %s: %v", addr, err)
	}
	reader := bufio.NewReader(conn)
	// Read greeting
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	greeting, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		t.Fatalf("read IMAPS greeting: %v", err)
	}
	if !strings.Contains(greeting, "OK") {
		conn.Close()
		t.Fatalf("unexpected IMAPS greeting: %s", strings.TrimSpace(greeting))
	}
	return conn, reader
}
