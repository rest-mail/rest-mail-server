package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

type domainItem struct {
	name       string
	serverType string
	active     bool
}

type domainsLoadedMsg struct {
	domains []domainItem
	err     error
}

type domainDeletedMsg struct {
	err error
}

type domainCreatedMsg struct {
	err error
}

// DomainsModel handles the domains management view.
type DomainsModel struct {
	api     *apiclient.Client
	domains []domainItem
	cursor  int
	loading bool
	err     error

	// Add domain mode
	adding   bool
	nameInput textinput.Model
	typeInput textinput.Model
	addFocus  int // 0=name, 1=type
}

func NewDomainsModel(api *apiclient.Client) DomainsModel {
	ni := textinput.New()
	ni.Placeholder = "example.com"
	ni.CharLimit = 253
	ni.Width = 40

	ti := textinput.New()
	ti.Placeholder = "traditional or restmail"
	ti.CharLimit = 20
	ti.Width = 40

	return DomainsModel{
		api:       api,
		nameInput: ni,
		typeInput: ti,
	}
}

func (m DomainsModel) Init() tea.Cmd {
	return m.loadDomains
}

func (m DomainsModel) loadDomains() tea.Msg {
	// TODO: Call admin API to list domains
	// For now, return hardcoded defaults
	return domainsLoadedMsg{
		domains: []domainItem{
			{name: "mail1.test", serverType: "traditional", active: true},
			{name: "mail2.test", serverType: "traditional", active: true},
			{name: "mail3.test", serverType: "restmail", active: true},
		},
	}
}

func (m DomainsModel) Update(msg tea.Msg) (DomainsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case domainsLoadedMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.domains = msg.domains
		}
		return m, nil

	case domainDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadDomains

	case domainCreatedMsg:
		m.adding = false
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadDomains

	case tea.KeyMsg:
		if m.adding {
			return m.updateAdding(msg)
		}

		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.domains)-1 {
				m.cursor++
			}
		case "a":
			m.adding = true
			m.nameInput.Focus()
			return m, textinput.Blink
		case "d", "delete":
			if len(m.domains) > 0 {
				// TODO: Call delete API
				return m, nil
			}
		}
	}

	return m, nil
}

func (m DomainsModel) updateAdding(msg tea.KeyMsg) (DomainsModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.adding = false
		m.nameInput.Reset()
		m.typeInput.Reset()
		return m, nil
	case "tab":
		if m.addFocus == 0 {
			m.addFocus = 1
			m.nameInput.Blur()
			m.typeInput.Focus()
		} else {
			m.addFocus = 0
			m.typeInput.Blur()
			m.nameInput.Focus()
		}
		return m, textinput.Blink
	case "enter":
		if m.addFocus == 1 && m.nameInput.Value() != "" {
			// TODO: Call create API
			m.adding = false
			name := m.nameInput.Value()
			stype := m.typeInput.Value()
			if stype == "" {
				stype = "traditional"
			}
			m.domains = append(m.domains, domainItem{name: name, serverType: stype, active: true})
			m.nameInput.Reset()
			m.typeInput.Reset()
			return m, nil
		}
		// Move to next field
		m.addFocus = 1
		m.nameInput.Blur()
		m.typeInput.Focus()
		return m, textinput.Blink
	}

	var cmd tea.Cmd
	if m.addFocus == 0 {
		m.nameInput, cmd = m.nameInput.Update(msg)
	} else {
		m.typeInput, cmd = m.typeInput.Update(msg)
	}
	return m, cmd
}

func (m DomainsModel) View(width, height int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Domains") + "\n\n")

	if m.loading {
		b.WriteString("  Loading domains...\n")
	} else if m.err != nil {
		b.WriteString(fmt.Sprintf("  Error: %s\n", m.err))
	} else {
		// Table header
		hdr := lipgloss.NewStyle().Bold(true).PaddingLeft(2).
			Render(fmt.Sprintf("  %-30s %-15s %-8s", "Domain", "Type", "Active"))
		b.WriteString(hdr + "\n")
		b.WriteString(lipgloss.NewStyle().PaddingLeft(2).Foreground(borderColor).
			Render(strings.Repeat("─", 60)) + "\n")

		for i, d := range m.domains {
			cursor := "  "
			style := lipgloss.NewStyle().PaddingLeft(2)
			if i == m.cursor {
				cursor = "▸ "
				style = selectedStyle.PaddingLeft(2)
			}
			active := "yes"
			if !d.active {
				active = "no"
			}
			row := fmt.Sprintf("%-30s %-15s %-8s", d.name, d.serverType, active)
			b.WriteString(style.Render(cursor+row) + "\n")
		}
	}

	if m.adding {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().PaddingLeft(2).Bold(true).Render("  Add Domain") + "\n")
		b.WriteString(fmt.Sprintf("    Name: %s\n", m.nameInput.View()))
		b.WriteString(fmt.Sprintf("    Type: %s\n", m.typeInput.View()))
		b.WriteString(helpStyle.Render("  tab: next field  enter: save  esc: cancel") + "\n")
	} else {
		b.WriteString("\n" + helpStyle.Render("  a: add  d: delete  esc: back"))
	}

	// Pad height
	s := b.String()
	lines := lipgloss.Height(s)
	for i := lines; i < height; i++ {
		s += "\n"
	}
	return s
}
