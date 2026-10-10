package main

import (
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/mdp/qrterminal/v4"
)

// printSetupBanner shows the setup code of a server nobody has claimed: a QR
// code of the app's link, and the code itself to type. It goes to the process's
// own output, which every platform shows whoever runs the server. The server
// does not know the address it is reached at — that is the deployment's to say
// — so the app asks for it, or `restmail setup --server URL --code CODE` draws a
// QR code with both.
func printSetupBanner(w io.Writer, code string, expires time.Time) {
	link := setupLink(code)
	fmt.Fprint(w, "\n==================== rest-mail: claim this server ====================\n\n")
	fmt.Fprint(w, "Nobody has claimed this server yet. Scan with the rest-mail app to\nbecome its first admin:\n\n")
	qrterminal.GenerateWithConfig(link, qrterminal.Config{Level: qrterminal.M, Writer: w, HalfBlocks: true, QuietZone: 2})
	fmt.Fprintf(w, "\nOr enter the setup code in the app:  %s\n\n", code)
	fmt.Fprintf(w, "It works once, until %s or until the server restarts; a\n", expires.UTC().Format("2006-01-02 15:04 MST"))
	fmt.Fprint(w, "restart prints a new code. Anyone who can read this output can claim\nthe server, so claim it now.\n")
	fmt.Fprint(w, "\n======================================================================\n\n")
}

// setupLink is what the app opens. It names no server: the app asks which.
func setupLink(code string) string {
	return "restmail://setup?" + url.Values{"token": {code}}.Encode()
}
