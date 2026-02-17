package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/tui"
)

func main() {
	apiURL := flag.String("api", "http://localhost:8080", "REST API base URL")
	adminEmail := flag.String("user", "", "Admin email for login (or set RESTMAIL_ADMIN_EMAIL)")
	adminPass := flag.String("pass", "", "Admin password (or set RESTMAIL_ADMIN_PASSWORD)")
	flag.Parse()

	// Prefer flags, fall back to env vars
	email := *adminEmail
	if email == "" {
		email = os.Getenv("RESTMAIL_ADMIN_EMAIL")
	}
	pass := *adminPass
	if pass == "" {
		pass = os.Getenv("RESTMAIL_ADMIN_PASSWORD")
	}

	client := apiclient.New(*apiURL)

	// Login as admin to get a token for API calls
	token := ""
	if email != "" && pass != "" {
		loginResp, err := client.Login(email, pass)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: admin login failed: %v\n", err)
		} else {
			token = loginResp.Data.AccessToken
		}
	} else {
		fmt.Fprintln(os.Stderr, "hint: set -user/-pass flags or RESTMAIL_ADMIN_EMAIL/RESTMAIL_ADMIN_PASSWORD env vars for admin access")
	}

	model := tui.NewModel(client, token)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
