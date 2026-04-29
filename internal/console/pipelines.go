package console

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/restmail/restmail/internal/gateway/apiclient"
)

type pipelineItem struct {
	id        uint
	domainID  uint
	direction string
	filters   []string
	active    bool
}

type pipelinesLoadedMsg struct {
	pipelines []pipelineItem
	err       error
}

type pipelineToggledMsg struct {
	err error
}

// PipelinesModel handles the pipeline management view.
type PipelinesModel struct {
	api       *apiclient.Client
	token     string
	pipelines []pipelineItem
	cursor    int
	loading   bool
	err       error
}

func NewPipelinesModel(api *apiclient.Client, token string) PipelinesModel {
	return PipelinesModel{
		api:   api,
		token: token,
	}
}

func (m PipelinesModel) Init() tea.Cmd {
	return m.loadPipelines
}

func (m PipelinesModel) loadPipelines() tea.Msg {
	resp, err := m.api.ListPipelines(m.token, 0) // all domains
	if err != nil {
		return pipelinesLoadedMsg{err: err}
	}
	var items []pipelineItem
	for _, p := range resp.Data {
		filterNames := parseFilterNames(p.Filters)
		items = append(items, pipelineItem{
			id:        p.ID,
			domainID:  p.DomainID,
			direction: p.Direction,
			filters:   filterNames,
			active:    p.Active,
		})
	}
	return pipelinesLoadedMsg{pipelines: items}
}

func parseFilterNames(raw json.RawMessage) []string {
	var configs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &configs); err != nil {
		return nil
	}
	names := make([]string, len(configs))
	for i, c := range configs {
		names[i] = c.Name
	}
	return names
}

func (m PipelinesModel) Update(msg tea.Msg) (PipelinesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case pipelinesLoadedMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.pipelines = msg.pipelines
		}
		return m, nil

	case pipelineToggledMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadPipelines

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.pipelines)-1 {
				m.cursor++
			}
		case "t":
			// Toggle active/inactive
			if len(m.pipelines) > 0 {
				p := m.pipelines[m.cursor]
				return m, func() tea.Msg {
					err := m.api.TogglePipeline(m.token, p.id, !p.active)
					return pipelineToggledMsg{err: err}
				}
			}
		}
	}

	return m, nil
}

func (m PipelinesModel) View(width, height int) string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Pipelines") + "\n\n")

	if m.loading {
		b.WriteString("  Loading pipelines...\n")
	} else if m.err != nil {
		b.WriteString(fmt.Sprintf("  Error: %s\n", m.err))
	} else if len(m.pipelines) == 0 {
		b.WriteString("  No pipelines configured.\n")
	} else {
		// Table header
		hdr := lipgloss.NewStyle().Bold(true).PaddingLeft(2).
			Render(fmt.Sprintf("  %-6s %-10s %-10s %-8s %s", "ID", "Domain", "Direction", "Active", "Filters"))
		b.WriteString(hdr + "\n")
		b.WriteString(lipgloss.NewStyle().PaddingLeft(2).Foreground(borderColor).
			Render(strings.Repeat("─", min(width-4, 80))) + "\n")

		for i, p := range m.pipelines {
			cursor := "  "
			style := lipgloss.NewStyle().PaddingLeft(2)
			if i == m.cursor {
				cursor = "▸ "
				style = selectedStyle.PaddingLeft(2)
			}
			active := "yes"
			if !p.active {
				active = "no"
			}
			filterStr := strings.Join(p.filters, ", ")
			maxFilters := width - 50
			if maxFilters < 20 {
				maxFilters = 20
			}
			if len(filterStr) > maxFilters {
				filterStr = filterStr[:maxFilters-3] + "..."
			}
			row := fmt.Sprintf("%-6d %-10d %-10s %-8s %s", p.id, p.domainID, p.direction, active, filterStr)
			b.WriteString(style.Render(cursor+row) + "\n")
		}
	}

	b.WriteString("\n" + helpStyle.Render("  t: toggle active  esc: back"))

	// Pad height
	s := b.String()
	lines := lipgloss.Height(s)
	for i := lines; i < height; i++ {
		s += "\n"
	}
	return s
}
