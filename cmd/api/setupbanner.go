package main

import (
	"fmt"
	"io"
	"time"

	"github.com/mdp/qrterminal/v4"
	"github.com/restmail/restmail/internal/setuplink"
)

// printSetupBanner shows the setup code of a server nobody has claimed: a QR
// code of the link that claims it, and the code itself to type. It goes to the
// process's own output, which every platform shows whoever runs the server. The
// server does not know the address it is reached at — that is the deployment's
// to say — so the link names no server and the app asks which; `restmail setup
// --server URL --code CODE` draws one naming both.
func printSetupBanner(w io.Writer, base, code string, expires time.Time) {
	link := setuplink.Setup(base, "", code)
	fmt.Fprint(w, "\n==================== rest-mail: claim this server ====================\n\n")
	fmt.Fprint(w, "Nobody has claimed this server yet. Scan this to become its first\nadmin — with your phone's camera, or the rest-mail app:\n\n")
	qrterminal.GenerateWithConfig(link, qrterminal.Config{Level: qrterminal.M, Writer: w, HalfBlocks: true, QuietZone: 2})
	fmt.Fprintf(w, "\nOr open:                            %s\n", link)
	fmt.Fprintf(w, "Or enter the setup code in the app: %s\n\n", code)
	fmt.Fprintf(w, "It works once, until %s or until the server restarts; a\n", expires.UTC().Format("2006-01-02 15:04 MST"))
	fmt.Fprint(w, "restart prints a new code. Anyone who can read this output can claim\nthe server, so claim it now.\n")
	fmt.Fprint(w, "\n======================================================================\n\n")
}
