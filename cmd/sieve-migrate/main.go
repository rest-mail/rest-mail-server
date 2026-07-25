// Command sieve-migrate is a one-time migration scanner for stored per-mailbox
// Sieve scripts.
//
// Background: github.com/rest-mail/go-sieve enforces RFC 5228 "require"
// semantics — a script that uses an extension (fileinto, reject, vacation,
// imap4flags, envelope, body, mailbox, copy, the :regex match type, a
// non-default comparator, …) WITHOUT declaring it via `require` fails to parse.
// The delivery path is fail-closed: a script that fails to parse defers that
// mailbox's mail. This tool finds such scripts before that behavior ships and,
// with --repair, rewrites each failing script with the correct `require`.
//
// Modes:
//   - dry-run (default): report every stored script that fails validation — its
//     owning mailbox, the parse error, and the extension(s) it uses that lack a
//     require. Nothing is written.
//   - --repair: for each failing script, derive the extensions it actually uses,
//     prepend a correct `require [...]`, re-validate, and (only if it now parses)
//     write it back. A script that cannot be made to parse is left untouched and
//     reported as unrepairable.
//
// Exit codes: 0 = every stored script is valid (nothing to do); 1 = an
// operational error (config/DB); 2 = scripts need attention (dry-run: failing
// scripts exist; repair: unrepairable scripts remain).
//
// go-sieve version: this tool validates with whatever go-sieve the module is
// pinned to (see validateScript). It is currently pinned to v0.2.0, which
// already enforces `require`, so the scan is meaningful today. If/when the
// server bumps go-sieve (e.g. to v0.3.0), no logic here changes — re-run the
// scanner after the bump to catch anything the newer parser rejects.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/go-sieve"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// validateScript is the single point of contact with the go-sieve parser. The
// version swap (v0.2.0 -> a later release) is a go.mod change only; no logic in
// this package changes because everything routes through here.
func validateScript(script string) error {
	return sieve.Validate(script)
}

// missingCapRe extracts the capability name from a go-sieve "not declared with
// require" error, e.g. `... requires "fileinto" to be declared with require`.
// It matches every gated feature the parser reports this way: actions
// (fileinto/reject/vacation/imap4flags/notify), tests (envelope/body), tagged
// arguments (fileinto :create -> "mailbox", :copy -> "copy", :flags ->
// "imap4flags"), the :regex match type, and non-default comparators
// (comparator-i;ascii-numeric). It deliberately does NOT match
// "require of unsupported extension X" (a script requiring something the parser
// cannot implement), which is genuinely unrepairable.
var missingCapRe = regexp.MustCompile(`requires "([^"]+)" to be declared with require`)

// extractMissingCap returns the capability name a validation error says is
// undeclared, or "" if the error is not a missing-require error.
func extractMissingCap(err error) string {
	if err == nil {
		return ""
	}
	if m := missingCapRe.FindStringSubmatch(err.Error()); len(m) == 2 {
		return m[1]
	}
	return ""
}

// buildRequireStmt renders a Sieve `require [...]` statement for caps. Capability
// names are emitted as Sieve quoted strings; strconv.Quote's escaping is a
// superset-safe encoding for the ascii capability tokens go-sieve uses
// (including ones containing ';' and '-', e.g. comparator-i;ascii-numeric).
func buildRequireStmt(caps []string) string {
	quoted := make([]string, len(caps))
	for i, c := range caps {
		quoted[i] = strconv.Quote(c)
	}
	return "require [" + strings.Join(quoted, ", ") + "];"
}

// applyRequires returns script with a `require` statement for caps prepended.
// A prepended require is always valid: RFC 5228 permits multiple require
// statements as long as they precede every non-require command, and placing ours
// at the very top guarantees that. When caps is empty the script is returned
// unchanged.
func applyRequires(script string, caps []string) string {
	if len(caps) == 0 {
		return script
	}
	return buildRequireStmt(caps) + "\n" + script
}

// analysis is the verdict for a single script.
type analysis struct {
	OK         bool     // parses as-is; nothing to do
	Err        error    // original validation error (nil when OK)
	Missing    []string // extensions used but not declared, discovered during repair
	Repaired   string   // a script that parses (valid only when Repairable)
	Repairable bool     // Repaired can replace the original
}

// analyze validates a script and, if it fails, derives the missing `require`
// extensions and a repaired form.
func analyze(script string) analysis {
	origErr := validateScript(script)
	if origErr == nil {
		return analysis{OK: true}
	}
	missing, repaired, repairable := deriveRepair(script)
	return analysis{Err: origErr, Missing: missing, Repaired: repaired, Repairable: repairable}
}

// deriveRepair iteratively discovers which extensions a failing script uses
// without a require. The parser reports only the FIRST undeclared extension, so
// each pass adds the reported capability to a prepended require and re-validates,
// accumulating the full set until the script parses (repairable) or an error
// that is not a missing-require error is hit (unrepairable). The seen-set guards
// against a non-terminating loop if the same capability were ever reported twice.
func deriveRepair(script string) (missing []string, repaired string, repairable bool) {
	seen := map[string]bool{}
	var caps []string
	for {
		candidate := applyRequires(script, caps)
		err := validateScript(candidate)
		if err == nil {
			return caps, candidate, true
		}
		missingCap := extractMissingCap(err)
		if missingCap == "" || seen[missingCap] {
			// Not a missing-require error (a genuine syntax error, an unsupported
			// require, or no forward progress): cannot auto-repair.
			return caps, "", false
		}
		seen[missingCap] = true
		caps = append(caps, missingCap)
	}
}

// ── Store ────────────────────────────────────────────────────────────

// scriptRecord is one stored Sieve script joined to its owning mailbox.
type scriptRecord struct {
	ScriptID  uint
	MailboxID uint
	Address   string // mailbox address, for reporting; may be empty if the join misses
	Script    string
	Active    bool
}

// owner is a human-readable identifier for a script's mailbox.
func (r scriptRecord) owner() string {
	if r.Address != "" {
		return r.Address
	}
	return fmt.Sprintf("mailbox_id=%d", r.MailboxID)
}

// Store enumerates and updates stored Sieve scripts. It is an interface so the
// scan/repair logic can be tested without a database.
type Store interface {
	List() ([]scriptRecord, error)
	UpdateScript(scriptID uint, newScript string) error
}

// gormStore is the production Store backed by the sieve_scripts table.
type gormStore struct{ db *gorm.DB }

func (s *gormStore) List() ([]scriptRecord, error) {
	var recs []scriptRecord
	err := s.db.
		Table("sieve_scripts AS ss").
		Select("ss.id AS script_id, ss.mailbox_id AS mailbox_id, ss.script AS script, ss.active AS active, m.address AS address").
		Joins("LEFT JOIN mailboxes m ON m.id = ss.mailbox_id").
		Order("ss.id").
		Scan(&recs).Error
	return recs, err
}

func (s *gormStore) UpdateScript(scriptID uint, newScript string) error {
	return s.db.Model(&models.SieveScript{}).
		Where("id = ?", scriptID).
		Update("script", newScript).Error
}

// ── Run ──────────────────────────────────────────────────────────────

type summary struct {
	Total        int
	OK           int
	Failing      int
	Repaired     int
	Unrepairable int
}

// run scans every stored script, reports failures, and (when repair is set)
// writes back repairs. It returns the summary counts and the first write error.
func run(store Store, repair bool, out io.Writer) (summary, error) {
	recs, err := store.List()
	if err != nil {
		return summary{}, fmt.Errorf("enumerate sieve scripts: %w", err)
	}

	var sum summary
	sum.Total = len(recs)
	fmt.Fprintf(out, "scanning %d stored sieve script(s)\n", sum.Total)

	for _, rec := range recs {
		a := analyze(rec.Script)
		if a.OK {
			sum.OK++
			continue
		}
		sum.Failing++

		missing := "none detected"
		if len(a.Missing) > 0 {
			missing = strings.Join(a.Missing, ", ")
		}
		fmt.Fprintf(out, "\nFAIL  %s (script_id=%d)\n", rec.owner(), rec.ScriptID)
		fmt.Fprintf(out, "      error:            %s\n", a.Err)
		fmt.Fprintf(out, "      missing require:  %s\n", missing)

		if !a.Repairable {
			sum.Unrepairable++
			fmt.Fprintf(out, "      repair:           NOT auto-repairable (manual fix required)\n")
			continue
		}

		if !repair {
			fmt.Fprintf(out, "      repair:           would add require [%s] (run with --repair)\n", strings.Join(quoteAll(a.Missing), ", "))
			continue
		}

		if err := store.UpdateScript(rec.ScriptID, a.Repaired); err != nil {
			return sum, fmt.Errorf("write repaired script for %s (script_id=%d): %w", rec.owner(), rec.ScriptID, err)
		}
		sum.Repaired++
		fmt.Fprintf(out, "      repair:           REPAIRED (added require [%s])\n", strings.Join(quoteAll(a.Missing), ", "))
	}

	fmt.Fprintf(out, "\n─ summary ─\n")
	fmt.Fprintf(out, "total:        %d\n", sum.Total)
	fmt.Fprintf(out, "ok:           %d\n", sum.OK)
	fmt.Fprintf(out, "failing:      %d\n", sum.Failing)
	if repair {
		fmt.Fprintf(out, "repaired:     %d\n", sum.Repaired)
	}
	fmt.Fprintf(out, "unrepairable: %d\n", sum.Unrepairable)
	if !repair && sum.Failing > 0 {
		fmt.Fprintf(out, "\n(dry run — no scripts were modified; re-run with --repair to fix repairable scripts)\n")
	}
	return sum, nil
}

func quoteAll(caps []string) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = strconv.Quote(c)
	}
	return out
}

// exitCode maps a completed run to a process exit code (see package doc).
func exitCode(sum summary, repair bool) int {
	if repair {
		if sum.Unrepairable > 0 {
			return 2
		}
		return 0
	}
	if sum.Failing > 0 {
		return 2
	}
	return 0
}

func main() {
	repair := flag.Bool("repair", false, "rewrite each repairable failing script with the correct require (default: dry-run, report only)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: sieve-migrate [--repair]\n\n")
		fmt.Fprintf(os.Stderr, "One-time scanner for stored per-mailbox Sieve scripts that use an\n")
		fmt.Fprintf(os.Stderr, "extension without declaring it via `require` (which now fails delivery).\n\n")
		fmt.Fprintf(os.Stderr, "Reads the same DB config as the server (env / config.Load).\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load config: %v\n", err)
		os.Exit(1)
	}

	database, err := db.WaitForDB(cfg, 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: connect to database: %v\n", err)
		os.Exit(1)
	}

	store := &gormStore{db: database}
	if *repair {
		fmt.Println("MODE: --repair (failing scripts WILL be rewritten in place)")
	} else {
		fmt.Println("MODE: dry-run (no writes; pass --repair to fix)")
	}

	sum, err := run(store, *repair, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(exitCode(sum, *repair))
}
