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
	flag.Parse()

	client := apiclient.New(*apiURL)

	// Login as admin to get a token for API calls
	loginResp, err := client.Login("admin@mail3.test", "password123")
	token := ""
	if err == nil {
		token = loginResp.Data.AccessToken
	}

	model := tui.NewModel(client, token)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
