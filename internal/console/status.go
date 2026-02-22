package console

import (
	"strings"
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
	api        *apiclient.Client
	token      string
	statuses   map[string]domainStatus
	queueDepth int64
	activeBans int64
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
					newStatuses[d.Name] = domainStatus{healthy: d.Active}
				}

				// Count mailboxes per domain
				mbResp, err := m.api.ListMailboxes(m.token)
				if err == nil {
					for _, mb := range mbResp.Data {
						parts := strings.SplitN(mb.Address, "@", 2)
						if len(parts) == 2 {
							if st, ok := newStatuses[parts[1]]; ok {
								st.users++
								newStatuses[parts[1]] = st
							}
						}
					}
				}

				m.statuses = newStatuses
			}

			// Fetch queue stats
			queueResp, err := m.api.QueueStats(m.token)
			if err == nil {
				m.queueDepth = queueResp.Data.Pending
			}

			// Fetch active ban count
			banResp, err := m.api.ListBans(m.token)
			if err == nil && banResp.Pagination != nil {
				m.activeBans = banResp.Pagination.Total
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

// QueueDepth returns the current pending queue items.
func (m StatusModel) QueueDepth() int64 { return m.queueDepth }

// ActiveBans returns the count of active IP bans.
func (m StatusModel) ActiveBans() int64 { return m.activeBans }
