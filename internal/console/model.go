package console

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// View represents the current active view
type View int

const (
	ViewMain View = iota
	ViewDomains
	ViewUsers
	ViewInbox
	ViewReadMessage
	ViewCompose
	ViewPipelines
)

// Model is the top-level Bubble Tea model.
type Model struct {
	api    *apiclient.Client
	token  string
	width  int
	height int

	// Navigation
	view View

	// Sub-models
	domains   DomainsModel
	users     UsersModel
	inbox     InboxModel
	compose   ComposeModel
	pipelines PipelinesModel
	status    StatusModel

	// Main menu
	menuItems []string
	menuIdx   int

	// RBAC
	capabilities []string
	isSuperAdmin bool
	username     string
}

// NewModel creates the root console model.
func NewModel(api *apiclient.Client, token string) Model {
	// Parse JWT token to extract capabilities
	capabilities, isSuperAdmin, username := parseTokenCapabilities(token)

	return Model{
		api:           api,
		token:         token,
		capabilities:  capabilities,
		isSuperAdmin:  isSuperAdmin,
		username:      username,
		menuItems: []string{
			"Domains     - Manage mail domains",
			"Users       - Manage mailboxes and users",
			"Inbox       - Browse a user's inbox",
			"Compose     - Send mail as any user",
			"Pipelines   - View and manage filter pipelines",
		},
		menuIdx:   0,
		domains:   NewDomainsModel(api, token),
		users:     NewUsersModel(api, token),
		inbox:     NewInboxModel(api, token),
		compose:   NewComposeModel(api, token),
		pipelines: NewPipelinesModel(api, token),
		status:    NewStatusModel(api, token),
	}
}

// parseTokenCapabilities extracts capabilities from the JWT token
func parseTokenCapabilities(tokenStr string) (capabilities []string, isSuperAdmin bool, username string) {
	if tokenStr == "" {
		return nil, false, "anonymous"
	}

	// Parse without validation (we're just reading claims, API already validated it)
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		return nil, false, "unknown"
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, false, "unknown"
	}

	// Extract username
	if un, ok := claims["username"].(string); ok {
		username = un
	} else if email, ok := claims["email"].(string); ok {
		username = email
	}

	// Extract capabilities
	if caps, ok := claims["capabilities"].([]interface{}); ok {
		for _, cap := range caps {
			if capStr, ok := cap.(string); ok {
				capabilities = append(capabilities, capStr)
				if capStr == "*" {
					isSuperAdmin = true
				}
			}
		}
	}

	return capabilities, isSuperAdmin, username
}

func (m Model) Init() tea.Cmd {
	return m.status.Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.view == ViewMain {
				return m, tea.Quit
			}
		case "esc":
			if m.view != ViewMain {
				m.view = ViewMain
				return m, nil
			}
			return m, tea.Quit
		}
	case statusTickMsg:
		var cmd tea.Cmd
		m.status, cmd = m.status.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)
	}

	// Route to active view
	switch m.view {
	case ViewMain:
		return m.updateMain(msg)
	case ViewDomains:
		var cmd tea.Cmd
		m.domains, cmd = m.domains.Update(msg)
		cmds = append(cmds, cmd)
	case ViewUsers:
		var cmd tea.Cmd
		m.users, cmd = m.users.Update(msg)
		cmds = append(cmds, cmd)
	case ViewInbox:
		var cmd tea.Cmd
		m.inbox, cmd = m.inbox.Update(msg)
		cmds = append(cmds, cmd)
		if m.inbox.readingMessage {
			m.view = ViewReadMessage
		}
	case ViewReadMessage:
		var cmd tea.Cmd
		m.inbox, cmd = m.inbox.Update(msg)
		cmds = append(cmds, cmd)
		if !m.inbox.readingMessage {
			m.view = ViewInbox
		}
	case ViewCompose:
		var cmd tea.Cmd
		m.compose, cmd = m.compose.Update(msg)
		cmds = append(cmds, cmd)
		if m.compose.sent {
			m.view = ViewMain
			m.compose = NewComposeModel(m.api, m.token)
		}
	case ViewPipelines:
		var cmd tea.Cmd
		m.pipelines, cmd = m.pipelines.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) updateMain(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.menuIdx > 0 {
				m.menuIdx--
			}
		case "down", "j":
			if m.menuIdx < len(m.menuItems)-1 {
				m.menuIdx++
			}
		case "enter":
			switch m.menuIdx {
			case 0:
				m.view = ViewDomains
				return m, m.domains.Init()
			case 1:
				m.view = ViewUsers
				return m, m.users.Init()
			case 2:
				m.view = ViewInbox
				return m, m.inbox.Init()
			case 3:
				m.view = ViewCompose
				return m, m.compose.Init()
			case 4:
				m.view = ViewPipelines
				return m, m.pipelines.Init()
			}
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Header
	header := headerStyle.Width(m.width).Render(
		fmt.Sprintf("  rest-mail admin%s",
			lipgloss.NewStyle().Foreground(mutedColor).Render("                                              esc: back  q: quit")),
	)

	// Status bar (3 columns)
	statusBar := m.renderStatusBar()

	// Content height = total - header(1) - status bar(~4) - borders
	contentHeight := m.height - 2 - lipgloss.Height(statusBar)
	if contentHeight < 3 {
		contentHeight = 3
	}

	// Main content
	var content string
	switch m.view {
	case ViewMain:
		content = m.renderMainMenu(contentHeight)
	case ViewDomains:
		content = m.domains.View(m.width, contentHeight)
	case ViewUsers:
		content = m.users.View(m.width, contentHeight)
	case ViewInbox, ViewReadMessage:
		content = m.inbox.View(m.width, contentHeight)
	case ViewCompose:
		content = m.compose.View(m.width, contentHeight)
	case ViewPipelines:
		content = m.pipelines.View(m.width, contentHeight)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		content,
		statusBar,
	)
}

func (m Model) renderMainMenu(height int) string {
	s := "\n"
	title := "What would you like to do?"
	if !m.isSuperAdmin && len(m.capabilities) > 0 {
		capsPreview := strings.Join(m.capabilities[:min(3, len(m.capabilities))], ", ")
		if len(m.capabilities) > 3 {
			capsPreview += ", ..."
		}
		title += "\n" + lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Render(fmt.Sprintf("Capabilities: %s", capsPreview))
	}
	s += titleStyle.Render(title) + "\n\n"

	for i, item := range m.menuItems {
		cursor := "  "
		style := lipgloss.NewStyle().PaddingLeft(2)
		if i == m.menuIdx {
			cursor = "▸ "
			style = selectedStyle.PaddingLeft(2)
		}
		s += style.Render(cursor+item) + "\n"
	}

	s += "\n" + helpStyle.Render("↑/↓: navigate  enter: select  q: quit")

	// Pad to fill height
	lines := lipgloss.Height(s)
	for i := lines; i < height; i++ {
		s += "\n"
	}

	return s
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m Model) renderStatusBar() string {
	colWidth := m.width / 3
	if colWidth < 20 {
		colWidth = 20
	}

	domains := m.domains.domains
	if len(domains) == 0 {
		return statusBarStyle.Width(m.width).Render("  No domains loaded")
	}

	// Show up to 3 domains
	maxCols := 3
	if len(domains) < maxCols {
		maxCols = len(domains)
	}
	cols := make([]string, maxCols)
	for i := 0; i < maxCols; i++ {
		d := domains[i]
		st := m.status.GetDomainStatus(d.name)
		dot := successDot
		statusText := "ok"
		if !st.healthy {
			dot = dangerDot
			statusText = "unreachable"
		}
		msgStr := "n/a"
		if st.messages > 0 {
			msgStr = fmt.Sprintf("%d", st.messages)
		}
		col := fmt.Sprintf(" %s (%s)\n %s %d users\n %s %s messages\n %s status: %s",
			d.name, d.serverType,
			dot, st.users,
			dot, msgStr,
			dot, statusText,
		)
		cols[i] = statusColumnStyle.Width(colWidth).Render(col)
	}

	domainRow := lipgloss.JoinHorizontal(lipgloss.Top, cols...)

	// Global stats row
	queueStr := fmt.Sprintf("Queue: %d pending", m.status.QueueDepth())
	banStr := fmt.Sprintf("Bans: %d active", m.status.ActiveBans())

	// Add user info and capability warning
	userInfo := fmt.Sprintf("User: %s", m.username)
	if !m.isSuperAdmin {
		warning := lipgloss.NewStyle().
			Foreground(lipgloss.Color("202")). // Orange/warning color
			Render("⚠ Limited Access")
		userInfo = fmt.Sprintf("%s  |  %s", userInfo, warning)
	}

	globalRow := fmt.Sprintf("  %s  |  %s  |  %s", queueStr, banStr, userInfo)

	return statusBarStyle.Width(m.width).Render(
		domainRow + "\n" + globalRow,
	)
}
