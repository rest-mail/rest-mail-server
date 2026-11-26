package mailmodel

import (
	"encoding/json"
	"go/build"
	"strings"
	"testing"
	"time"
)

func TestAuthResultKeepsTheSelectorInsideTheServer(t *testing.T) {
	got, err := json.Marshal(AuthResult{Method: AuthDKIM, Result: "pass", Property: PropHeaderD, Domain: "example.com", Selector: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"method":"dkim","result":"pass","property":"header.d","domain":"example.com"}`
	if string(got) != want {
		t.Fatalf("AuthResult encodes as %s, want %s", got, want)
	}
}

func TestAuthResultStringWritesOnlyAPlainHostName(t *testing.T) {
	cases := []struct {
		r    AuthResult
		want string
	}{
		{AuthResult{Method: AuthSPF, Result: "pass", Property: PropMailFrom, Domain: "example.com"}, "spf=pass smtp.mailfrom=example.com"},
		{AuthResult{Method: AuthARC, Result: "pass", Domain: "sealer.example"}, "arc=pass"},
		{AuthResult{Method: AuthDMARC, Result: "fail", Property: PropHeaderFrom, Domain: "x.example; dkim=pass"}, "dmarc=fail"},
	}
	for _, tc := range cases {
		if got := tc.r.String(); got != tc.want {
			t.Errorf("%+v renders as %q, want %q", tc.r, got, tc.want)
		}
	}
}

func TestIsHostName(t *testing.T) {
	for name, want := range map[string]bool{
		"example.com":                   true,
		"a-b.example":                   true,
		"":                              false,
		"example..com":                  false,
		"exa mple.com":                  false,
		"münchen.example":               false,
		strings.Repeat("a", 64):         false,
		strings.Repeat("a.", 127) + "a": false,
	} {
		if got := IsHostName(name); got != want {
			t.Errorf("IsHostName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCalendarEventWireShape(t *testing.T) {
	ev := CalendarEvent{Method: "REQUEST", UID: "u1", Summary: "s",
		DTStart: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC), DTEnd: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC),
		Organizer: CalendarAddress{Address: "o@example.com"}}
	got, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"method":"REQUEST","uid":"u1","summary":"s","dtstart":"2026-11-26T09:00:00Z","dtend":"2026-11-26T10:00:00Z","all_day":false,"organizer":{"address":"o@example.com"},"sequence":0,"dtstamp":"0001-01-01T00:00:00Z"}`
	if string(got) != want {
		t.Fatalf("CalendarEvent encodes as %s, want %s", got, want)
	}
}

// A client links this package, so it must not reach into the server's packages.
func TestImportsNothingFromTheServer(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if strings.Contains(imp, "/internal/") || strings.HasPrefix(imp, "github.com/rest-mail/rest-mail-server/") {
			t.Errorf("mailmodel imports %s", imp)
		}
	}
}
