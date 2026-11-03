package wire

import "time"

// ParseDateTime parses an IMAP date-time (RFC 3501 §9) as sent in the
// optional APPEND argument, e.g. "25-Jul-2026 13:04:05 +0000". A single-digit
// day must be space-padded or zero-padded. Validate the wire grammar before
// time.Parse, which also accepts bare days, fractional seconds and extra spaces.
func ParseDateTime(s string) (time.Time, bool) {
	if len(s) != 26 || s[2] != '-' || s[6] != '-' || s[11] != ' ' ||
		s[14] != ':' || s[17] != ':' || s[20] != ' ' || (s[21] != '+' && s[21] != '-') {
		return time.Time{}, false
	}
	for _, i := range []int{0, 1, 7, 8, 9, 10, 12, 13, 15, 16, 18, 19, 22, 23, 24, 25} {
		if i == 0 && s[i] == ' ' {
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return time.Time{}, false
		}
	}
	// Go's numeric-zone parser permits hour 24 and minute 60. Neither is a
	// valid hhmm offset; reject them instead of silently normalizing the zone.
	if s[22:24] > "23" || s[24:26] > "59" {
		return time.Time{}, false
	}
	if t, err := time.Parse("_2-Jan-2006 15:04:05 -0700", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// ParseSearchDate parses RFC 3501 §9's date token: date-text, optionally
// enclosed in DQUOTEs. Unlike an astring, it permits neither quoted escapes
// nor surrounding whitespace. Validate the wire shape before calendar parsing.
func ParseSearchDate(s string) (time.Time, bool) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	dayLen := len(s) - 9
	if (dayLen != 1 && dayLen != 2) || s[dayLen] != '-' || s[dayLen+4] != '-' {
		return time.Time{}, false
	}
	for i := range s {
		if i < dayLen || i > dayLen+4 {
			if s[i] < '0' || s[i] > '9' {
				return time.Time{}, false
			}
		}
	}
	t, err := time.Parse("2-Jan-2006", s)
	return t, err == nil
}
