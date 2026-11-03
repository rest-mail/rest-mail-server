package wire

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FuzzSearchDate compares the date token parser to RFC 3501 §9's grammar and
// an independent Gregorian calendar oracle, including century leap-year rules.
// Generated dates ensure valid inputs remain reachable during fuzzing.
func FuzzSearchDate(f *testing.F) {
	for _, token := range []string{
		"1-Jan-2025", `"01-jAn-2025"`, "29-Feb-2024", "29-Feb-2000", "29-Feb-1900",
		"31-Apr-2025", "0-Jan-2025", "001-Jan-2025", "1-Jan-0000", "31-Dec-9999",
		`" 1-Jan-2025"`, `"1-Jan-2025 "`, "1-Jan-2025\t", "1-Jan-2025\u00a0",
		`"1-Jan-2025`, `1-Jan-2025"`, `"1-Jan-2025\""`, "", "1-Jan-2025\x00",
	} {
		f.Add(token, uint16(2024), byte(1), byte(1))
	}
	grammar := regexp.MustCompile(`^(?:([0-9]{1,2})-([A-Za-z]{3})-([0-9]{4})|"([0-9]{1,2})-([A-Za-z]{3})-([0-9]{4})")$`)
	months := []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	f.Fuzz(func(t *testing.T, token string, year uint16, month, day byte) {
		groups := grammar.FindStringSubmatch(token)
		wantOK := false
		var want time.Time
		if groups != nil {
			if groups[1] == "" {
				groups = groups[3:]
			}
			d, _ := strconv.Atoi(groups[1])
			y, _ := strconv.Atoi(groups[3])
			for i, name := range months {
				if strings.ToLower(groups[2]) != name {
					continue
				}
				days := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[i]
				if i == 1 && y%4 == 0 && (y%100 != 0 || y%400 == 0) {
					days++
				}
				wantOK = d >= 1 && d <= days
				want = time.Date(y, time.Month(i+1), d, 0, 0, 0, 0, time.UTC)
			}
		}
		got, ok := ParseSearchDate(token)
		if ok != wantOK || (ok && !got.Equal(want)) {
			t.Fatalf("ParseSearchDate(%q) = %v, %t; oracle %v, %t", token, got, ok, want, wantOK)
		}
		want = time.Date(int(year)%10000, time.Month(month%12+1), int(day%28+1), 0, 0, 0, 0, time.UTC)
		for _, generated := range []string{want.Format("2-Jan-2006"), fmt.Sprintf("\"%s\"", want.Format("02-Jan-2006"))} {
			got, ok = ParseSearchDate(generated)
			if !ok || !got.Equal(want) {
				t.Fatalf("valid generated date %q: %v, %t", generated, got, ok)
			}
		}
	})
}
