package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/mailimport"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
)

const importIMAPUsage = `restmail import-imap — copy an IMAP account through the public import API

  restmail import-imap --source imaps://old.example.com --source-user alice@example.com --account-id 12 --dry-run
  restmail import-imap --source imap://old.example.com --starttls --source-user alice@example.com --account-id 12 --dry-run
  restmail import-imap --source imaps://old.example.com --source-user alice@example.com --server https://mail.example.com --account-id 12

Set RESTMAIL_IMAP_PASSWORD to the source password or app password, or set
RESTMAIL_IMAP_ACCESS_TOKEN to an operator-obtained OAuth access token. Never set
both. OAuth uses advertised OAUTHBEARER or XOAUTH2 after verified TLS. Set
RESTMAIL_API_KEY for the destination with mail:import on its account/domain.
No password/token argument, URL credentials, unprotected IMAP or insecure TLS option
is accepted. An imap:// source requires --starttls; credentials follow verified
TLS. The source is examined read-only; BODY.PEEK never marks mail read.

  --source URL           imaps://hostname[:993], or imap://hostname[:143] with --starttls
  --starttls             require a verified STARTTLS upgrade; never fall back to plaintext
  --source-user NAME     source IMAP username (master-user syntax is allowed)
  --server URL           destination https; http only for localhost
  --account-id ID        existing destination account
  --max-message-bytes N  source message limit (default 10485760; maximum 1 GiB)
  --dry-run              read/validate the IMAP source; no destination API calls
  --json                 one summary on stdout; one error on stderr

All selectable folders, original octets, supported flags and INTERNALDATE are
copied. An invalid or colliding folder name is imported under a derived name or
"<name> (2)" and reported (renamed_folders in --json). Deleted and keyword flags are preserved. Rerun to
resume: destination account/folder/Message-ID deduplication skips existing mail.
Later source edits, moves and deletes are not synchronized. Empty source folders
cannot be created by message import. No device flow or token refresh is performed.
If the source refuses an expired token or stops a long copy, obtain a valid token
and rerun to resume; completed imports are retained.
See docs/migrating-mail.md for cutover and verification limits.
`

type imapImportSource func(context.Context, mailimport.IMAPOptions, int64, mailimport.Visit) error

func runImportIMAP(ctx context.Context, args []string, token, password, accessToken string, client *http.Client, stdout, stderr io.Writer) int {
	return runImportIMAPSource(ctx, args, token, password, accessToken, client, stdout, stderr, mailimport.IMAP)
}

func runImportIMAPSource(ctx context.Context, args []string, token, password, accessToken string, client *http.Client, stdout, stderr io.Writer, readSource imapImportSource) int {
	fs := flag.NewFlagSet("import-imap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "IMAP source URL")
	startTLS := fs.Bool("starttls", false, "require a verified STARTTLS upgrade")
	user := fs.String("source-user", "", "IMAP source username")
	server := fs.String("server", "", "destination URL")
	account := fs.Uint64("account-id", 0, "destination account")
	limit := fs.Int64("max-message-bytes", 10<<20, "maximum source message bytes")
	dry := fs.Bool("dry-run", false, "validate source without destination writes")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		// Flag errors may contain a supplied value; do not echo URL credentials.
		return o.fail(exitUsage, "bad_flag", "invalid import-imap flags; use --help")
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, importIMAPUsage)
		return exitOK
	}
	u, err := url.Parse(*source)
	if err != nil || (!*startTLS && u.Scheme != "imaps") || (*startTLS && u.Scheme != "imap") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return o.fail(exitUsage, "bad_flag", "source must be imaps://hostname[:port], or imap://hostname[:port] with --starttls, without credentials, path, query or fragment")
	}
	port := u.Port()
	if port == "" {
		port = "993"
		if *startTLS {
			port = "143"
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || fs.NArg() != 0 || *user == "" || *account == 0 || *account > 1<<32-1 || *limit < 1 || *limit > 1<<30 {
		return o.fail(exitUsage, "bad_flag", "give a valid source, source user, account ID and message limit between 1 byte and 1 GiB")
	}
	if password != "" && accessToken != "" {
		return o.fail(exitUsage, "conflicting_source_credentials", "set either RESTMAIL_IMAP_PASSWORD or RESTMAIL_IMAP_ACCESS_TOKEN, not both")
	}
	if password == "" && accessToken == "" {
		return o.fail(exitUsage, "no_source_password", "set RESTMAIL_IMAP_PASSWORD to the source password or app password, or RESTMAIL_IMAP_ACCESS_TOKEN to an OAuth access token")
	}
	var base *url.URL
	if !*dry {
		base, err = serverURL(*server)
		if err != nil {
			return o.fail(exitUsage, "bad_flag", "give a valid destination --server URL")
		}
		if token == "" {
			return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY with mail:import on the destination")
		}
	}
	// The run's import session is opened at its first import, so a source refused
	// before then makes no API call, and closed when the run ends.
	var session *importSessionRun
	total, imported, skipped := 0, 0, 0
	renames := []mailimport.FolderRename{}
	report := func(rename mailimport.FolderRename) {
		renames = append(renames, rename)
		if !o.json {
			fmt.Fprintf(stdout, "Source folder %q is imported as %q: %s.\n", rename.Source, rename.Folder, rename.Reason)
		}
	}
	visit := func(source string, message apitypes.ImportMessageBody) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := rmime.Parse(message.RawMessage); err != nil || len(message.RawMessage) == 0 {
			return fmt.Errorf("%s is not a parseable message", source)
		}
		total++
		if *dry {
			return nil
		}
		if session == nil {
			opened, err := openImportSession(ctx, client, base, token, uint32(*account), "imap")
			if err != nil {
				return fmt.Errorf("%s: %w", source, err)
			}
			session = opened
		}
		result, status, err := postImportedMessage(ctx, client, base, token, uint32(*account), session.id, message)
		if err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("%s: server refused import (HTTP %d); check destination, scope, size and quota", source, status)
		}
		if result.ID == 0 || result.MessageID == "" || result.Folder != message.Folder {
			return fmt.Errorf("%s: server returned an invalid import result", source)
		}
		if result.Imported {
			imported++
		} else {
			skipped++
		}
		return nil
	}
	err = readSource(ctx, mailimport.IMAPOptions{Address: net.JoinHostPort(u.Hostname(), port), Username: *user, Password: password, AccessToken: accessToken, StartTLS: *startTLS, Renamed: report}, *limit, visit)
	result := map[string]any{"messages": total, "imported": imported, "skipped": skipped, "dry_run": *dry, "renamed_folders": renames}
	if closeErr := finishImportSession(session, o, result, err != nil); err == nil {
		err = closeErr
	}
	if err != nil {
		detail := err.Error()
		if accessToken != "" {
			detail = strings.ReplaceAll(detail, accessToken, "[redacted]")
			detail += "; if the source token expired or was refused, obtain a valid RESTMAIL_IMAP_ACCESS_TOKEN and rerun"
		}
		return o.fail(exitFailed, "import_failed", fmt.Sprintf("%s (completed %d imports, skipped %d existing copies; rerun to resume)", detail, imported, skipped))
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(result)
	} else if *dry {
		fmt.Fprintf(stdout, "Validated %d source messages; no destination API calls made.\n", total)
	} else {
		fmt.Fprintf(stdout, "Imported %d messages; skipped %d existing copies.\n", imported, skipped)
	}
	return exitOK
}
