package mailcheck

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#5B21B6")).
			Padding(0, 2)

	categoryStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#A78BFA")).
			MarginTop(1)

	passStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22C55E")).
			Bold(true)

	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EAB308")).
			Bold(true)

	failStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444")).
			Bold(true)

	skipStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F97316")).
			Bold(true)

	detailStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CA3AF")).
			PaddingLeft(4)

	fixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#38BDF8")).
			PaddingLeft(2)

	scoreGoodStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#22C55E"))

	scoreOkStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#EAB308"))

	scoreBadStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#EF4444"))

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).
			MarginTop(1)

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#5B21B6")).
			Padding(0, 1)
)

// statusIcon returns a colored status indicator.
func statusIcon(s Status) string {
	switch s {
	case StatusPass:
		return passStyle.Render("[PASS]")
	case StatusWarn:
		return warnStyle.Render("[WARN]")
	case StatusFail:
		return failStyle.Render("[FAIL]")
	case StatusSkip:
		return skipStyle.Render("[SKIP]")
	case StatusError:
		return errorStyle.Render("[ERR] ")
	default:
		return "[????]"
	}
}

// categoryName returns a human-readable category name.
func categoryName(cat string) string {
	switch cat {
	case "dns":
		return "DNS Records"
	case "smtp":
		return "SMTP / Submission"
	case "tls":
		return "TLS Certificates"
	case "imap":
		return "IMAP"
	case "pop3":
		return "POP3"
	case "reputation":
		return "Reputation"
	case "security":
		return "Security"
	case "headers":
		return "Header Analysis"
	case "roundtrip":
		return "Deliverability"
	case "skip":
		return "Skipped"
	default:
		return strings.ToUpper(cat)
	}
}

// DisplayReport prints the report to the terminal with colors.
func DisplayReport(report *Report, verbose bool) {
	fmt.Println()
	fmt.Println(titleStyle.Render(fmt.Sprintf(" Instant Mail Check — %s ", report.Domain)))
	fmt.Println()

	// Group checks by category
	categories := []string{"dns", "smtp", "tls", "imap", "pop3", "security", "reputation", "roundtrip", "headers", "skip"}
	grouped := make(map[string][]CheckResult)
	for _, c := range report.Checks {
		grouped[c.Category] = append(grouped[c.Category], c)
	}

	for _, cat := range categories {
		checks, ok := grouped[cat]
		if !ok || len(checks) == 0 {
			continue
		}

		fmt.Println(categoryStyle.Render(fmt.Sprintf("── %s ──", categoryName(cat))))

		for _, c := range checks {
			duration := ""
			if c.Duration > 0 {
				duration = fmt.Sprintf(" (%s)", formatDuration(c.Duration))
			}

			fmt.Printf("  %s  %-28s %s%s\n",
				statusIcon(c.Status),
				c.Name,
				c.Summary,
				skipStyle.Render(duration),
			)

			if verbose && c.Detail != "" {
				for _, line := range strings.Split(c.Detail, "\n") {
					fmt.Println(detailStyle.Render(line))
				}
			}

			if c.Fix != "" && (c.Status == StatusFail || c.Status == StatusWarn) {
				fmt.Println(fixStyle.Render("  ↳ Fix: " + c.Fix))
			}
		}
	}

	// Score
	fmt.Println()
	pct := report.Percentage()
	var scoreStr string
	if pct >= 80 {
		scoreStr = scoreGoodStyle.Render(fmt.Sprintf("%d/%d (%d%%)", report.Score, report.MaxScore, pct))
	} else if pct >= 50 {
		scoreStr = scoreOkStyle.Render(fmt.Sprintf("%d/%d (%d%%)", report.Score, report.MaxScore, pct))
	} else {
		scoreStr = scoreBadStyle.Render(fmt.Sprintf("%d/%d (%d%%)", report.Score, report.MaxScore, pct))
	}

	fmt.Println(borderStyle.Render(fmt.Sprintf("  Score: %s", scoreStr)))

	// Summary counts
	var pass, warn, fail, skip int
	for _, c := range report.Checks {
		switch c.Status {
		case StatusPass:
			pass++
		case StatusWarn:
			warn++
		case StatusFail:
			fail++
		case StatusSkip, StatusError:
			skip++
		}
	}
	fmt.Printf("  %s %d passed  %s %d warnings  %s %d failed  %s %d skipped\n",
		passStyle.Render("●"), pass,
		warnStyle.Render("●"), warn,
		failStyle.Render("●"), fail,
		skipStyle.Render("●"), skip,
	)

	// Footer
	fmt.Println()
	fmt.Println(footerStyle.Render("─────────────────────────────────────────────────"))
	fmt.Println(footerStyle.Render("  Instant Mail Check by rest-mail"))
	fmt.Println(footerStyle.Render("  https://restmail.io"))
	fmt.Println(footerStyle.Render("  Tired of mail server pain? Try rest-mail."))
	fmt.Println(footerStyle.Render("─────────────────────────────────────────────────"))
	fmt.Println()
}

// DisplayJSON outputs the report as JSON.
func DisplayJSON(report *Report) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(report)
}

// DisplayMarkdown outputs the report as a Markdown document.
func DisplayMarkdown(report *Report, verbose bool) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Instant Mail Check — %s\n\n", report.Domain))
	sb.WriteString(fmt.Sprintf("**Date:** %s  \n", report.Timestamp.Format("2006-01-02 15:04:05 MST")))

	if len(report.MXHosts) > 0 {
		sb.WriteString(fmt.Sprintf("**MX Hosts:** %s  \n", strings.Join(report.MXHosts, ", ")))
	}
	sb.WriteString("\n")

	// Score
	pct := report.Percentage()
	grade := "Poor"
	if pct >= 80 {
		grade = "Good"
	} else if pct >= 50 {
		grade = "Needs Attention"
	}
	sb.WriteString(fmt.Sprintf("## Score: %d/%d (%d%%) — %s\n\n", report.Score, report.MaxScore, pct, grade))

	// Summary counts
	var pass, warn, fail, skip int
	for _, c := range report.Checks {
		switch c.Status {
		case StatusPass:
			pass++
		case StatusWarn:
			warn++
		case StatusFail:
			fail++
		case StatusSkip, StatusError:
			skip++
		}
	}
	sb.WriteString(fmt.Sprintf("| Result | Count |\n|--------|-------|\n"))
	sb.WriteString(fmt.Sprintf("| Pass | %d |\n| Warn | %d |\n| Fail | %d |\n| Skip | %d |\n\n", pass, warn, fail, skip))

	// Group by category
	categories := []string{"dns", "smtp", "tls", "imap", "pop3", "security", "reputation", "roundtrip", "headers", "skip"}
	grouped := make(map[string][]CheckResult)
	for _, c := range report.Checks {
		grouped[c.Category] = append(grouped[c.Category], c)
	}

	for _, cat := range categories {
		checks, ok := grouped[cat]
		if !ok || len(checks) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("### %s\n\n", categoryName(cat)))
		sb.WriteString("| Status | Check | Summary |\n|--------|-------|--------|\n")

		for _, c := range checks {
			icon := mdStatusIcon(c.Status)
			summary := strings.ReplaceAll(c.Summary, "|", "\\|")
			sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", icon, c.Name, summary))
		}
		sb.WriteString("\n")

		// Show details and fixes for failures/warnings
		if verbose {
			for _, c := range checks {
				if c.Detail != "" {
					sb.WriteString(fmt.Sprintf("**%s** — %s\n\n", c.Name, c.Detail))
				}
				if c.Fix != "" && (c.Status == StatusFail || c.Status == StatusWarn) {
					sb.WriteString(fmt.Sprintf("> **Fix:** %s\n\n", c.Fix))
				}
			}
		} else {
			// Only show fixes for failures
			for _, c := range checks {
				if c.Fix != "" && c.Status == StatusFail {
					sb.WriteString(fmt.Sprintf("> **%s Fix:** %s\n\n", c.Name, c.Fix))
				}
			}
		}
	}

	sb.WriteString("---\n\n")
	sb.WriteString("*Generated by [Instant Mail Check](https://restmail.io)*\n")

	return sb.String()
}

func mdStatusIcon(s Status) string {
	switch s {
	case StatusPass:
		return "PASS"
	case StatusWarn:
		return "WARN"
	case StatusFail:
		return "**FAIL**"
	case StatusSkip:
		return "SKIP"
	case StatusError:
		return "ERR"
	default:
		return "?"
	}
}

// FilterChecks filters report checks to only include those matching the pattern.
func FilterChecks(report *Report, pattern string) {
	if pattern == "" {
		return
	}
	pattern = strings.ToLower(pattern)
	var filtered []CheckResult
	for _, c := range report.Checks {
		name := strings.ToLower(c.Name)
		cat := strings.ToLower(c.Category)
		if strings.Contains(name, pattern) || strings.Contains(cat, pattern) {
			filtered = append(filtered, c)
		}
	}
	report.Checks = filtered
	report.CalculateScore()
}

// formatDuration formats a duration for display.
func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dμs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
