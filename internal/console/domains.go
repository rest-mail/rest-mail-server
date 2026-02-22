package console

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

type domainItem struct {
	id         uint
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
	token   string
	domains []domainItem
	cursor  int
	loading bool
	err     error

	// Add domain mode
	adding    bool
	nameInput textinput.Model
	typeInput textinput.Model
	addFocus  int // 0=name, 1=type
}

func NewDomainsModel(api *apiclient.Client, token string) DomainsModel {
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
		token:     token,
		nameInput: ni,
		typeInput: ti,
	}
}

func (m DomainsModel) Init() tea.Cmd {
	return m.loadDomains
}

func (m DomainsModel) loadDomains() tea.Msg {
	resp, err := m.api.ListDomains(m.token)
	if err != nil {
		return domainsLoadedMsg{err: err}
	}
	var items []domainItem
	for _, d := range resp.Data {
		items = append(items, domainItem{
			id:         d.ID,
			name:       d.Name,
			serverType: d.ServerType,
			active:     d.Active,
		})
	}
	return domainsLoadedMsg{domains: items}
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
				d := m.domains[m.cursor]
				return m, func() tea.Msg {
					err := m.api.DeleteDomain(m.token, d.id)
					return domainDeletedMsg{err: err}
				}
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
			name := m.nameInput.Value()
			stype := m.typeInput.Value()
			if stype == "" {
				stype = "traditional"
			}
			m.nameInput.Reset()
			m.typeInput.Reset()
			return m, func() tea.Msg {
				err := m.api.CreateDomain(m.token, name, stype)
				return domainCreatedMsg{err: err}
			}
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
