package mime

import (
	"strings"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

// ParseCalendar parses an iCalendar (text/calendar) body and extracts calendar events.
// It handles line unfolding, VTIMEZONE blocks, and multiple VEVENT sections.
func ParseCalendar(data string) ([]pipeline.CalendarEvent, error) {
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
func BuildCalendarReply(event pipeline.CalendarEvent, attendeeEmail string, partstat string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	b.WriteString("VERSION:2.0\r\n")
	b.WriteString("PRODID:-//RESTmail//Calendar//EN\r\n")
	b.WriteString("METHOD:REPLY\r\n")
	b.WriteString("BEGIN:VEVENT\r\n")
	b.WriteString("UID:" + event.UID + "\r\n")
	b.WriteString("DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z") + "\r\n")

	if !event.DTStart.IsZero() {
		b.WriteString("DTSTART:" + event.DTStart.UTC().Format("20060102T150405Z") + "\r\n")
	}
	if !event.DTEnd.IsZero() {
		b.WriteString("DTEND:" + event.DTEnd.UTC().Format("20060102T150405Z") + "\r\n")
	}

	if event.Summary != "" {
		b.WriteString("SUMMARY:" + escapeValue(event.Summary) + "\r\n")
	}

	if event.Organizer.Address != "" {
		orgLine := "ORGANIZER"
		if event.Organizer.Name != "" {
			orgLine += ";CN=" + event.Organizer.Name
		}
		orgLine += ":mailto:" + event.Organizer.Address
		b.WriteString(orgLine + "\r\n")
	}

	attendeeLine := "ATTENDEE;PARTSTAT=" + partstat + ":mailto:" + attendeeEmail
	b.WriteString(attendeeLine + "\r\n")

	b.WriteString("SEQUENCE:" + intToStr(event.Sequence) + "\r\n")
	b.WriteString("END:VEVENT\r\n")
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

// escapeValue escapes special characters in iCalendar values.
func escapeValue(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
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
