package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/restmail/restmail/internal/auth"
	rmail "github.com/restmail/restmail/internal/mail"
)

const importPasswdFileUsage = `restmail import-passwd-file — create the accounts of a Dovecot passwd-file, keeping their passwords

  restmail import-passwd-file --server https://mail.example.com --domain example.com /etc/dovecot/users

Reads a Dovecot passwd-file (user:{SCHEME}hash:uid:gid:gecos:home:shell:extra),
from the file named or from stdin, and creates one account per user with its
existing ARGON2ID, ARGON2I, BLF-CRYPT, SHA256-CRYPT or SHA512-CRYPT hash, as
import-account does. A user without a domain takes --domain. Authentication uses
RESTMAIL_API_KEY, a scoped API key with mailboxes:write on the domains.

An existing account is reported and left alone, never replaced. A line with no
password, a weak or unsupported scheme, or an invalid address is reported and
skipped; the other users are still imported. Hashes and the API key are never
printed. Only addresses and passwords move: not display names, quotas, aliases
or mail. See docs/migrating-accounts.md.

  --server URL      the server's address (https; http only for localhost);
                    not needed with --dry-run
  --domain DOMAIN   the domain of users written without one
  --dry-run         check every line and send nothing
  --json            one JSON object on stdout: every user, by outcome

Exit status: 0 every user created or already present, 1 any line failed or
was refused, 2 usage.
`

// maxPasswdFileBytes bounds what is read: far above any real passwd-file.
const maxPasswdFileBytes = 64 << 20

// passwdFileResult is one user line's outcome. It never holds the hash.
type passwdFileResult struct {
	Line    int    `json:"line"`
	Address string `json:"address,omitempty"`
	ID      uint   `json:"id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func runImportPasswdFile(ctx context.Context, args []string, stdin io.Reader, token string, client *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-passwd-file", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
	domain := fs.String("domain", "", "the domain of users written without one")
	dryRun := fs.Bool("dry-run", false, "check every line and send nothing")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, importPasswdFileUsage)
		return exitOK
	}
	base, err := serverURL(*server)
	if err != nil && !(*dryRun && *server == "") {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	if *domain != "" {
		if _, err := rmail.CanonicalMailboxAddress("postmaster@" + *domain); err != nil {
			return o.fail(exitUsage, "bad_flag", "give --domain as a domain name")
		}
	}
	if token == "" && !*dryRun {
		return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY to a scoped API key with mailboxes:write on the domains")
	}
	var source io.Reader
	switch fs.NArg() {
	case 0:
		if isTerminal(stdin) {
			return o.fail(exitUsage, "no_file", "name the passwd-file, or pipe it on stdin")
		}
		source = stdin
	case 1:
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return o.fail(exitUsage, "no_file", "could not open the passwd-file")
		}
		defer f.Close()
		source = f
	default:
		return o.fail(exitUsage, "bad_flag", "name at most one passwd-file")
	}

	outcome := map[string][]passwdFileResult{"created": {}, "existing": {}, "would_create": {}, "failed": {}}
	scanner := bufio.NewScanner(io.LimitReader(source, maxPasswdFileBytes))
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		address, hash, reason := parsePasswdFileLine(text, *domain)
		if reason != "" {
			outcome["failed"] = append(outcome["failed"], passwdFileResult{Line: line, Address: address, Reason: reason})
			continue
		}
		if *dryRun {
			outcome["would_create"] = append(outcome["would_create"], passwdFileResult{Line: line, Address: address})
			continue
		}
		id, status, err := postImportedAccount(ctx, client, base, token, address, "", hash)
		switch {
		case err != nil:
			outcome["failed"] = append(outcome["failed"], passwdFileResult{Line: line, Address: address, Reason: err.Error()})
		case status == http.StatusConflict:
			outcome["existing"] = append(outcome["existing"], passwdFileResult{Line: line, Address: address, Reason: "the account already exists; left alone"})
		case status != http.StatusCreated:
			outcome["failed"] = append(outcome["failed"], passwdFileResult{Line: line, Address: address, Reason: fmt.Sprintf("the server refused the import (HTTP %d)", status)})
		case id == 0:
			outcome["failed"] = append(outcome["failed"], passwdFileResult{Line: line, Address: address, Reason: "the server returned an invalid account response"})
		default:
			outcome["created"] = append(outcome["created"], passwdFileResult{Line: line, Address: address, ID: id})
		}
	}
	if err := scanner.Err(); err != nil {
		outcome["failed"] = append(outcome["failed"], passwdFileResult{Line: line + 1, Reason: "the file could not be read past this line (a line over 64 KiB?)"})
	}

	if o.json {
		_ = json.NewEncoder(stdout).Encode(outcome)
	} else {
		for _, r := range outcome["failed"] {
			fmt.Fprintf(stderr, "restmail: line %d %s: %s\n", r.Line, r.Address, r.Reason)
		}
		if *dryRun {
			fmt.Fprintf(stdout, "Would create %d accounts; %d lines fail.\n", len(outcome["would_create"]), len(outcome["failed"]))
		} else {
			fmt.Fprintf(stdout, "Created %d accounts; %d already existed and were left alone; %d lines failed.\n",
				len(outcome["created"]), len(outcome["existing"]), len(outcome["failed"]))
		}
	}
	if len(outcome["failed"]) > 0 {
		return exitFailed
	}
	return exitOK
}

// parsePasswdFileLine reads one user line: the address (with the default domain
// added when it has none) and the normalized hash, or why the line is refused.
// Dovecot's fields are user:password:uid:gid:gecos:home:shell:extra; only the
// first two matter here, and a hash never contains a colon.
func parsePasswdFileLine(text, domain string) (address, hash, reason string) {
	fields := strings.SplitN(text, ":", 8)
	if len(fields) < 2 {
		return "", "", "not a passwd-file line (user:password:...)"
	}
	user := strings.TrimSpace(fields[0])
	if !strings.Contains(user, "@") {
		if domain == "" {
			return user, "", "the user has no domain; give --domain"
		}
		user += "@" + domain
	}
	address, err := rmail.CanonicalMailboxAddress(user)
	if err != nil {
		return user, "", "not a valid mail address"
	}
	if fields[1] == "" {
		return address, "", "no password in the file"
	}
	hash, err = auth.NormalizeImportedPasswordHash(fields[1])
	if err != nil {
		return address, "", err.Error()
	}
	return address, hash, ""
}
