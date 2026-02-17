package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

type statusTickMsg time.Time

type domainStatus struct {
	healthy  bool
	users    int
	messages int
}

// StatusModel tracks server status for the bottom bar.
type StatusModel struct {
	api      *apiclient.Client
	token    string
	statuses map[string]domainStatus
}

func NewStatusModel(api *apiclient.Client, token string) StatusModel {
	return StatusModel{
		api:   api,
		token: token,
		statuses: map[string]domainStatus{
			"mail1.test": {healthy: true},
			"mail2.test": {healthy: true},
			"mail3.test": {healthy: true},
		},
	}
}

func (m StatusModel) Init() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return statusTickMsg(t)
	})
}

func (m StatusModel) Update(msg tea.Msg) (StatusModel, tea.Cmd) {
	switch msg.(type) {
	case statusTickMsg:
		// Fetch live domain data
		if m.token != "" {
			resp, err := m.api.ListDomains(m.token)
			if err == nil {
				newStatuses := make(map[string]domainStatus)
				for _, d := range resp.Data {
					newStatuses[d.Name] = domainStatus{healthy: d.Active, users: 0, messages: 0}
				}
				m.statuses = newStatuses
			}
		}
		return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
			return statusTickMsg(t)
		})
	}
	return m, nil
}

func (m StatusModel) GetDomainStatus(domain string) domainStatus {
	if st, ok := m.statuses[domain]; ok {
		return st
	}
	return domainStatus{}
}
