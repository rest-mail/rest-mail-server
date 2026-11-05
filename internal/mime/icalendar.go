package mime

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// ParseCalendar parses an iCalendar (text/calendar) body and extracts calendar events.
// It handles line unfolding, VTIMEZONE blocks, and multiple VEVENT sections.
func ParseCalendar(data string) ([]pipeline.CalendarEvent, error) {
	// Events are stored as JSON and in text columns, which hold neither a NUL nor invalid UTF-8, and
	// refusing the row refuses the mail. The source keeps what arrived (#1915).
	data = strings.ToValidUTF8(strings.ReplaceAll(data, "\x00", "\uFFFD"), "\uFFFD")

	// Unfold lines: RFC 5545 says lines can be folded by inserting CRLF + whitespace.
	data = unfoldLines(data)

	lines := splitLines(data)

	var events []pipeline.CalendarEvent
	method := ""
	timezones := make(map[string]*time.Location) // TZID -> Location

	// First pass: extract METHOD and VTIMEZONE definitions
	for _, line := range lines {
		key, value := parseLine(line)
		if key == "METHOD" {
			method = strings.ToUpper(value)
		}
	}

	// Parse VTIMEZONE blocks
	inTZ := false
	var tzID string
	var stdOffset, dstOffset int
	hasStd, hasDst := false, false
	inStandard, inDaylight := false, false
	for _, line := range lines {
		key, value := parseLine(line)

		switch {
		case key == "BEGIN" && value == "VTIMEZONE":
			inTZ = true
			tzID = ""
			stdOffset = 0
			dstOffset = 0
			hasStd = false
			hasDst = false
			inStandard = false
			inDaylight = false
		case key == "END" && value == "VTIMEZONE":
			if tzID != "" {
				offset := stdOffset
				if hasDst && !hasStd {
					offset = dstOffset
				}
				timezones[tzID] = time.FixedZone(tzID, offset)
			}
			inTZ = false
		case inTZ && key == "TZID":
			tzID = value
		case inTZ && key == "BEGIN" && value == "STANDARD":
			inStandard = true
			hasStd = true
		case inTZ && key == "END" && value == "STANDARD":
			inStandard = false
		case inTZ && key == "BEGIN" && value == "DAYLIGHT":
			inDaylight = true
			hasDst = true
		case inTZ && key == "END" && value == "DAYLIGHT":
			inDaylight = false
		case inTZ && key == "TZOFFSETTO":
			offset := parseUTCOffset(value)
			if inStandard {
				stdOffset = offset
			} else if inDaylight {
				dstOffset = offset
			}
		}
	}

	// Second pass: extract VEVENT blocks
	inEvent := false
	var current map[string]string
	var attendees []pipeline.CalendarAddress
	for _, line := range lines {
		key, value := parseLine(line)

		switch {
		case key == "BEGIN" && value == "VEVENT":
			inEvent = true
			current = make(map[string]string)
			attendees = nil
		case key == "END" && value == "VEVENT":
			if current != nil {
				ev := buildEvent(current, attendees, method, timezones)
				events = append(events, ev)
			}
			inEvent = false
			current = nil
		case inEvent:
			if strings.HasPrefix(key, "ATTENDEE") {
				attendees = append(attendees, parseAttendee(key, value))
			} else if strings.HasPrefix(key, "ORGANIZER") {
				// Store both the params (in key) and the value
				current["ORGANIZER_RAW_KEY"] = key
				current["ORGANIZER"] = value
			} else {
				// Store the full key (with params) for DTSTART/DTEND
				baseKey := extractBaseKey(key)
				if baseKey == "DTSTART" || baseKey == "DTEND" || baseKey == "DTSTAMP" {
					current[baseKey+"_PARAMS"] = key
				}
				current[baseKey] = value
			}
		}
	}

	return events, nil
}

// unfoldLines removes line continuations per RFC 5545 section 3.1.
// A long line may be split by inserting a CRLF followed by a single whitespace character.
func unfoldLines(data string) string {
	data = strings.ReplaceAll(data, "\r\n ", "")
	data = strings.ReplaceAll(data, "\r\n\t", "")
	data = strings.ReplaceAll(data, "\n ", "")
	data = strings.ReplaceAll(data, "\n\t", "")
	return data
}

// splitLines splits by either CRLF or LF.
func splitLines(data string) []string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	return strings.Split(data, "\n")
}

// parseLine splits "KEY;PARAMS:VALUE" into key (including params) and value.
func parseLine(line string) (string, string) {
	line = strings.TrimSpace(line)
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	return line[:idx], line[idx+1:]
}

// extractBaseKey returns just the property name from "DTSTART;TZID=America/New_York".
func extractBaseKey(key string) string {
	if idx := strings.IndexByte(key, ';'); idx >= 0 {
		return key[:idx]
	}
	return key
}

// parseAttendee extracts attendee information from an ATTENDEE line.
// Example: ATTENDEE;CN=Bob;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.com
func parseAttendee(key, value string) pipeline.CalendarAddress {
	addr := pipeline.CalendarAddress{
		Address: stripMailto(value),
	}

	params := parseParams(key)
	addr.Name = params["CN"]
	addr.Role = params["ROLE"]
	addr.PartStat = params["PARTSTAT"]
	if strings.EqualFold(params["RSVP"], "TRUE") {
		addr.RSVP = true
	}

	return addr
}

// parseOrganizer extracts organizer information from ORGANIZER line.
func parseOrganizer(rawKey, value string) pipeline.CalendarAddress {
	addr := pipeline.CalendarAddress{
		Address: stripMailto(value),
	}
	params := parseParams(rawKey)
	addr.Name = params["CN"]
	return addr
}

// parseParams extracts parameters from a property key like "ATTENDEE;CN=Bob;ROLE=REQ-PARTICIPANT".
func parseParams(key string) map[string]string {
	params := make(map[string]string)
	parts := strings.Split(key, ";")
	for _, part := range parts[1:] { // skip the property name
		eqIdx := strings.IndexByte(part, '=')
		if eqIdx < 0 {
			continue
		}
		pKey := strings.ToUpper(part[:eqIdx])
		pVal := part[eqIdx+1:]
		// Remove quotes if present
		pVal = strings.Trim(pVal, "\"")
		params[pKey] = pVal
	}
	return params
}

// stripMailto removes the "mailto:" prefix from email addresses.
func stripMailto(value string) string {
	if strings.HasPrefix(strings.ToLower(value), "mailto:") {
		return value[7:]
	}
	return value
}

// parseUTCOffset parses UTC offset strings like "+0530" or "-0800" into seconds.
func parseUTCOffset(s string) int {
	s = strings.TrimSpace(s)
	if len(s) < 5 {
		return 0
	}
	sign := 1
	if s[0] == '-' {
		sign = -1
	}
	s = s[1:]
	hours := atoiCal(s[:2])
	minutes := atoiCal(s[2:4])
	return sign * (hours*3600 + minutes*60)
}

// atoiCal is a simple string-to-int for small positive numbers.
func atoiCal(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// parseDateTime parses an iCalendar date/time value.
// Formats supported:
//   - 20240115T093000Z (UTC)
//   - 20240115T093000 (floating or with TZID parameter)
//   - 20240115 (all-day, date only)
func parseDateTime(value string, paramKey string, timezones map[string]*time.Location) (time.Time, bool) {
	value = strings.TrimSpace(value)

	// Date-only format: YYYYMMDD (all-day event)
	if len(value) == 8 && !strings.Contains(value, "T") {
		t, err := time.Parse("20060102", value)
		if err != nil {
			return time.Time{}, true
		}
		return t, true
	}

	// Check for timezone parameter
	var loc *time.Location
	if paramKey != "" {
		params := parseParams(paramKey)
		if tzid, ok := params["TZID"]; ok {
			// First check our parsed VTIMEZONE map
			if tz, ok := timezones[tzid]; ok {
				loc = tz
			} else {
				// Try Go's timezone database
				if tz, err := time.LoadLocation(tzid); err == nil {
					loc = tz
				}
			}
		}
	}

	// UTC format: ends with Z
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse("20060102T150405Z", value)
		if err != nil {
			return time.Time{}, false
		}
		return t, false
	}

	// Local format with timezone
	t, err := time.Parse("20060102T150405", value)
	if err != nil {
		return time.Time{}, false
	}

	if loc != nil {
		t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc)
	}

	return t, false
}

// buildEvent constructs a CalendarEvent from the collected properties.
func buildEvent(props map[string]string, attendees []pipeline.CalendarAddress, method string, timezones map[string]*time.Location) pipeline.CalendarEvent {
	ev := pipeline.CalendarEvent{
		Method:      method,
		UID:         props["UID"],
		Summary:     unescapeValue(props["SUMMARY"]),
		Description: unescapeValue(props["DESCRIPTION"]),
		Location:    unescapeValue(props["LOCATION"]),
		Status:      props["STATUS"],
		Sequence:    atoiCal(props["SEQUENCE"]),
		Attendees:   attendees,
	}

	// Parse start time
	if dtstart, ok := props["DTSTART"]; ok {
		paramKey := props["DTSTART_PARAMS"]
		t, allDay := parseDateTime(dtstart, paramKey, timezones)
		ev.DTStart = t
		ev.AllDay = allDay
	}

	// Parse end time
	if dtend, ok := props["DTEND"]; ok {
		paramKey := props["DTEND_PARAMS"]
		t, _ := parseDateTime(dtend, paramKey, timezones)
		ev.DTEnd = t
	}

	// Parse DTSTAMP
	if dtstamp, ok := props["DTSTAMP"]; ok {
		paramKey := props["DTSTAMP_PARAMS"]
		t, _ := parseDateTime(dtstamp, paramKey, timezones)
		ev.DTStamp = t
	}

	// Parse organizer
	if _, ok := props["ORGANIZER"]; ok {
		ev.Organizer = parseOrganizer(props["ORGANIZER_RAW_KEY"], props["ORGANIZER"])
	}

	return ev
}

// unescapeValue handles iCalendar escape sequences.
func unescapeValue(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\N", "\n")
	s = strings.ReplaceAll(s, "\\,", ",")
	s = strings.ReplaceAll(s, "\\;", ";")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// BuildCalendarReply generates an iCalendar REPLY for a calendar invite.
// partstat should be "ACCEPTED", "DECLINED", or "TENTATIVE".
//
// The event's text is whatever the invitation's sender wrote. A value iCalendar
// can escape is escaped; a UID or a parameter has no escape for a line break, so
// it loses it.
func BuildCalendarReply(event pipeline.CalendarEvent, attendeeEmail string, partstat string) string {
	var b calendarWriter
	b.line("BEGIN:VCALENDAR")
	b.line("VERSION:2.0")
	b.line("PRODID:-//RESTmail//Calendar//EN")
	b.line("METHOD:REPLY")
	b.line("BEGIN:VEVENT")
	b.line("UID:" + sanitizeHeaderValue(event.UID))
	b.line("DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z"))

	if !event.DTStart.IsZero() {
		b.line("DTSTART:" + event.DTStart.UTC().Format("20060102T150405Z"))
	}
	if !event.DTEnd.IsZero() {
		b.line("DTEND:" + event.DTEnd.UTC().Format("20060102T150405Z"))
	}

	if event.Summary != "" {
		b.line("SUMMARY:" + escapeValue(event.Summary))
	}

	if event.Organizer.Address != "" {
		orgLine := "ORGANIZER"
		if event.Organizer.Name != "" {
			orgLine += ";CN=" + sanitizeHeaderValue(event.Organizer.Name)
		}
		orgLine += ":mailto:" + event.Organizer.Address
		b.line(orgLine)
	}

	attendeeLine := "ATTENDEE;PARTSTAT=" + partstat + ":mailto:" + attendeeEmail
	b.line(attendeeLine)

	b.line("SEQUENCE:" + intToStr(event.Sequence))
	b.line("END:VEVENT")
	b.line("END:VCALENDAR")
	return b.String()
}

// calendarWriter writes iCalendar content lines, each ended by CRLF and
// folded so that no line is longer than 75 octets (RFC 5545 §3.1).
type calendarWriter struct {
	strings.Builder
}

// line writes one content line, folded.
func (w *calendarWriter) line(s string) {
	w.WriteString(foldContentLine(s))
}

// foldContentLine returns a content line ended by CRLF, with a CRLF and one
// space inserted wherever it would pass 75 octets (RFC 5545 §3.1). A fold never
// falls inside a UTF-8 sequence: a continuation line's leading space counts
// toward its 75.
func foldContentLine(s string) string {
	const limit = 75
	var b strings.Builder
	room := limit
	for len(s) > room {
		cut := room
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		room = limit - 1
	}
	b.WriteString(s)
	b.WriteString("\r\n")
	return b.String()
}

// escapeValue escapes special characters in iCalendar values.
func escapeValue(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

// intToStr converts an int to a string without importing strconv.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	digits := make([]byte, 0, 10)
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	// Reverse
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// GenerateICS creates a valid iCalendar (.ics) file from a CalendarEvent struct.
// It produces a complete VCALENDAR with a single VEVENT, supporting METHOD values
// of REQUEST, CANCEL, and REPLY. The returned bytes use CRLF line endings per RFC 5545.
func GenerateICS(event pipeline.CalendarEvent) ([]byte, error) {
	method := strings.ToUpper(event.Method)
	if method == "" {
		method = "REQUEST"
	}
	if method != "REQUEST" && method != "CANCEL" && method != "REPLY" {
		return nil, fmt.Errorf("unsupported METHOD: %s (must be REQUEST, CANCEL, or REPLY)", method)
	}

	if event.UID == "" {
		return nil, fmt.Errorf("UID is required")
	}

	var b calendarWriter
	b.line("BEGIN:VCALENDAR")
	b.line("VERSION:2.0")
	b.line("PRODID:-//RESTmail//Calendar//EN")
	b.line("METHOD:" + method)
	b.line("BEGIN:VEVENT")
	b.line("UID:" + event.UID)

	// DTSTAMP: required per RFC 5545; use the event's stamp or current time
	if !event.DTStamp.IsZero() {
		b.line("DTSTAMP:" + event.DTStamp.UTC().Format("20060102T150405Z"))
	} else {
		b.line("DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z"))
	}

	// DTSTART / DTEND
	if !event.DTStart.IsZero() {
		if event.AllDay {
			b.line("DTSTART;VALUE=DATE:" + event.DTStart.Format("20060102"))
		} else {
			b.line("DTSTART:" + event.DTStart.UTC().Format("20060102T150405Z"))
		}
	}
	if !event.DTEnd.IsZero() {
		if event.AllDay {
			b.line("DTEND;VALUE=DATE:" + event.DTEnd.Format("20060102"))
		} else {
			b.line("DTEND:" + event.DTEnd.UTC().Format("20060102T150405Z"))
		}
	}

	// SUMMARY
	if event.Summary != "" {
		b.line("SUMMARY:" + escapeValue(event.Summary))
	}

	// DESCRIPTION
	if event.Description != "" {
		b.line("DESCRIPTION:" + escapeValue(event.Description))
	}

	// LOCATION
	if event.Location != "" {
		b.line("LOCATION:" + escapeValue(event.Location))
	}

	// STATUS
	if event.Status != "" {
		b.line("STATUS:" + event.Status)
	}

	// ORGANIZER
	if event.Organizer.Address != "" {
		orgLine := "ORGANIZER"
		if event.Organizer.Name != "" {
			orgLine += ";CN=" + event.Organizer.Name
		}
		orgLine += ":mailto:" + event.Organizer.Address
		b.line(orgLine)
	}

	// ATTENDEES
	for _, att := range event.Attendees {
		attLine := "ATTENDEE"
		if att.Name != "" {
			attLine += ";CN=" + att.Name
		}
		if att.Role != "" {
			attLine += ";ROLE=" + att.Role
		}
		if att.PartStat != "" {
			attLine += ";PARTSTAT=" + att.PartStat
		}
		if att.RSVP {
			attLine += ";RSVP=TRUE"
		}
		attLine += ":mailto:" + att.Address
		b.line(attLine)
	}

	// SEQUENCE
	b.line("SEQUENCE:" + intToStr(event.Sequence))

	b.line("END:VEVENT")
	b.line("END:VCALENDAR")

	return []byte(b.String()), nil
}
