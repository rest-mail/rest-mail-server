package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/restmail/restmail/internal/console"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

func main() {
	apiURL := flag.String("api", "http://localhost:8080", "REST API base URL")
	adminUser := flag.String("user", "", "Admin username for login (or set RESTMAIL_ADMIN_USERNAME)")
	adminPass := flag.String("pass", "", "Admin password (or set RESTMAIL_ADMIN_PASSWORD)")
	flag.Parse()

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
