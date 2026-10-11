package setuplink

import (
	"net/url"
	"strings"
	"testing"
)

const code = "7KQ2-M9XD-4TPA-HV3N-8RWC-ZB6E"

// What the app is handed, and what a web server is not.
func TestSetup_TheCodeRidesAfterTheHash(t *testing.T) {
	link := Setup(DefaultBase, "", code)
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "rest-mail.com" || u.Path != "/setup" {
		t.Errorf("link = %q", link)
	}
	if u.RawQuery != "" {
		t.Errorf("link = %q: a code in the query reaches the web server's logs", link)
	}
	if got := carriedBy(t, link).Get("token"); got != code {
		t.Errorf("the link carries token %q, want %q", got, code)
	}
}

func TestSetup_NamesTheServerWhenItIsKnown(t *testing.T) {
	const server = "https://mail.example.com"
	for _, base := range []string{DefaultBase, ""} {
		parameters := carriedBy(t, Setup(base, server, code))
		if parameters.Get("server") != server || parameters.Get("token") != code {
			t.Errorf("base %q: link = %q", base, Setup(base, server, code))
		}
	}
	// A server prints a link naming no server: it does not know its address.
	if strings.Contains(Setup(DefaultBase, "", code), "server=") {
		t.Error("a link with no server named should carry no server")
	}
}

// Set nothing and no site is involved at all.
func TestSetup_WithoutABaseItIsTheAppsOwnScheme(t *testing.T) {
	link := Setup("", "", code)
	if !strings.HasPrefix(link, "restmail://setup?") || !strings.Contains(link, url.QueryEscape(code)) {
		t.Errorf("link = %q", link)
	}
}

func TestReset_StaysOnTheAppsScheme(t *testing.T) {
	// There is no page for a reset link, so it must not be given a web form.
	if link := Reset("https://mail.example.com", code); !strings.HasPrefix(link, "restmail://reset?") {
		t.Errorf("link = %q", link)
	}
}

func TestParseBase(t *testing.T) {
	for raw, want := range map[string]string{
		DefaultBase:                     DefaultBase,
		"https://example.com/claim/":    "https://example.com/claim",
		"  https://example.com/claim  ": "https://example.com/claim",
		"http://localhost:8080/setup":   "http://localhost:8080/setup",
		"":                              "",
		"   ":                           "",
	} {
		got, err := ParseBase(raw)
		if err != nil || got != want {
			t.Errorf("ParseBase(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	// A base that is not an address, or that already carries parameters, would
	// make a link nobody can open: say so at startup rather than print one.
	for _, raw := range []string{"rest-mail.com/setup", "restmail://setup", "https://example.com/setup#token=x", "https://example.com/setup?a=b", "ftp://example.com/x"} {
		if _, err := ParseBase(raw); err == nil {
			t.Errorf("ParseBase(%q) was accepted", raw)
		}
	}
}

// carriedBy reads a link's parameters, wherever that form of link keeps them:
// after the `#` for a web link, in the query for the app's own scheme.
func carriedBy(t *testing.T, link string) url.Values {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse %q: %v", link, err)
	}
	raw := u.RawQuery
	if u.Fragment != "" {
		raw = u.Fragment
	}
	parameters, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse %q of %q: %v", raw, link, err)
	}
	return parameters
}
