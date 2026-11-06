package quiethours

import (
	"testing"
	"time"
)

func TestQuietHoursUntil(t *testing.T) {
	for _, test := range []struct {
		name, start, end, zone, now, want string
	}{
		{"daytime", "09:00", "17:00", "Asia/Tokyo", "2026-11-03T01:00:00Z", "2026-11-03T08:00:00Z"},
		{"start inclusive", "22:00", "08:00", "Europe/Berlin", "2026-11-02T21:00:00Z", "2026-11-03T07:00:00Z"},
		{"cross midnight", "22:00", "08:00", "Europe/Berlin", "2026-11-03T02:05:13Z", "2026-11-03T07:00:00Z"},
		{"end exclusive", "22:00", "08:00", "Europe/Berlin", "2026-11-03T07:00:00Z", ""},
		{"outside", "22:00", "08:00", "Europe/Berlin", "2026-11-03T13:00:00Z", ""},
		{"spring forward", "22:00", "08:00", "Europe/Berlin", "2026-03-28T21:00:00Z", "2026-03-29T06:00:00Z"},
		{"nonexistent end", "22:00", "02:30", "Europe/Berlin", "2026-03-28T21:00:00Z", "2026-03-29T01:00:00Z"},
		{"fall back", "22:00", "08:00", "Europe/Berlin", "2026-10-24T20:00:00Z", "2026-10-25T07:00:00Z"},
		{"first repeated hour", "01:00", "03:00", "Europe/Berlin", "2026-10-25T00:15:00Z", "2026-10-25T02:00:00Z"},
		{"second repeated hour", "01:00", "03:00", "Europe/Berlin", "2026-10-25T01:15:00Z", "2026-10-25T02:00:00Z"},
		{"half hour DST jump", "22:00", "02:15", "Australia/Lord_Howe", "2025-10-04T12:00:00Z", "2025-10-04T15:30:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, test.now)
			if err != nil {
				t.Fatal(err)
			}
			setting := Setting{Enabled: true, Start: test.start, End: test.end, TimeZone: test.zone}
			until := setting.Until(now)
			if test.want == "" {
				if !until.IsZero() {
					t.Fatalf("unexpected quiet hours until %v", until)
				}
			} else if until.Format(time.RFC3339) != test.want {
				t.Fatalf("until %v, want %s", until, test.want)
			}
			setting.Enabled = false
			if !setting.Until(now).IsZero() {
				t.Fatal("disabled setting held a notification")
			}
		})
	}
}

func TestQuietHoursValidation(t *testing.T) {
	if fields := (Setting{}).Validate(); len(fields) != 0 {
		t.Fatal(fields)
	}
	for _, test := range []struct{ start, end, zone, field string }{
		{"22:00", "08:00", "Mars/Olympus", "time_zone"},
		{"22:00", "08:00", "Local", "time_zone"},
		{"22:00", "08:00", "", "time_zone"},
		{"24:00", "08:00", "UTC", "start"},
		{"9:00", "08:00", "UTC", "start"},
		{"", "08:00", "UTC", "start"},
		{"22:00", "22:00", "UTC", "end"},
	} {
		if fields := (Setting{Enabled: true, Start: test.start, End: test.end, TimeZone: test.zone}).Validate(); fields[test.field] == "" {
			t.Fatalf("%+v: %v", test, fields)
		}
	}
}
