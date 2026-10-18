package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mdp/qrterminal/v4"
	"github.com/restmail/restmail/internal/setuplink"
	"golang.org/x/term"
	"rsc.io/qr"
)

// `restmail sign-in-link` shows the link that opens the rest-mail app signed in
// to a server: as a QR code a phone's camera opens, and always as text, with
// the server, the account's address and its password beside it. The installer
// shows the first account's password this way, and `chore password:reset` a
// reset one.
//
// The QR code is drawn only where it can be: on a terminal wide enough for it.
// The text is there every time, because nothing here can tell whether a QR code
// it drew can be scanned — a tiny font, or a terminal that mangles the block
// characters — and whoever installed the server must not be left with only an
// unreadable picture.

const signInUsage = `restmail sign-in-link — the link that opens the rest-mail app, signed in

  printf '%s' "$PASSWORD" | restmail sign-in-link --server https://mail.example.com --address admin@example.com

Shows it as a QR code, when the terminal is wide enough for one, and always as
text with the server, address and password beside it. A phone's camera opens
the app from the QR code with the sign-in filled in. The password is read from
stdin, never from the command line, where any process on the machine could
read it.

  --server URL     the server's address (https; http only for localhost)
  --address ADDR   whose password it is: the account's address
  --no-qr          the text only
  --json           one JSON object on stdout; errors as one on stderr
  --show-secret    include the link and the password in --json output

Exit status: 0 ok, 2 usage.
`

// qrQuietZone is the blank border around a drawn QR code, in modules.
const qrQuietZone = 2

// terminalColumns is how wide w is, when it is a terminal; 0 when it is not, so
// nothing is drawn into a file or a pipe. Replaced in tests.
var terminalColumns = func(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0
	}
	columns, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return columns
}

// isTerminal says whether r is a person typing rather than a pipe.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func runSignInLink(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sign-in-link", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
	address := fs.String("address", "", "whose password it is")
	noQR := fs.Bool("no-qr", false, "the text only")
	asJSON := fs.Bool("json", false, "JSON output")
	showSecret := fs.Bool("show-secret", false, "include the link and the password in --json output")
	help := fs.Bool("help", false, "show help")

	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, signInUsage)
		return exitOK
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	name := strings.ToLower(strings.TrimSpace(*address))
	if local, domain, ok := strings.Cut(name, "@"); !ok || local == "" || domain == "" {
		return o.fail(exitUsage, "bad_flag", "--address must be the account's address, such as admin@example.com")
	}
	if isTerminal(stdin) {
		return o.fail(exitUsage, "no_password", `the password comes on stdin: printf '%s' "$PASSWORD" | restmail sign-in-link --server URL --address ADDRESS`)
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 4096))
	if err != nil {
		return o.fail(exitFailed, "read_failed", err.Error())
	}
	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		return o.fail(exitUsage, "no_password", `no password on stdin: printf '%s' "$PASSWORD" | restmail sign-in-link --server URL --address ADDRESS`)
	}

	link := setuplink.SignIn(base.String(), name, password)
	if o.json {
		result := map[string]any{"server": base.String(), "address": name}
		if *showSecret {
			result["link"], result["password"] = link, password
		} else {
			result["link_redacted"] = true
		}
		_ = json.NewEncoder(stdout).Encode(result)
		return exitOK
	}

	if !*noQR && fitsQR(link, terminalColumns(stdout)) {
		fmt.Fprint(stdout, "  Scan this with your phone's camera: it opens the rest-mail app, signed in.\n\n")
		qrterminal.GenerateWithConfig(link, qrterminal.Config{Level: qrterminal.M, Writer: stdout, HalfBlocks: true, QuietZone: qrQuietZone})
		fmt.Fprintln(stdout)
	} else {
		fmt.Fprint(stdout, "  Open this link on your phone: it opens the rest-mail app, signed in.\n  Or sign in by hand, with the server, address and password below.\n\n")
	}
	fmt.Fprintf(stdout, "  Link:      %s\n  Server:    %s\n  Address:   %s\n  Password:  %s\n", link, base, name, password)
	return exitOK
}

// fitsQR says whether a QR code of link fits in columns: one column per module,
// and the quiet zone on either side.
func fitsQR(link string, columns int) bool {
	code, err := qr.Encode(link, qr.M)
	if err != nil {
		return false
	}
	return columns >= code.Size+2*qrQuietZone
}
