package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// foldICS folds an iCalendar body at 75 octets, as RFC 5545 section 3.1 says a sender does, so a long
// property is a real one and no line is longer than SMTP carries.
func foldICS(ics string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(ics, "\r\n") {
		for len(line) > 75 {
			b.WriteString(line[:75] + "\r\n ")
			line = line[75:]
		}
		b.WriteString(line + "\r\n")
	}
	return strings.TrimSuffix(b.String(), "\r\n")
}

// A calendar's properties are the sender's to write at any length and in any octets, and an invite's
// version row is stored in the message's transaction. A property its column can't hold must not
// refuse the mail, on any path: the row holds what fits and the source what arrived.
func TestCalendarFieldsDoNotRefuseMail(t *testing.T) {
	f, routes := senderRoutes(t)
	longUID := strings.Repeat("u", 600) + "@remote.test"
	longAddress := strings.Repeat("o", 300) + "@remote.test"
	event := func(uid, method, status, summary, organizer string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:" + method + "\r\nBEGIN:VEVENT\r\nUID:" + uid +
			"\r\nSEQUENCE:0\r\nDTSTART:20261101T100000Z\r\nDTEND:20261101T110000Z\r\nSUMMARY:" + summary +
			"\r\nSTATUS:" + status + "\r\nORGANIZER:mailto:" + organizer + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	const uid, organizer = "plan@remote.test", "organizer@remote.test"
	for _, tc := range []struct {
		name, ics string
		base64    bool
		wantUID   string // what the event in the message answers
	}{
		{name: "an ordinary invite", ics: event(uid, "REQUEST", "CONFIRMED", "Plan", organizer), wantUID: uid},
		{name: "a long UID", ics: foldICS(event(longUID, "REQUEST", "CONFIRMED", "Plan", organizer)), wantUID: longUID},
		{name: "a long organizer", ics: foldICS(event(uid, "REQUEST", "CONFIRMED", "Plan", longAddress)), wantUID: uid},
		{name: "a long method", ics: foldICS(event(uid, strings.Repeat("M", 40), "CONFIRMED", "Plan", organizer)), wantUID: uid},
		{name: "a long status", ics: foldICS(event(uid, "REQUEST", strings.Repeat("S", 40), "Plan", organizer)), wantUID: uid},
		{name: "a NUL in the summary", ics: event(uid, "REQUEST", "CONFIRMED", "a\x00b", organizer), base64: true, wantUID: uid},
		{name: "a NUL in the UID", ics: event("a\x00b", "REQUEST", "CONFIRMED", "Plan", organizer), base64: true, wantUID: "a�b"},
		{name: "invalid UTF-8 in the summary", ics: event(uid, "REQUEST", "CONFIRMED", "a\xffb", organizer), base64: true, wantUID: uid},
		{name: "invalid UTF-8 in the organizer", ics: event(uid, "REQUEST", "CONFIRMED", "Plan", "a\xffb@remote.test"), base64: true, wantUID: uid},
	} {
		for _, route := range routes {
			t.Run(tc.name+"/"+route.name, func(t *testing.T) {
				part := "Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n" + tc.ics
				if tc.base64 {
					part = "Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
						base64.StdEncoding.EncodeToString([]byte(tc.ics)) + "\r\n"
				}
				message := fmt.Sprintf("From: Old <old@remote.test>\r\nTo: chris@example.test\r\nSubject: Plan\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMessage-ID: <%x@remote.test>\r\n", tc.name+route.name) +
					"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nJoin us.\r\n--B\r\n" + part + "--B--\r\n"
				if err := route.send(t, message); err != nil {
					t.Fatalf("refused: %v", err)
				}
				stored := lastStored(t, f, message)
				// RESTMAIL stores the source and reads no calendar from it.
				if route.name == "RESTMAIL" {
					return
				}
				var events []pipeline.CalendarEvent
				if err := json.Unmarshal(stored.CalendarEventsRaw, &events); err != nil || len(events) != 1 {
					t.Fatalf("calendar events %.200s (%v)", stored.CalendarEventsRaw, err)
				}
				// A reply names the event as its organizer wrote it, whatever the version row holds.
				if events[0].UID != tc.wantUID {
					t.Fatalf("event UID %.40q, want %.40q", events[0].UID, tc.wantUID)
				}
				var versions []models.CalendarEventVersion
				if err := f.db.Where("message_id = ?", stored.ID).Find(&versions).Error; err != nil {
					t.Fatal(err)
				}
				if len(versions) != 1 {
					t.Fatalf("%d version rows", len(versions))
				}
			})
		}
	}
}
