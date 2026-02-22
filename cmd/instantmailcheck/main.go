package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/restmail/restmail/internal/mailcheck"
)

// Version is set via ldflags at build time.
var Version = "dev"

func main() {
	// Flags
	dkimSelector := flag.String("dkim-selector", "", "DKIM selector to check (default: tries common selectors)")
	sendTo := flag.String("send-to", "", "Email address to send a test message to (Tier 2)")
	user := flag.String("user", "", "Username/email for authenticated checks (Tier 3)")
	pass := flag.String("pass", "", "Password for authenticated checks (Tier 3)")
	verbose := flag.Bool("v", false, "Show detailed output for every check")
	jsonOutput := flag.Bool("json", false, "Output results as JSON")
	markdown := flag.Bool("markdown", false, "Output results as Markdown")
	securityAudit := flag.Bool("security-audit", false, "Run Tier 4 exploit simulation checks (safe, non-destructive)")
	timeout := flag.Duration("timeout", 10*time.Second, "Timeout per check")
	threshold := flag.Int("threshold", 50, "Minimum score percentage to pass (exit code 0)")
	checks := flag.String("checks", "", "Filter checks by name or category pattern (e.g., 'dns', 'tls', 'security')")
	output := flag.String("output", "", "Write report to file instead of stdout")
	showVersion := flag.Bool("version", false, "Show version and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Instant Mail Check — comprehensive mail server diagnostics
https://restmail.io

Usage:
  instantmailcheck [flags] <domain>

Examples:
  instantmailcheck example.com                              # Public audit
  instantmailcheck example.com --dkim-selector default      # Specific DKIM selector
  instantmailcheck example.com --send-to test@example.com   # Send test email
  instantmailcheck example.com --user u --pass p            # Full auth test
  instantmailcheck example.com --security-audit             # Exploit simulation
  instantmailcheck example.com --json                       # JSON output
  instantmailcheck example.com --markdown --output report.md  # Markdown report
  instantmailcheck example.com --threshold 80               # Strict CI pass/fail
  instantmailcheck example.com --checks dns                 # Only DNS checks

Flags:
`)
		flag.PrintDefaults()
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("instantmailcheck v%s\n", Version)
		os.Exit(0)
	}

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: domain argument required")
		fmt.Fprintln(os.Stderr, "Usage: instantmailcheck [flags] <domain>")
		os.Exit(1)
	}

	domain := flag.Arg(0)

	opts := mailcheck.Options{
		Domain:        domain,
		DKIMSelector:  *dkimSelector,
		SendTo:        *sendTo,
		User:          *user,
		Pass:          *pass,
		Verbose:       *verbose,
		JSON:          *jsonOutput,
		SecurityAudit: *securityAudit,
		Timeout:       *timeout,
	}

	quiet := *jsonOutput || *markdown
	if !quiet {
		fmt.Printf("Checking %s...\n", domain)
	}

	report := mailcheck.Run(opts)

	// Apply check filter if specified
	if *checks != "" {
		mailcheck.FilterChecks(report, *checks)
	}

	// Generate output
	var outputStr string
	if *jsonOutput {
		mailcheck.DisplayJSON(report)
	} else if *markdown {
		outputStr = mailcheck.DisplayMarkdown(report, *verbose)
		if *output != "" {
			if err := os.WriteFile(*output, []byte(outputStr), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing to %s: %s\n", *output, err)
				os.Exit(1)
			}
			fmt.Printf("Report written to %s\n", *output)
		} else {
			fmt.Print(outputStr)
		}
	} else {
		mailcheck.DisplayReport(report, *verbose)
	}

	// Exit with non-zero if score is below threshold
	if report.Percentage() < *threshold {
		os.Exit(2)
	}
}
