package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

type inboxMessagesMsg struct {
	messages []apiclient.MessageSummary
	err      error
}

type inboxMessageDetailMsg struct {
	message *apiclient.MessageDetail
	err     error
}

// InboxModel handles browsing a user's inbox.
type InboxModel struct {
	api        *apiclient.Client
	adminToken string // admin token for browsing any mailbox

	// User selection
	selectingUser bool
	userInput     textinput.Model
	selectedUser  string
	token         string
	accountID     uint

	// Message list
	messages []apiclient.MessageSummary
	cursor   int
	loading  bool
	err      error

	// Reading a message
	readingMessage bool
	messageDetail  *apiclient.MessageDetail
	scrollOffset   int
}

func NewInboxModel(api *apiclient.Client, adminToken string) InboxModel {
	ui := textinput.New()
	ui.Placeholder = "user@domain.test"
	ui.CharLimit = 255
	ui.Width = 40

	return InboxModel{
		api:           api,
		adminToken:    adminToken,
		selectingUser: true,
		userInput:     ui,
	}
}

func (m InboxModel) Init() tea.Cmd {
	m.userInput.Focus()
	return textinput.Blink
}

func (m InboxModel) Update(msg tea.Msg) (InboxModel, tea.Cmd) {
	switch msg := msg.(type) {
	case inboxMessagesMsg:
		m.loading = false
		m.err = msg.err
		m.messages = msg.messages
		return m, nil

	case inboxMessageDetailMsg:
		m.loading = false
		m.err = msg.err
		if msg.message != nil {
			m.messageDetail = msg.message
			m.readingMessage = true
			m.scrollOffset = 0
		}
		return m, nil

	case tea.KeyMsg:
		if m.readingMessage {
			return m.updateReading(msg)
		}
		if m.selectingUser {
			return m.updateSelectUser(msg)
		}
		return m.updateMessageList(msg)
	}
	return m, nil
}

func (m InboxModel) updateSelectUser(msg tea.KeyMsg) (InboxModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		email := m.userInput.Value()
		if email == "" {
			return m, nil
		}
		m.selectedUser = email
		m.selectingUser = false
		m.loading = true
		return m, m.fetchMessages(email)
	}

	var cmd tea.Cmd
	m.userInput, cmd = m.userInput.Update(msg)
	return m, cmd
}

func (m InboxModel) updateMessageList(msg tea.KeyMsg) (InboxModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.messages)-1 {
			m.cursor++
		}
	case "enter":
		if len(m.messages) > 0 {
			msgID := m.messages[m.cursor].ID
			m.loading = true
			return m, m.fetchMessageDetail(msgID)
		}
	case "backspace":
		m.selectingUser = true
		m.messages = nil
		m.userInput.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m InboxModel) updateReading(msg tea.KeyMsg) (InboxModel, tea.Cmd) {
	switch msg.String() {
	case "esc", "backspace", "q":
		m.readingMessage = false
		m.messageDetail = nil
		return m, nil
	case "up", "k":
		if m.scrollOffset > 0 {
			m.scrollOffset--
		}
	case "down", "j":
		m.scrollOffset++
	}
	return m, nil
}

func (m InboxModel) fetchMessages(email string) tea.Cmd {
	return func() tea.Msg {
		// Use the admin token to browse any user's mailbox.
		// Look up the mailbox by address to find its account ID.
		token := m.adminToken
		if token == "" {
			return inboxMessagesMsg{err: fmt.Errorf("no admin token available")}
		}

		// Find the mailbox for this email via the admin mailbox list
		mbResp, err := m.api.ListMailboxes(token)
		if err != nil {
			return inboxMessagesMsg{err: fmt.Errorf("list mailboxes: %w", err)}
		}
		var accountID uint
		for _, mb := range mbResp.Data {
			if mb.Address == email {
				accountID = mb.ID
				break
			}
		}
		if accountID == 0 {
			return inboxMessagesMsg{err: fmt.Errorf("mailbox not found: %s", email)}
		}

		msgResp, err := m.api.ListMessages(token, accountID, "INBOX")
		if err != nil {
			return inboxMessagesMsg{err: err}
		}

		return inboxMessagesMsg{messages: msgResp.Data}
	}
}

func (m InboxModel) fetchMessageDetail(msgID uint) tea.Cmd {
	return func() tea.Msg {
		token := m.adminToken
		if token == "" {
			return inboxMessageDetailMsg{err: fmt.Errorf("no admin token available")}
		}

		detail, err := m.api.GetMessage(token, msgID)
		if err != nil {
			return inboxMessageDetailMsg{err: err}
		}

		return inboxMessageDetailMsg{message: &detail.Data}
	}
}

func (m InboxModel) View(width, height int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Inbox Browser") + "\n\n")

	if m.selectingUser {
		b.WriteString("  Enter user email to browse:\n\n")
		b.WriteString(fmt.Sprintf("    %s\n", m.userInput.View()))
		b.WriteString("\n" + helpStyle.Render("  enter: load inbox  esc: back"))
	} else if m.readingMessage && m.messageDetail != nil {
		msg := m.messageDetail
		b.WriteString(lipgloss.NewStyle().Bold(true).PaddingLeft(2).
			Render(fmt.Sprintf("  Subject: %s", msg.Subject)) + "\n")
		b.WriteString(mutedStyle.PaddingLeft(2).
			Render(fmt.Sprintf("  From: %s <%s>", msg.SenderName, msg.Sender)) + "\n")
		b.WriteString(mutedStyle.PaddingLeft(2).
			Render(fmt.Sprintf("  Date: %s", msg.ReceivedAt.Format("2006-01-02 15:04"))) + "\n")
		b.WriteString(lipgloss.NewStyle().PaddingLeft(2).Foreground(borderColor).
			Render(strings.Repeat("─", width-4)) + "\n")

		body := msg.BodyText
		if body == "" {
			body = "(no body)"
		}
		lines := strings.Split(body, "\n")
		maxLines := height - 10
		if maxLines < 5 {
			maxLines = 5
		}
		start := m.scrollOffset
		if start >= len(lines) {
			start = len(lines) - 1
		}
		if start < 0 {
			start = 0
		}
		end := start + maxLines
		if end > len(lines) {
			end = len(lines)
		}
		for _, line := range lines[start:end] {
			b.WriteString("  " + line + "\n")
		}

		b.WriteString("\n" + helpStyle.Render("  ↑/↓: scroll  esc: back to list"))
	} else if m.loading {
		b.WriteString(fmt.Sprintf("  Loading inbox for %s...\n", m.selectedUser))
	} else if m.err != nil {
		b.WriteString(fmt.Sprintf("  Error: %s\n", m.err))
		b.WriteString("\n" + helpStyle.Render("  backspace: back to user select  esc: back"))
	} else {
		b.WriteString(mutedStyle.PaddingLeft(2).Render(fmt.Sprintf("  Inbox: %s (%d messages)", m.selectedUser, len(m.messages))) + "\n\n")

		if len(m.messages) == 0 {
			b.WriteString("  (empty inbox)\n")
		} else {
			hdr := lipgloss.NewStyle().Bold(true).PaddingLeft(2).
				Render(fmt.Sprintf("  %-4s %-30s %-40s %s", "", "From", "Subject", "Date"))
			b.WriteString(hdr + "\n")

			for i, msg := range m.messages {
				cursor := "  "
				style := lipgloss.NewStyle().PaddingLeft(2)
				if i == m.cursor {
					cursor = "▸ "
					style = selectedStyle.PaddingLeft(2)
				}
				readMark := " "
				if !msg.IsRead {
					readMark = "●"
				}
				date := msg.ReceivedAt.Format("Jan 02 15:04")
				from := msg.SenderName
				if from == "" {
					from = msg.Sender
				}
				if len(from) > 28 {
					from = from[:28] + ".."
				}
				subj := msg.Subject
				if len(subj) > 38 {
					subj = subj[:38] + ".."
				}
				row := fmt.Sprintf("%-4s %-30s %-40s %s", readMark, from, subj, date)
				b.WriteString(style.Render(cursor+row) + "\n")
			}
		}

		b.WriteString("\n" + helpStyle.Render("  enter: read  backspace: change user  esc: back"))
	}

	s := b.String()
	lines := lipgloss.Height(s)
	for i := lines; i < height; i++ {
		s += "\n"
	}
	return s
}
