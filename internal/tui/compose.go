package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

type composeSentMsg struct {
	err error
}

// ComposeModel handles composing and sending mail from the TUI.
type ComposeModel struct {
	api *apiclient.Client

	fromInput    textinput.Model
	toInput      textinput.Model
	subjectInput textinput.Model
	bodyArea     textarea.Model

	focusIdx int // 0=from, 1=to, 2=subject, 3=body
	sending  bool
	sent     bool
	err      error
}

func NewComposeModel(api *apiclient.Client) ComposeModel {
	from := textinput.New()
	from.Placeholder = "sender@domain.test"
	from.CharLimit = 255
	from.Width = 50

	to := textinput.New()
	to.Placeholder = "recipient@domain.test"
	to.CharLimit = 255
	to.Width = 50

	subj := textinput.New()
	subj.Placeholder = "Subject"
	subj.CharLimit = 255
	subj.Width = 50

	body := textarea.New()
	body.Placeholder = "Compose your message here..."
	body.CharLimit = 10000
	body.SetWidth(60)
	body.SetHeight(10)
	body.ShowLineNumbers = false

	return ComposeModel{
		api:          api,
		fromInput:    from,
		toInput:      to,
		subjectInput: subj,
		bodyArea:     body,
	}
}

func (m ComposeModel) Init() tea.Cmd {
	m.fromInput.Focus()
	return textinput.Blink
}

func (m ComposeModel) Update(msg tea.Msg) (ComposeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case composeSentMsg:
		m.sending = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.sent = true
		}
		return m, nil

	case tea.KeyMsg:
		return m.updateKey(msg)
	}

	// Pass non-key messages (e.g. blink) to the active input
	var cmd tea.Cmd
	switch m.focusIdx {
	case 0:
		m.fromInput, cmd = m.fromInput.Update(msg)
	case 1:
		m.toInput, cmd = m.toInput.Update(msg)
	case 2:
		m.subjectInput, cmd = m.subjectInput.Update(msg)
	case 3:
		m.bodyArea, cmd = m.bodyArea.Update(msg)
	}
	return m, cmd
}

func (m ComposeModel) updateKey(msg tea.KeyMsg) (ComposeModel, tea.Cmd) {
	switch msg.String() {
	case "tab":
		m.focusIdx = (m.focusIdx + 1) % 4
		return m, m.updateFocus()
	case "shift+tab":
		m.focusIdx = (m.focusIdx + 3) % 4 // -1 mod 4
		return m, m.updateFocus()
	case "enter":
		if m.focusIdx < 3 {
			// Advance to next field on enter for header fields
			m.focusIdx++
			return m, m.updateFocus()
		}
		// When focused on body textarea, let enter pass through for newlines
		var cmd tea.Cmd
		m.bodyArea, cmd = m.bodyArea.Update(msg)
		return m, cmd
	case "ctrl+s":
		return m.attemptSend()
	}

	// Update the active input
	var cmd tea.Cmd
	switch m.focusIdx {
	case 0:
		m.fromInput, cmd = m.fromInput.Update(msg)
	case 1:
		m.toInput, cmd = m.toInput.Update(msg)
	case 2:
		m.subjectInput, cmd = m.subjectInput.Update(msg)
	case 3:
		m.bodyArea, cmd = m.bodyArea.Update(msg)
	}
	return m, cmd
}

func (m ComposeModel) updateFocus() tea.Cmd {
	m.fromInput.Blur()
	m.toInput.Blur()
	m.subjectInput.Blur()
	m.bodyArea.Blur()

	switch m.focusIdx {
	case 0:
		m.fromInput.Focus()
		return textinput.Blink
	case 1:
		m.toInput.Focus()
		return textinput.Blink
	case 2:
		m.subjectInput.Focus()
		return textinput.Blink
	case 3:
		m.bodyArea.Focus()
		return textarea.Blink
	}
	return nil
}

func (m ComposeModel) attemptSend() (ComposeModel, tea.Cmd) {
	from := m.fromInput.Value()
	to := m.toInput.Value()
	subj := m.subjectInput.Value()
	body := m.bodyArea.Value()

	if from == "" || to == "" {
		m.err = fmt.Errorf("from and to are required")
		return m, nil
	}

	m.sending = true
	m.err = nil
	return m, func() tea.Msg {
		req := &apiclient.DeliverRequest{
			Address:  to,
			Sender:   from,
			Subject:  subj,
			BodyText: body,
		}
		_, err := m.api.DeliverMessage(req)
		return composeSentMsg{err: err}
	}
}

func (m ComposeModel) View(width, height int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Compose Message") + "\n\n")

	if m.sending {
		b.WriteString("  Sending message...\n")
	} else if m.sent {
		b.WriteString("  Message sent successfully!\n")
		b.WriteString("\n" + helpStyle.Render("  esc: back to menu"))
	} else {
		if m.err != nil {
			b.WriteString(lipgloss.NewStyle().Foreground(dangerColor).PaddingLeft(2).
				Render(fmt.Sprintf("  Error: %s", m.err)) + "\n\n")
		}

		b.WriteString(fmt.Sprintf("    From:    %s\n", m.fromInput.View()))
		b.WriteString(fmt.Sprintf("    To:      %s\n", m.toInput.View()))
		b.WriteString(fmt.Sprintf("    Subject: %s\n", m.subjectInput.View()))
		b.WriteString(lipgloss.NewStyle().PaddingLeft(2).Foreground(borderColor).
			Render(strings.Repeat("─", width-4)) + "\n")

		// Calculate remaining height for the textarea
		// Header lines: title(2) + error(0-2) + from(1) + to(1) + subject(1) + separator(1) + label(1) + help(2) = ~9-11
		usedLines := 11
		if m.err != nil {
			usedLines += 2
		}
		bodyHeight := height - usedLines
		if bodyHeight < 3 {
			bodyHeight = 3
		}

		// Resize the textarea to fill remaining space
		m.bodyArea.SetWidth(width - 8)
		m.bodyArea.SetHeight(bodyHeight)

		b.WriteString("    Body:\n")
		b.WriteString("    " + strings.ReplaceAll(m.bodyArea.View(), "\n", "\n    ") + "\n")
		b.WriteString("\n" + helpStyle.Render("  tab: next field  enter: newline (in body)  ctrl+s: send  esc: back"))
	}

	s := b.String()
	lines := lipgloss.Height(s)
	for i := lines; i < height; i++ {
		s += "\n"
	}
	return s
}
