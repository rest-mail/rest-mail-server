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

  restmail status        what a server is, and whether it has been claimed
  restmail setup         claim a freshly installed server (QR code for the app)
  restmail reset-admin   let an admin back in; also hands a server over
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
	adminUser := flag.String("user", "", "Admin username for login (or set RESTMAIL_ADMIN_USERNAME)")
	adminPass := flag.String("pass", "", "Admin password (or set RESTMAIL_ADMIN_PASSWORD)")
	flag.Parse()
	if *showVersion {
		fmt.Println("restmail", version)
		return
	}

	// Prefer flags, fall back to env vars
	username := *adminUser
	if username == "" {
		username = os.Getenv("RESTMAIL_ADMIN_USERNAME")
	}
	pass := *adminPass
	if pass == "" {
		pass = os.Getenv("RESTMAIL_ADMIN_PASSWORD")
	}

	client := apiclient.New(*apiURL)

	// Login as admin to get a token for API calls
	token := ""
	if username != "" && pass != "" {
		loginResp, err := client.LoginAdmin(username, pass)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: admin login failed: %v\n", err)
		} else {
			token = loginResp.Data.AccessToken
		}
	} else {
		fmt.Fprintln(os.Stderr, "hint: set -user/-pass flags or RESTMAIL_ADMIN_USERNAME/RESTMAIL_ADMIN_PASSWORD env vars for admin access")
	}

	model := console.NewModel(client, token)

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
	case "setup", "reset-admin":
		return runSetupCommand(context.Background(), args[1], args[2:], client, localKeys, stdout, stderr), true
	case "status":
		return runStatus(context.Background(), args[2:], client, stdout, stderr), true
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
