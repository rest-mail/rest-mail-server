package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/restmail/restmail/internal/console"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// version is stamped by the release build (.goreleaser.yaml); a local build
// says "dev".
var version = "dev"

const topUsage = `restmail — run a rest-mail server

  restmail status        what a server is, and which version it runs
  restmail reset-admin   let an admin back in; also hands a server over
  restmail sign-in-link  the link, and QR code, that opens the app signed in
  restmail password-hash a bcrypt hash of an admin password, for a deployment
  restmail import-account create an account with its existing password hash
  restmail import-passwd-file create a Dovecot passwd-file's accounts, keeping passwords
  restmail import-maildir import a Maildir and its folders through the public API
  restmail import-mbox    import a From-framed mbox through the public API
  restmail tlsa-records   prepare SMTP TLSA records from public certificates
  restmail version       print the version
  restmail [flags]       the interactive admin console (-api, -user, -pass)

restmail <command> --help for each command's flags. Every command except the
console takes --json for one JSON object on stdout.
`

func main() {
	if code, handled := runSubcommand(os.Args, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}

	showVersion := flag.Bool("version", false, "Print the version and exit")
	apiURL := flag.String("api", "http://localhost:8080", "REST API base URL")
	adminUser := flag.String("user", "", "The admin account's address, to sign in with (or set RESTMAIL_ADMIN_ADDRESS)")
	adminPass := flag.String("pass", "", "Its password (or set RESTMAIL_ADMIN_PASSWORD)")
	adminCode := flag.String("code", "", "A current code from its authenticator, if it has 2FA (or set RESTMAIL_ADMIN_CODE)")
	flag.Parse()
	if *showVersion {
		fmt.Println("restmail", version)
		return
	}

	// Prefer flags, fall back to env vars
	address := *adminUser
	if address == "" {
		address = os.Getenv("RESTMAIL_ADMIN_ADDRESS")
	}
	pass := *adminPass
	if pass == "" {
		pass = os.Getenv("RESTMAIL_ADMIN_PASSWORD")
	}
	code := *adminCode
	if code == "" {
		code = os.Getenv("RESTMAIL_ADMIN_CODE")
	}

	client := apiclient.New(*apiURL)

	// Login as admin to get a token for API calls
	token := ""
	var capabilities []string
	if address != "" && pass != "" {
		loginResp, err := client.Login(address, pass, code)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: admin login failed: %v\n", err)
		} else {
			token = loginResp.Data.AccessToken
			capabilities = loginResp.Data.Capabilities
		}
	} else {
		fmt.Fprintln(os.Stderr, "hint: set -user/-pass (and -code) flags or RESTMAIL_ADMIN_ADDRESS/RESTMAIL_ADMIN_PASSWORD env vars for admin access")
	}

	model := console.NewModel(client, token, capabilities)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runSubcommand runs the command named by args[1], if there is one. handled is
// false only when the arguments are the console's own, which main then parses.
func runSubcommand(args []string, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) < 2 {
		return 0, false
	}
	client := &http.Client{Timeout: 30 * time.Second}
	switch args[1] {
	case "reset-admin":
		return runResetAdmin(context.Background(), args[2:], client, localKeys, stdout, stderr), true
	case "status":
		return runStatus(context.Background(), args[2:], client, keyFile, stdout, stderr), true
	case "sign-in-link":
		return runSignInLink(args[2:], os.Stdin, stdout, stderr), true
	case "password-hash":
		return runPasswordHash(args[2:], os.Stdin, stdout, stderr), true
	case "import-account":
		return runImportAccount(context.Background(), args[2:], os.Stdin, os.Getenv("RESTMAIL_API_KEY"), client, stdout, stderr), true
	case "import-passwd-file":
		return runImportPasswdFile(context.Background(), args[2:], os.Stdin, os.Getenv("RESTMAIL_API_KEY"), client, stdout, stderr), true
	case "import-maildir":
		return runImportMail(context.Background(), "maildir", args[2:], os.Getenv("RESTMAIL_API_KEY"), client, stdout, stderr), true
	case "import-mbox":
		return runImportMail(context.Background(), "mbox", args[2:], os.Getenv("RESTMAIL_API_KEY"), client, stdout, stderr), true
	case "tlsa-records":
		return runTLSARecords(args[2:], stdout, stderr), true
	case "version":
		fmt.Fprintln(stdout, "restmail", version)
		return 0, true
	case "help", "--help", "-h":
		fmt.Fprint(stdout, topUsage)
		return 0, true
	}
	// A word, so it was meant as a command; anything starting with "-" is the
	// console's own flags. Without this, a word this version does not know — a
	// typo, or a command from a newer release — silently opened the console
	// instead of saying so: `restmail status` on a build from before that
	// command existed showed the admin console.
	if !strings.HasPrefix(args[1], "-") {
		fmt.Fprintf(stderr, "restmail: there is no %q command in restmail %s\n\n%s", args[1], version, topUsage)
		return 2, true
	}
	return 0, false
}
