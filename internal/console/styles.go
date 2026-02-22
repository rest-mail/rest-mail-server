package console

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	primaryColor   = lipgloss.Color("#2563EB")
	successColor   = lipgloss.Color("#16A34A")
	dangerColor    = lipgloss.Color("#DC2626")
	mutedColor     = lipgloss.Color("#6B7280")
	borderColor    = lipgloss.Color("#374151")
	highlightColor = lipgloss.Color("#3B82F6")

	// Styles
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(primaryColor).
			PaddingLeft(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("#1F2937")).
			Foreground(lipgloss.Color("#F9FAFB")).
			Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(borderColor)

	statusColumnStyle = lipgloss.NewStyle().
				Padding(0, 1).
				Border(lipgloss.NormalBorder(), false, true, false, false).
				BorderForeground(borderColor)

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(highlightColor)

	mutedStyle = lipgloss.NewStyle().
			Foreground(mutedColor)

	successDot = lipgloss.NewStyle().Foreground(successColor).Render("●")
	dangerDot  = lipgloss.NewStyle().Foreground(dangerColor).Render("●")

	helpStyle = lipgloss.NewStyle().
			Foreground(mutedColor).
			PaddingLeft(1)
)
