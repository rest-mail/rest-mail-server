package timezone

import "testing"

func TestLoad(t *testing.T) {
	for _, name := range []string{"UTC", "Europe/Berlin", "America/New_York", "Asia/Tokyo"} {
		location, err := Load(name)
		if err != nil {
			t.Errorf("Load(%q): %v", name, err)
		} else if location.String() != name {
			t.Errorf("Load(%q) = %s", name, location)
		}
	}
	for _, name := range []string{"", "Local", "Mars/Olympus", "../etc/passwd", "+01:00"} {
		if location, err := Load(name); err == nil {
			t.Errorf("Load(%q) = %s, want a refusal", name, location)
		}
	}
}
