package setuplink

import (
	"net/url"
	"strings"
	"testing"
)

const token = "q3B9xX7mVt0kQ2pLr8sYw1nZc4dHf6jA"

func TestReset_StaysOnTheAppsScheme(t *testing.T) {
	// There is no page for a reset link, so it must not be given a web form.
	link := Reset("https://mail.example.com", token)
	if !strings.HasPrefix(link, "restmail://reset?") {
		t.Fatalf("link = %q", link)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("token"); got != token {
		t.Errorf("token = %q, want %q", got, token)
	}
	if got := u.Query().Get("server"); got != "https://mail.example.com" {
		t.Errorf("server = %q", got)
	}
}

// A link with no server leaves the app to ask which.
func TestReset_WithoutAServerNamesNone(t *testing.T) {
	u, err := url.Parse(Reset("", token))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Has("server") {
		t.Errorf("link names a server: %q", u)
	}
}
