// Package setuplink builds the link that claims a freshly installed server.
//
// A link is how a setup code travels: scanned off a QR code, or sent to
// whoever is going to claim the server. Two forms do that.
//
//	https://rest-mail.com/setup#token=CODE   a web link
//	restmail://setup?token=CODE              the app's own scheme
//
// The web form is the default because a web link is tappable everywhere a link
// is — a phone's own camera recognises one in a QR code and offers to open it,
// and a chat app makes it a link rather than plain text — where a custom scheme
// is usually left as text. Nothing is given away by it: the code rides after
// the `#`, which browsers never send to a web server, and the app takes the
// link before a browser sees it at all. Where the app is not installed, the
// page says what the code is for.
//
// Which site does that is a setting, because it need not be this project's:
// point it at your own, or set nothing and get the app's scheme, which
// involves no site at all.
package setuplink

import (
	"fmt"
	"net/url"
	"strings"
)

// DefaultBase is where setup links point unless a server says otherwise. The
// page there belongs to this project and never learns a code: it only tells
// whoever opens it what to do when the app did not take the link.
const DefaultBase = "https://rest-mail.com/setup"

// ParseBase checks a configured base. Empty is allowed and means the app's own
// scheme, with no site involved.
func ParseBase(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("RESTMAIL_SETUP_LINK_BASE must be an http(s) address or empty, got %q", base)
	}
	if u.Fragment != "" || u.RawQuery != "" {
		return "", fmt.Errorf("RESTMAIL_SETUP_LINK_BASE carries the code itself, so it must be a plain address, got %q", base)
	}
	return strings.TrimRight(base, "/"), nil
}

// Setup is the link that claims a server with code. server may be empty: a
// server does not know the address it is reached at, so the link it prints
// names none and the app asks which. Whoever does know — the CLI — says.
func Setup(base, server, code string) string {
	parameters := url.Values{"token": {code}}
	if server != "" {
		parameters.Set("server", server)
	}
	if base == "" {
		return "restmail://setup?" + parameters.Encode()
	}
	// After the `#`: what claims a mail server is never sent to a web server.
	return base + "#" + parameters.Encode()
}

// Reset is the link that lets an admin back in. It stays on the app's own
// scheme: there is no page for it, and the app cannot redeem one from a link
// yet.
func Reset(server, code string) string {
	parameters := url.Values{"token": {code}}
	if server != "" {
		parameters.Set("server", server)
	}
	return "restmail://reset?" + parameters.Encode()
}
