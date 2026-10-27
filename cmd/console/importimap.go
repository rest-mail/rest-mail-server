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

	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/mailimport"
	rmime "github.com/restmail/restmail/internal/mime"
)

const importIMAPUsage = `restmail import-imap — copy an IMAP account through the public import API

  restmail import-imap --source imaps://old.example.com --source-user alice@example.com --account-id 12 --dry-run
  restmail import-imap --source imaps://old.example.com --source-user alice@example.com --server https://mail.example.com --account-id 12

Set RESTMAIL_IMAP_PASSWORD to the source password or app password. Set
RESTMAIL_API_KEY for the destination with mailboxes:write on its account/domain.
No password argument, URL credentials, plaintext IMAP or insecure TLS option is
accepted. The source is examined read-only; BODY.PEEK never marks mail read.

  --source URL           imaps://hostname[:993], verified TLS only
  --source-user NAME     source IMAP username (master-user syntax is allowed)
  --server URL           destination https; http only for localhost
  --account-id ID        existing destination account
  --max-message-bytes N  source message limit (default 10485760; maximum 1 GiB)
  --dry-run              read/validate the IMAP source; no destination API calls
  --json                 one summary on stdout; one error on stderr

All selectable folders, original octets, supported flags and INTERNALDATE are
copied. Deleted and keyword flags are refused rather than dropped. Rerun to
resume: destination account/folder/Message-ID deduplication skips existing mail.
Later source edits, moves and deletes are not synchronized. Empty source folders
cannot be created by message import. OAuth and STARTTLS sources are not supported
by this command; use an IMAP app password and an implicit-TLS endpoint.
See docs/migrating-mail.md for cutover and verification limits.
`

type imapImportSource func(context.Context, mailimport.IMAPOptions, int64, mailimport.Visit) error

func runImportIMAP(ctx context.Context, args []string, token, password string, client *http.Client, stdout, stderr io.Writer) int {
	return runImportIMAPSource(ctx, args, token, password, client, stdout, stderr, mailimport.IMAP)
}

func runImportIMAPSource(ctx context.Context, args []string, token, password string, client *http.Client, stdout, stderr io.Writer, readSource imapImportSource) int {
	fs := flag.NewFlagSet("import-imap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "IMAP source URL")
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
	if err != nil || u.Scheme != "imaps" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return o.fail(exitUsage, "bad_flag", "source must be imaps://hostname[:port] without credentials, path, query or fragment")
	}
	port := u.Port()
	if port == "" {
		port = "993"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || fs.NArg() != 0 || *user == "" || *account == 0 || *account > 1<<32-1 || *limit < 1 || *limit > 1<<30 {
		return o.fail(exitUsage, "bad_flag", "give a valid source, source user, account ID and message limit between 1 byte and 1 GiB")
	}
	if password == "" {
		return o.fail(exitUsage, "no_source_password", "set RESTMAIL_IMAP_PASSWORD to the source password or app password")
	}
	var base *url.URL
	if !*dry {
		base, err = serverURL(*server)
		if err != nil {
			return o.fail(exitUsage, "bad_flag", "give a valid destination --server URL")
		}
		if token == "" {
			return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY with mailboxes:write on the destination")
		}
	}
	total, imported, skipped := 0, 0, 0
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
		result, status, err := postImportedMessage(ctx, client, base, token, uint32(*account), message)
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
	err = readSource(ctx, mailimport.IMAPOptions{Address: net.JoinHostPort(u.Hostname(), port), Username: *user, Password: password}, *limit, visit)
	if err != nil {
		return o.fail(exitFailed, "import_failed", fmt.Sprintf("%s (completed %d imports, skipped %d existing copies; rerun to resume)", err, imported, skipped))
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"messages": total, "imported": imported, "skipped": skipped, "dry_run": *dry})
	} else if *dry {
		fmt.Fprintf(stdout, "Validated %d source messages; no destination API calls made.\n", total)
	} else {
		fmt.Fprintf(stdout, "Imported %d messages; skipped %d existing copies.\n", imported, skipped)
	}
	return exitOK
}
