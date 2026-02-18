package mail

import (
	"regexp"
	"testing"
)

func TestGenerateMessageID_Format(t *testing.T) {
	id := GenerateMessageID("example.com")

	// Must be wrapped in angle brackets with hex-uuid@domain format.
	re := regexp.MustCompile(`^<[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}@example\.com>$`)
	if !re.MatchString(id) {
		t.Errorf("GenerateMessageID returned %q, want format <hex-uuid@example.com>", id)
	}
}

func TestGenerateMessageID_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := GenerateMessageID("test.local")
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate Message-ID after %d iterations: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestDomainFromAddress(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"user@example.com", "example.com"},
		{"user@sub.example.com", "sub.example.com"},
		{"nodomain", "nodomain"},
		{"", ""},
		{"user@", ""},
		{"@domain.com", "domain.com"},
		{"multi@at@signs.com", "signs.com"},
	}
	for _, tt := range tests {
		got := DomainFromAddress(tt.input)
		if got != tt.want {
			t.Errorf("DomainFromAddress(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
