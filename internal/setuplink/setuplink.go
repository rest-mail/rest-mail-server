// Package setuplink builds the links that hand the rest-mail app something to
// redeem on a server: scanned off a QR code, or sent to whoever is going to
// redeem it.
package setuplink

import (
	"net/url"
	"strings"
)

// SignInBase is where sign-in links point. A web link, because a phone's own
// camera offers to open one it sees in a QR code, and a chat app makes one a
// link rather than plain text; the app takes it before a browser does (iOS),
// and where it does not, the page there hands it to the app (Android) or shows
// it to type in. The page belongs to this project and never learns a password:
// everything rides after the `#`, which browsers never send to a web server.
const SignInBase = "https://rest-mail.com/setup"

// SignIn is the link that opens the app with a sign-in filled in: the server,
// the username and the password. The installer shows it for the first admin,
// whose password is the bootstrap one. In the order a person reads them, so
// that the link shown as text is legible.
func SignIn(server, username, password string) string {
	var b strings.Builder
	b.WriteString(SignInBase)
	b.WriteString("#server=")
	b.WriteString(url.QueryEscape(server))
	b.WriteString("&username=")
	b.WriteString(url.QueryEscape(username))
	b.WriteString("&password=")
	b.WriteString(url.QueryEscape(password))
	return b.String()
}

// Reset is the link that lets an admin back in. It stays on the app's own
// scheme: there is no page for it, and the app cannot redeem one from a link
// yet.
func Reset(server, code string) string {
	parameters := url.Values{"token": {code}}
	if server != "" {
		parameters.Set("server", server)
	}
	return "rest-mail://reset?" + parameters.Encode()
}
