package quiethours

import (
	"fmt"
	"time"
	_ "time/tzdata"
)

type Setting struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start" pattern:"^$|^([01][0-9]|2[0-3]):[0-5][0-9]$" example:"22:00"`
	End      string `json:"end" pattern:"^$|^([01][0-9]|2[0-3]):[0-5][0-9]$" example:"08:00"`
	TimeZone string `json:"time_zone" format:"iana-time-zone" example:"Europe/Berlin"`
}

func (s Setting) Validate() map[string]string {
	fields := map[string]string{}
	for name, value := range map[string]string{"start": s.Start, "end": s.End} {
		if value == "" && !s.Enabled {
			continue
		}
		if parsed, err := time.Parse("15:04", value); err != nil || parsed.Format("15:04") != value {
			fields[name] = "must be a local time in HH:MM format"
		}
	}
	if s.Start != "" && s.Start == s.End {
		fields["end"] = "must differ from start"
	}
	if s.TimeZone != "" || s.Enabled {
		if _, err := location(s.TimeZone); err != nil {
			fields["time_zone"] = "must be an IANA time zone name"
		}
	}
	return fields
}

func location(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("not an IANA time zone: %q", name)
	}
	return time.LoadLocation(name)
}

func (s Setting) Until(now time.Time) time.Time {
	if !s.Enabled || len(s.Validate()) != 0 {
		return time.Time{}
	}
	zone, _ := location(s.TimeZone)
	start, _ := time.Parse("15:04", s.Start)
	end, _ := time.Parse("15:04", s.End)
	startMinute := start.Hour()*60 + start.Minute()
	endMinute := end.Hour()*60 + end.Minute()
	contains := func(at time.Time) bool {
		local := at.In(zone)
		minute := local.Hour()*60 + local.Minute()
		if startMinute < endMinute {
			return minute >= startMinute && minute < endMinute
		}
		return minute >= startMinute || minute < endMinute
	}
	if !contains(now) {
		return time.Time{}
	}
	for boundary := now.Truncate(time.Minute).Add(time.Minute); boundary.Before(now.Add(49 * time.Hour)); boundary = boundary.Add(time.Minute) {
		if !contains(boundary) {
			return boundary
		}
	}
	return time.Time{}
}
