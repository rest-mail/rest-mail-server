package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

type userItem struct {
	email       string
	displayName string
	domain      string
}

type usersLoadedMsg struct {
	users []userItem
	err   error
}

// UsersModel handles user/mailbox management.
type UsersModel struct {
	api    *apiclient.Client
	users  []userItem
	cursor int
	loading bool
	err     error

	// Create user mode
	creating     bool
	emailInput   textinput.Model
	nameInput    textinput.Model
	passInput    textinput.Model
	createFocus  int
}

func NewUsersModel(api *apiclient.Client) UsersModel {
	ei := textinput.New()
	ei.Placeholder = "user@domain.test"
	ei.CharLimit = 255
	ei.Width = 40

	ni := textinput.New()
	ni.Placeholder = "Display Name"
	ni.CharLimit = 100
	ni.Width = 40

	pi := textinput.New()
	pi.Placeholder = "password"
	pi.EchoMode = textinput.EchoPassword
	pi.CharLimit = 100
	pi.Width = 40

	return UsersModel{
		api:        api,
		emailInput: ei,
		nameInput:  ni,
		passInput:  pi,
	}
}

func (m UsersModel) Init() tea.Cmd {
	return m.loadUsers
}

func (m UsersModel) loadUsers() tea.Msg {
	// TODO: Call admin API to list mailboxes
	return usersLoadedMsg{
		users: []userItem{
			{email: "alice@mail1.test", displayName: "Alice", domain: "mail1.test"},
			{email: "bob@mail1.test", displayName: "Bob", domain: "mail1.test"},
			{email: "charlie@mail2.test", displayName: "Charlie", domain: "mail2.test"},
			{email: "diana@mail2.test", displayName: "Diana", domain: "mail2.test"},
			{email: "eve@mail3.test", displayName: "Eve", domain: "mail3.test"},
			{email: "frank@mail3.test", displayName: "Frank", domain: "mail3.test"},
		},
	}
}

func (m UsersModel) Update(msg tea.Msg) (UsersModel, tea.Cmd) {
	switch msg := msg.(type) {
	case usersLoadedMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.users = msg.users
		}
		return m, nil

	case tea.KeyMsg:
		if m.creating {
			return m.updateCreating(msg)
		}

		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.users)-1 {
				m.cursor++
			}
		case "c":
			m.creating = true
			m.createFocus = 0
			m.emailInput.Focus()
			return m, textinput.Blink
		case "d", "delete":
			// TODO: Delete user via API
			if len(m.users) > 0 {
				m.users = append(m.users[:m.cursor], m.users[m.cursor+1:]...)
				if m.cursor >= len(m.users) && m.cursor > 0 {
					m.cursor--
				}
			}
		case "r":
			// TODO: Reset password via API
		}
	}
	return m, nil
}

func (m UsersModel) updateCreating(msg tea.KeyMsg) (UsersModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.creating = false
		m.emailInput.Reset()
		m.nameInput.Reset()
		m.passInput.Reset()
		return m, nil
	case "tab":
		m.createFocus = (m.createFocus + 1) % 3
		m.emailInput.Blur()
		m.nameInput.Blur()
		m.passInput.Blur()
		switch m.createFocus {
		case 0:
			m.emailInput.Focus()
		case 1:
			m.nameInput.Focus()
		case 2:
			m.passInput.Focus()
		}
		return m, textinput.Blink
	case "enter":
		if m.createFocus == 2 && m.emailInput.Value() != "" {
			// TODO: Call create API
			email := m.emailInput.Value()
			name := m.nameInput.Value()
			parts := strings.SplitN(email, "@", 2)
			domain := ""
			if len(parts) > 1 {
				domain = parts[1]
			}
			m.users = append(m.users, userItem{email: email, displayName: name, domain: domain})
			m.creating = false
			m.emailInput.Reset()
			m.nameInput.Reset()
			m.passInput.Reset()
			return m, nil
		}
		// Advance to next field
		m.createFocus = (m.createFocus + 1) % 3
		m.emailInput.Blur()
		m.nameInput.Blur()
		m.passInput.Blur()
		switch m.createFocus {
		case 0:
			m.emailInput.Focus()
		case 1:
			m.nameInput.Focus()
		case 2:
			m.passInput.Focus()
		}
		return m, textinput.Blink
	}

	var cmd tea.Cmd
	switch m.createFocus {
	case 0:
		m.emailInput, cmd = m.emailInput.Update(msg)
	case 1:
		m.nameInput, cmd = m.nameInput.Update(msg)
	case 2:
		m.passInput, cmd = m.passInput.Update(msg)
	}
	return m, cmd
}

func (m UsersModel) View(width, height int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Users / Mailboxes") + "\n\n")

	if m.loading {
		b.WriteString("  Loading users...\n")
	} else if m.err != nil {
		b.WriteString(fmt.Sprintf("  Error: %s\n", m.err))
	} else {
		hdr := lipgloss.NewStyle().Bold(true).PaddingLeft(2).
			Render(fmt.Sprintf("  %-35s %-20s %-15s", "Email", "Name", "Domain"))
		b.WriteString(hdr + "\n")
		b.WriteString(lipgloss.NewStyle().PaddingLeft(2).Foreground(borderColor).
			Render(strings.Repeat("─", 75)) + "\n")

		for i, u := range m.users {
			cursor := "  "
			style := lipgloss.NewStyle().PaddingLeft(2)
			if i == m.cursor {
				cursor = "▸ "
				style = selectedStyle.PaddingLeft(2)
			}
			row := fmt.Sprintf("%-35s %-20s %-15s", u.email, u.displayName, u.domain)
			b.WriteString(style.Render(cursor+row) + "\n")
		}
	}

	if m.creating {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().PaddingLeft(2).Bold(true).Render("  Create User") + "\n")
		b.WriteString(fmt.Sprintf("    Email:    %s\n", m.emailInput.View()))
		b.WriteString(fmt.Sprintf("    Name:     %s\n", m.nameInput.View()))
		b.WriteString(fmt.Sprintf("    Password: %s\n", m.passInput.View()))
		b.WriteString(helpStyle.Render("  tab: next field  enter: save  esc: cancel") + "\n")
	} else {
		b.WriteString("\n" + helpStyle.Render("  c: create  d: delete  r: reset password  esc: back"))
	}

	s := b.String()
	lines := lipgloss.Height(s)
	for i := lines; i < height; i++ {
		s += "\n"
	}
	return s
}
