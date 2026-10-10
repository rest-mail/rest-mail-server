package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/restmail/restmail/internal/console"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// version is stamped by the release build (.goreleaser.yaml); a local build
// says "dev".
var version = "dev"

const topUsage = `restmail — run a rest-mail server

  restmail setup         claim a freshly installed server (QR code for the app)
  restmail reset-admin   let an admin back in; also hands a server over
  restmail version       print the version
  restmail [flags]       the interactive admin console (-api, -user, -pass)

restmail <command> --help for each command's flags. Every command except the
console takes --json for one JSON object on stdout.
`

func main() {
	// Subcommands first; anything else is the console's own flags.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "setup", "reset-admin":
			os.Exit(runSetupCommand(context.Background(), os.Args[1], os.Args[2:], execRunner, os.Stdout, os.Stderr))
		case "version":
			fmt.Println("restmail", version)
			return
		case "help", "--help", "-h":
			fmt.Print(topUsage)
			return
		}
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
