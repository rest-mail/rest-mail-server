package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// imapZoneSession is one IMAP connection to the real gateway, on the real API,
// on PostgreSQL, signed in with an app password as a mail client is.
type imapZoneSession struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
	tag    int
}

func openIMAPZoneSession(t *testing.T, f *credentialFixture, api *apiclient.Client, password string) *imapZoneSession {
	t.Helper()
	if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
		t.Fatalf("seed quota usage: %v", err)
	}
	// The fixture seeds its mailbox directly, and APPEND needs the import grant a
	// created account holds by default.
	mailbox := models.Mailbox{ID: f.mailboxID, Address: f.address}
	if err := grants.EnsureImport(f.db, &mailbox); err != nil {
		t.Fatalf("seed import grant: %v", err)
	}
	client, server := net.Pipe()
	session := imapserver.NewSession(server, imapgw.NewBackend(api, nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	s := &imapZoneSession{t: t, conn: client, reader: bufio.NewReader(client)}
	if greeting := s.line(); !strings.HasPrefix(greeting, "* OK") {
		t.Fatalf("greeting: %q", greeting)
	}
	if _, status := s.command(fmt.Sprintf("LOGIN %q %q", f.address, password)); !strings.Contains(status, " OK") {
		t.Fatalf("LOGIN: %q", status)
	}
	return s
}

func (s *imapZoneSession) line() string {
	s.t.Helper()
	_ = s.conn.SetDeadline(time.Now().Add(30 * time.Second))
	line, err := s.reader.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

func (s *imapZoneSession) write(text string) {
	s.t.Helper()
	_ = s.conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := s.conn.Write([]byte(text)); err != nil {
		s.t.Fatalf("write: %v", err)
	}
}

func (s *imapZoneSession) nextTag() string {
	s.tag++
	return fmt.Sprintf("z%d", s.tag)
}

func (s *imapZoneSession) responses(tag string) (untagged []string, status string) {
	s.t.Helper()
	for {
		line := s.line()
		if strings.HasPrefix(line, tag+" ") {
			return untagged, line
		}
		untagged = append(untagged, line)
	}
}

func (s *imapZoneSession) command(command string) (untagged []string, status string) {
	s.t.Helper()
	tag := s.nextTag()
	s.write(tag + " " + command + "\r\n")
	return s.responses(tag)
}

// appendAt stores a message through IMAP APPEND with the instant as its date-time.
func (s *imapZoneSession) appendAt(instant time.Time, dateHeader string) {
	s.t.Helper()
	message := fmt.Sprintf("Date: %s\r\nFrom: a@example.test\r\nTo: chris@example.test\r\nSubject: zone\r\nMessage-ID: <%d@example.test>\r\n\r\nbody\r\n", dateHeader, instant.UnixNano())
	tag := s.nextTag()
	s.write(fmt.Sprintf("%s APPEND INBOX \"%s\" {%d}\r\n", tag, instant.UTC().Format("02-Jan-2006 15:04:05 -0700"), len(message)))
	if line := s.line(); !strings.HasPrefix(line, "+") {
		s.t.Fatalf("APPEND continuation: %q", line)
	}
	s.write(message + "\r\n")
	if _, status := s.responses(tag); !strings.Contains(status, " OK") {
		s.t.Fatalf("APPEND %s: %q", instant, status)
	}
}

func (s *imapZoneSession) requireSearch(criteria, want string) {
	s.t.Helper()
	untagged, status := s.command("SEARCH " + criteria)
	if !strings.Contains(status, " OK") {
		s.t.Errorf("SEARCH %s: %q", criteria, status)
		return
	}
	if len(untagged) != 1 || strings.TrimSpace(untagged[0]) != strings.TrimSpace("* SEARCH "+want) {
		s.t.Errorf("SEARCH %s = %q, want %q", criteria, untagged, "* SEARCH "+want)
	}
}

func setIMAPZoneFixtureZone(t *testing.T, f *credentialFixture, body string, wantCode int) {
	t.Helper()
	rr := f.do(http.MethodPut, fmt.Sprintf("/api/v1/accounts/%d/time-zone", f.mailboxID), f.session, body)
	if rr.Code != wantCode {
		t.Fatalf("set zone %s: %d %s, want %d", body, rr.Code, rr.Body.String(), wantCode)
	}
}

func utcInstant(t *testing.T, value string) time.Time {
	t.Helper()
	instant, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return instant
}

// TestIMAPSearchUsesTheAccountZone is the owner's decision on #1557: SEARCH ON,
// BEFORE and SINCE take the client's date on the account's calendar, and UTC when
// the account has no zone. It runs a real session through the gateway and the API.
func TestIMAPSearchUsesTheAccountZone(t *testing.T) {
	gateway := newGatewayFixture(t)
	f := gateway.credentialFixture
	api := gatewayAPI(t, gateway.router, gateway.internal, imapCN)
	session := openIMAPZoneSession(t, f, api, gateway.app.Password)

	// Sequence numbers follow the order of these APPENDs.
	for _, message := range []struct{ instant, date string }{
		// New York moves to daylight time on 8 March 2026 at 07:00 UTC, a 23-hour day.
		{"2026-03-08T04:30:00Z", "Sat, 7 Mar 2026 23:30:00 -0500"}, // 1: 7 March there, 8 March UTC
		{"2026-03-08T05:00:00Z", "Sun, 8 Mar 2026 00:00:00 -0500"}, // 2: the first second of its 8 March
		{"2026-03-09T03:59:59Z", "Sun, 8 Mar 2026 23:59:59 -0400"}, // 3: the last second of its 8 March, 9 March UTC
		{"2026-03-09T04:00:00Z", "Mon, 9 Mar 2026 00:00:00 -0400"}, // 4: the first second of its 9 March
		// It moves back on 1 November 2026 at 06:00 UTC, a 25-hour day.
		{"2026-11-01T03:59:59Z", "Sat, 31 Oct 2026 23:59:59 -0400"}, // 5: the last second of its 31 October
		{"2026-11-01T04:00:00Z", "Sun, 1 Nov 2026 00:00:00 -0400"},  // 6: the first second of its 1 November
		{"2026-11-02T04:59:59Z", "Sun, 1 Nov 2026 23:59:59 -0500"},  // 7: the last second of its 1 November, 2 November UTC
		{"2026-11-02T05:00:00Z", "Mon, 2 Nov 2026 00:00:00 -0500"},  // 8: the first second of its 2 November
	} {
		session.appendAt(utcInstant(t, message.instant), message.date)
	}
	if _, status := session.command("SELECT INBOX"); !strings.Contains(status, " OK") {
		t.Fatalf("SELECT: %q", status)
	}

	// The sent date is as the Date: header writes it, in every zone (RFC 3501 §6.4.4).
	sentKeys := func() {
		t.Helper()
		session.requireSearch("SENTON 7-Mar-2026", "1")
		session.requireSearch("SENTON 8-Mar-2026", "2 3")
		session.requireSearch("SENTBEFORE 8-Mar-2026", "1")
		session.requireSearch("SENTSINCE 2-Nov-2026", "8")
	}
	// INTERNALDATE is rendered in UTC in every zone: only SEARCH's days change.
	internalDateInUTC := func() {
		t.Helper()
		untagged, status := session.command("FETCH 1 INTERNALDATE")
		if !strings.Contains(status, " OK") || len(untagged) != 1 || !strings.Contains(untagged[0], `INTERNALDATE "08-Mar-2026 04:30:00 +0000"`) {
			t.Errorf("FETCH INTERNALDATE = %q / %q, want it in UTC", untagged, status)
		}
	}

	t.Run("no zone is UTC", func(t *testing.T) {
		session.t = t
		session.requireSearch("ON 7-Mar-2026", "")
		session.requireSearch("ON 8-Mar-2026", "1 2")
		session.requireSearch("ON 9-Mar-2026", "3 4")
		session.requireSearch("BEFORE 9-Mar-2026", "1 2")
		session.requireSearch("SINCE 2-Nov-2026", "7 8")
		sentKeys()
		internalDateInUTC()
	})

	setIMAPZoneFixtureZone(t, f, `{"time_zone":"America/New_York"}`, http.StatusOK)
	t.Run("west of UTC across both changes", func(t *testing.T) {
		session.t = t
		session.requireSearch("ON 7-Mar-2026", "1")
		session.requireSearch("ON 8-Mar-2026", "2 3")
		session.requireSearch("ON 9-Mar-2026", "4")
		session.requireSearch("BEFORE 8-Mar-2026", "1")
		session.requireSearch("BEFORE 9-Mar-2026", "1 2 3")
		session.requireSearch("SINCE 9-Mar-2026", "4 5 6 7 8")
		session.requireSearch("ON 31-Oct-2026", "5")
		session.requireSearch("ON 1-Nov-2026", "6 7")
		session.requireSearch("ON 2-Nov-2026", "8")
		session.requireSearch("SINCE 1-Nov-2026 BEFORE 2-Nov-2026", "6 7")
		session.requireSearch("NOT ON 8-Mar-2026 BEFORE 9-Mar-2026", "1")
		session.requireSearch("OR ON 7-Mar-2026 ON 9-Mar-2026", "1 4")
		session.requireSearch("(ON 2-Nov-2026)", "8")
		sentKeys()
		internalDateInUTC()
	})

	setIMAPZoneFixtureZone(t, f, `{"time_zone":"Asia/Tokyo"}`, http.StatusOK)
	t.Run("east of UTC", func(t *testing.T) {
		session.t = t
		// Tokyo is nine hours ahead: 15:00 UTC is already the next day.
		session.requireSearch("ON 8-Mar-2026", "1 2")
		session.requireSearch("ON 9-Mar-2026", "3 4")
		session.requireSearch("ON 1-Nov-2026", "5 6")
		session.requireSearch("ON 2-Nov-2026", "7 8")
		sentKeys()
		internalDateInUTC()
	})

	setIMAPZoneFixtureZone(t, f, `{"time_zone":"Mars/Olympus"}`, http.StatusUnprocessableEntity)
	t.Run("a refused name changes nothing", func(t *testing.T) {
		session.t = t
		session.requireSearch("ON 1-Nov-2026", "5 6")
	})

	setIMAPZoneFixtureZone(t, f, `{"time_zone":null}`, http.StatusOK)
	t.Run("cleared on an open connection is UTC again", func(t *testing.T) {
		session.t = t
		session.requireSearch("ON 8-Mar-2026", "1 2")
		session.requireSearch("ON 2-Nov-2026", "7 8")
	})
}

// TestIMAPSearchOnADayWithoutMidnight holds the day to its real first second where
// the clock skips midnight: Havana goes from 00:00 to 01:00 on 9 March 2025, and
// back from 01:00 to 00:00 on 2 November 2025, so that day has two midnights.
func TestIMAPSearchOnADayWithoutMidnight(t *testing.T) {
	gateway := newGatewayFixture(t)
	f := gateway.credentialFixture
	api := gatewayAPI(t, gateway.router, gateway.internal, imapCN)
	session := openIMAPZoneSession(t, f, api, gateway.app.Password)
	for _, instant := range []string{
		"2025-03-09T04:59:59Z", // 1: 23:59:59 on 8 March
		"2025-03-09T05:00:00Z", // 2: 01:00:00 on 9 March, its first second
		"2025-11-02T03:59:59Z", // 3: 23:59:59 on 1 November
		"2025-11-02T04:00:00Z", // 4: the first 00:00:00 on 2 November
		"2025-11-02T05:00:00Z", // 5: the second 00:00:00 on 2 November
		"2025-11-03T04:59:59Z", // 6: 23:59:59 on 2 November
		"2025-11-03T05:00:00Z", // 7: 00:00:00 on 3 November
	} {
		session.appendAt(utcInstant(t, instant), "Mon, 1 Jan 2024 00:00:00 +0000")
	}
	setIMAPZoneFixtureZone(t, f, `{"time_zone":"America/Havana"}`, http.StatusOK)
	if _, status := session.command("SELECT INBOX"); !strings.Contains(status, " OK") {
		t.Fatalf("SELECT: %q", status)
	}
	session.requireSearch("ON 8-Mar-2025", "1")
	session.requireSearch("ON 9-Mar-2025", "2")
	session.requireSearch("BEFORE 9-Mar-2025", "1")
	session.requireSearch("ON 1-Nov-2025", "3")
	session.requireSearch("ON 2-Nov-2025", "4 5 6")
	session.requireSearch("SINCE 3-Nov-2025", "7")
}
