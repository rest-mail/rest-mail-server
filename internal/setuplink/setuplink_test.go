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

// What the app is handed, and what a web server is not: everything after the
// `#`, and every character of the password back as it went in.
func TestSignIn_EverythingRidesAfterTheHash(t *testing.T) {
	const password = "kX9+mQ2/rT7=vL4_wN8&pZ1 #?"
	link := SignIn("https://mail.example.com", "admin", password)
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "rest-mail.com" || u.Path != "/setup" || u.RawQuery != "" {
		t.Fatalf("link = %q", link)
	}
	// As the app and the page read it: the fragment as sent, then decoded.
	got, err := url.ParseQuery(u.EscapedFragment())
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("server") != "https://mail.example.com" || got.Get("username") != "admin" || got.Get("password") != password {
		t.Errorf("link carries %v", got)
	}
	// Server, username, password: the order a person reads them in.
	if strings.Index(link, "server=") > strings.Index(link, "username=") ||
		strings.Index(link, "username=") > strings.Index(link, "password=") {
		t.Errorf("parameters out of order: %q", link)
	}
}
