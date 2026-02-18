package mime

import (
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

const sampleVEVENT = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
METHOD:REQUEST
BEGIN:VEVENT
UID:event-123@example.com
DTSTART:20240315T140000Z
DTEND:20240315T150000Z
DTSTAMP:20240301T120000Z
SUMMARY:Team Standup
DESCRIPTION:Daily team standup meeting.\nBring your updates.
LOCATION:Conference Room B
ORGANIZER;CN=Alice Smith:mailto:alice@example.com
ATTENDEE;CN=Bob Jones;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.com
ATTENDEE;CN=Carol White;ROLE=OPT-PARTICIPANT;PARTSTAT=ACCEPTED:mailto:carol@example.com
STATUS:CONFIRMED
SEQUENCE:0
END:VEVENT
END:VCALENDAR`

func TestParseCalendar_AllProperties(t *testing.T) {
	events, err := ParseCalendar(sampleVEVENT)
	if err != nil {
		t.Fatalf("ParseCalendar returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]

	// Method
	if ev.Method != "REQUEST" {
		t.Errorf("Method = %q, want %q", ev.Method, "REQUEST")
	}

	// UID
	if ev.UID != "event-123@example.com" {
		t.Errorf("UID = %q, want %q", ev.UID, "event-123@example.com")
	}

	// Summary
	if ev.Summary != "Team Standup" {
		t.Errorf("Summary = %q, want %q", ev.Summary, "Team Standup")
	}

	// Description (should have newline unescaped)
	if !strings.Contains(ev.Description, "Daily team standup meeting.") {
		t.Errorf("Description = %q, expected to contain 'Daily team standup meeting.'", ev.Description)
	}
	if !strings.Contains(ev.Description, "\n") {
		t.Error("Description should contain an unescaped newline")
	}

	// Location
	if ev.Location != "Conference Room B" {
		t.Errorf("Location = %q, want %q", ev.Location, "Conference Room B")
	}

	// DTStart
	expectedStart := time.Date(2024, 3, 15, 14, 0, 0, 0, time.UTC)
	if !ev.DTStart.Equal(expectedStart) {
		t.Errorf("DTStart = %v, want %v", ev.DTStart, expectedStart)
	}

	// DTEnd
	expectedEnd := time.Date(2024, 3, 15, 15, 0, 0, 0, time.UTC)
	if !ev.DTEnd.Equal(expectedEnd) {
		t.Errorf("DTEnd = %v, want %v", ev.DTEnd, expectedEnd)
	}

	// AllDay
	if ev.AllDay {
		t.Error("AllDay should be false for a timed event")
	}

	// Organizer
	if ev.Organizer.Address != "alice@example.com" {
		t.Errorf("Organizer.Address = %q, want %q", ev.Organizer.Address, "alice@example.com")
	}
	if ev.Organizer.Name != "Alice Smith" {
		t.Errorf("Organizer.Name = %q, want %q", ev.Organizer.Name, "Alice Smith")
	}

	// Attendees
	if len(ev.Attendees) != 2 {
		t.Fatalf("expected 2 attendees, got %d", len(ev.Attendees))
	}

	bob := ev.Attendees[0]
	if bob.Address != "bob@example.com" {
		t.Errorf("Attendee[0].Address = %q, want %q", bob.Address, "bob@example.com")
	}
	if bob.Name != "Bob Jones" {
		t.Errorf("Attendee[0].Name = %q, want %q", bob.Name, "Bob Jones")
	}
	if bob.Role != "REQ-PARTICIPANT" {
		t.Errorf("Attendee[0].Role = %q, want %q", bob.Role, "REQ-PARTICIPANT")
	}
	if bob.PartStat != "NEEDS-ACTION" {
		t.Errorf("Attendee[0].PartStat = %q, want %q", bob.PartStat, "NEEDS-ACTION")
	}
	if !bob.RSVP {
		t.Error("Attendee[0].RSVP should be true")
	}

	carol := ev.Attendees[1]
	if carol.Address != "carol@example.com" {
		t.Errorf("Attendee[1].Address = %q, want %q", carol.Address, "carol@example.com")
	}
	if carol.PartStat != "ACCEPTED" {
		t.Errorf("Attendee[1].PartStat = %q, want %q", carol.PartStat, "ACCEPTED")
	}

	// Status
	if ev.Status != "CONFIRMED" {
		t.Errorf("Status = %q, want %q", ev.Status, "CONFIRMED")
	}

	// Sequence
	if ev.Sequence != 0 {
		t.Errorf("Sequence = %d, want %d", ev.Sequence, 0)
	}
}

func TestParseCalendar_MethodRequest(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
METHOD:REQUEST
BEGIN:VEVENT
UID:meeting-456@corp.com
DTSTART:20240601T100000Z
DTEND:20240601T110000Z
SUMMARY:Quarterly Review
ORGANIZER;CN=Manager:mailto:manager@corp.com
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:employee@corp.com
END:VEVENT
END:VCALENDAR`

	events, err := ParseCalendar(ics)
	if err != nil {
		t.Fatalf("ParseCalendar returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].Method != "REQUEST" {
		t.Errorf("Method = %q, want %q", events[0].Method, "REQUEST")
	}
	if events[0].Summary != "Quarterly Review" {
		t.Errorf("Summary = %q, want %q", events[0].Summary, "Quarterly Review")
	}
	if events[0].Organizer.Address != "manager@corp.com" {
		t.Errorf("Organizer.Address = %q, want %q", events[0].Organizer.Address, "manager@corp.com")
	}
}

func TestParseCalendar_Timezone(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Test//Test//EN
METHOD:REQUEST
BEGIN:VTIMEZONE
TZID:America/New_York
BEGIN:STANDARD
DTSTART:19701101T020000
RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU
TZOFFSETFROM:-0400
TZOFFSETTO:-0500
TZNAME:EST
END:STANDARD
BEGIN:DAYLIGHT
DTSTART:19700308T020000
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU
TZOFFSETFROM:-0500
TZOFFSETTO:-0400
TZNAME:EDT
END:DAYLIGHT
END:VTIMEZONE
BEGIN:VEVENT
UID:tz-event-789@example.com
DTSTART;TZID=America/New_York:20240115T093000
DTEND;TZID=America/New_York:20240115T103000
SUMMARY:Morning Meeting
ORGANIZER:mailto:boss@example.com
END:VEVENT
END:VCALENDAR`

	events, err := ParseCalendar(ics)
	if err != nil {
		t.Fatalf("ParseCalendar returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]

	if ev.Summary != "Morning Meeting" {
		t.Errorf("Summary = %q, want %q", ev.Summary, "Morning Meeting")
	}

	// The DTStart should have a timezone applied.
	// The VTIMEZONE specifies STANDARD TZOFFSETTO=-0500
	// 09:30 EST = 14:30 UTC
	utcStart := ev.DTStart.UTC()
	expectedHour := 14
	expectedMinute := 30
	if utcStart.Hour() != expectedHour || utcStart.Minute() != expectedMinute {
		t.Errorf("DTStart UTC = %v, expected hour=%d minute=%d", utcStart, expectedHour, expectedMinute)
	}
}

func TestParseCalendar_CancelEvent(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
METHOD:CANCEL
BEGIN:VEVENT
UID:cancelled-event@example.com
DTSTART:20240401T090000Z
DTEND:20240401T100000Z
SUMMARY:Cancelled Meeting
STATUS:CANCELLED
SEQUENCE:2
ORGANIZER;CN=Org:mailto:org@example.com
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:user@example.com
END:VEVENT
END:VCALENDAR`

	events, err := ParseCalendar(ics)
	if err != nil {
		t.Fatalf("ParseCalendar returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]

	if ev.Method != "CANCEL" {
		t.Errorf("Method = %q, want %q", ev.Method, "CANCEL")
	}
	if ev.Status != "CANCELLED" {
		t.Errorf("Status = %q, want %q", ev.Status, "CANCELLED")
	}
	if ev.Sequence != 2 {
		t.Errorf("Sequence = %d, want %d", ev.Sequence, 2)
	}
	if ev.UID != "cancelled-event@example.com" {
		t.Errorf("UID = %q, want %q", ev.UID, "cancelled-event@example.com")
	}
}

func TestParseCalendar_UpdateEvent(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
METHOD:REQUEST
BEGIN:VEVENT
UID:update-event@example.com
DTSTART:20240501T130000Z
DTEND:20240501T140000Z
SUMMARY:Updated Meeting (moved to 1 PM)
STATUS:CONFIRMED
SEQUENCE:3
ORGANIZER:mailto:org@example.com
ATTENDEE:mailto:user@example.com
END:VEVENT
END:VCALENDAR`

	events, err := ParseCalendar(ics)
	if err != nil {
		t.Fatalf("ParseCalendar returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]

	if ev.Method != "REQUEST" {
		t.Errorf("Method = %q, want %q", ev.Method, "REQUEST")
	}
	if ev.Sequence != 3 {
		t.Errorf("Sequence = %d, want %d", ev.Sequence, 3)
	}
	if ev.Summary != "Updated Meeting (moved to 1 PM)" {
		t.Errorf("Summary = %q, want %q", ev.Summary, "Updated Meeting (moved to 1 PM)")
	}
}

func TestParseCalendar_AllDayEvent(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
METHOD:REQUEST
BEGIN:VEVENT
UID:allday-123@example.com
DTSTART;VALUE=DATE:20240320
DTEND;VALUE=DATE:20240321
SUMMARY:Company Holiday
END:VEVENT
END:VCALENDAR`

	events, err := ParseCalendar(ics)
	if err != nil {
		t.Fatalf("ParseCalendar returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]
	if !ev.AllDay {
		t.Error("AllDay should be true for a date-only event")
	}
	if ev.DTStart.Year() != 2024 || ev.DTStart.Month() != 3 || ev.DTStart.Day() != 20 {
		t.Errorf("DTStart = %v, expected 2024-03-20", ev.DTStart)
	}
}

func TestParseCalendar_LineFolding(t *testing.T) {
	// RFC 5545 allows long lines to be folded with CRLF + space
	ics := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"METHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:fold-test@example.com\r\n" +
		"DTSTART:20240601T100000Z\r\n" +
		"DTEND:20240601T110000Z\r\n" +
		"SUMMARY:This is a very long sum\r\n" +
		" mary that has been folded acros\r\n" +
		" s multiple lines\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	events, err := ParseCalendar(ics)
	if err != nil {
		t.Fatalf("ParseCalendar returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	expected := "This is a very long summary that has been folded across multiple lines"
	if events[0].Summary != expected {
		t.Errorf("Summary = %q, want %q", events[0].Summary, expected)
	}
}

func TestBuildCalendarReply(t *testing.T) {
	event := pipeline.CalendarEvent{
		UID:     "event-123@example.com",
		Summary: "Team Standup",
		DTStart: time.Date(2024, 3, 15, 14, 0, 0, 0, time.UTC),
		DTEnd:   time.Date(2024, 3, 15, 15, 0, 0, 0, time.UTC),
		Organizer: pipeline.CalendarAddress{
			Address: "alice@example.com",
			Name:    "Alice Smith",
		},
	}

	reply := BuildCalendarReply(event, "bob@example.com", "ACCEPTED")

	if !strings.Contains(reply, "METHOD:REPLY") {
		t.Error("reply should contain METHOD:REPLY")
	}
	if !strings.Contains(reply, "UID:event-123@example.com") {
		t.Error("reply should contain the original UID")
	}
	if !strings.Contains(reply, "PARTSTAT=ACCEPTED") {
		t.Error("reply should contain PARTSTAT=ACCEPTED")
	}
	if !strings.Contains(reply, "mailto:bob@example.com") {
		t.Error("reply should contain attendee email")
	}
	if !strings.Contains(reply, "ORGANIZER;CN=Alice Smith:mailto:alice@example.com") {
		t.Error("reply should contain organizer")
	}
	if !strings.Contains(reply, "SUMMARY:Team Standup") {
		t.Error("reply should contain event summary")
	}
}

func TestBuildCalendarReply_Declined(t *testing.T) {
	event := pipeline.CalendarEvent{
		UID:       "decline-test@example.com",
		Summary:   "Optional Meeting",
		Organizer: pipeline.CalendarAddress{Address: "org@example.com"},
	}

	reply := BuildCalendarReply(event, "user@example.com", "DECLINED")

	if !strings.Contains(reply, "PARTSTAT=DECLINED") {
		t.Error("reply should contain PARTSTAT=DECLINED")
	}
}

func TestParseCalendar_ReplyMethod(t *testing.T) {
	ics := `BEGIN:VCALENDAR
VERSION:2.0
METHOD:REPLY
BEGIN:VEVENT
UID:reply-test@example.com
DTSTART:20240601T100000Z
DTEND:20240601T110000Z
SUMMARY:Test Event
ORGANIZER:mailto:org@example.com
ATTENDEE;PARTSTAT=ACCEPTED:mailto:user@example.com
SEQUENCE:0
END:VEVENT
END:VCALENDAR`

	events, err := ParseCalendar(ics)
	if err != nil {
		t.Fatalf("ParseCalendar returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].Method != "REPLY" {
		t.Errorf("Method = %q, want %q", events[0].Method, "REPLY")
	}
	if events[0].Attendees[0].PartStat != "ACCEPTED" {
		t.Errorf("Attendee PartStat = %q, want %q", events[0].Attendees[0].PartStat, "ACCEPTED")
	}
}
