// Package setuplink builds the links that hand the rest-mail app something to
// redeem on a server: scanned off a QR code, or sent to whoever is going to
// redeem it.
package setuplink

import "net/url"

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
