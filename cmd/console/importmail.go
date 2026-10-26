package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/restmail/restmail/internal/api/endpoint"
	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/mailimport"
	rmime "github.com/restmail/restmail/internal/mime"
)

const importMailUsage = `restmail import-maildir / import-mbox — import historical mail through the public API

  restmail import-maildir --server https://mail.example.com --account-id 12 --path ./Maildir
  restmail import-mbox --server https://mail.example.com --account-id 12 --path ./Inbox.mbox --mbox-format mboxrd

RESTMAIL_API_KEY needs mailboxes:write on the destination account's domain.
Messages are imported one at a time, preserving folders, supported flags and
internal dates. Existing Message-IDs in the same account and folder are skipped
without changing the first copy. Rerun the same command to resume after failure.
Source files are never changed. Raw messages and credentials are never printed.

  --server URL           https; http only for localhost
  --account-id ID        existing destination account
  --path PATH            Maildir root or one mbox file
  --folder NAME          mbox destination (default INBOX); Maildir maps its folders
  --mbox-format FORMAT   required for mbox: rfc4155, mboxrd or mboxo
  --max-message-bytes N  source message limit (default 10485760; maximum 1 GiB)
  --dry-run              validate source only, with no API calls or key required
  --json                 one summary on stdout; one error on stderr

Maildir uses file mtime for INTERNALDATE and reads cur/new plus Maildir++ folders.
Deleted-state/keyword flags, source symlinks, Content-Length-framed mbox and mbox
pseudo-messages are refused explicitly. Mbox escaping format is never guessed.
Dry-run does not check server authorization, destination, quota or server limits.
See docs/migrating-mail.md for supported formats and cutover limits.

Exit status: 0 complete, 2 usage, 1 source or API import failed.
`

func runImportMail(ctx context.Context, mode string, args []string, token string, client *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-"+mode, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "server URL")
	account := fs.Uint64("account-id", 0, "destination account")
	path := fs.String("path", "", "source path")
	folder := fs.String("folder", "", "mbox destination folder")
	format := fs.String("mbox-format", "", "explicit mbox escaping format")
	limit := fs.Int64("max-message-bytes", 10<<20, "maximum source message bytes")
	dry := fs.Bool("dry-run", false, "source validation only")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, importMailUsage)
		return exitOK
	}
	if fs.NArg() != 0 || *path == "" || *account == 0 || *account > 1<<32-1 || *limit < 1 || *limit > 1<<30 {
		return o.fail(exitUsage, "bad_flag", "give --path, --account-id (1–4294967295) and a message limit between 1 byte and 1 GiB")
	}
	if mode == "mbox" && (*format != "rfc4155" && *format != "mboxrd" && *format != "mboxo") {
		return o.fail(exitUsage, "bad_flag", "choose --mbox-format rfc4155, mboxrd or mboxo; escaping is not guessed")
	}
	if mode == "maildir" && (*folder != "" || *format != "") {
		return o.fail(exitUsage, "bad_flag", "Maildir maps its source folders; --folder and --mbox-format are only for mbox")
	}
	var base *url.URL
	if !*dry {
		var err error
		base, err = serverURL(*server)
		if err != nil {
			return o.fail(exitUsage, "bad_flag", err.Error())
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
	var err error
	if mode == "maildir" {
		err = mailimport.Maildir(ctx, *path, *limit, visit)
	} else {
		var file *os.File
		file, err = os.Open(*path)
		if err == nil {
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				err = errors.New("mbox source must be a regular file")
			} else {
				err = mailimport.Mbox(ctx, file, *folder, *format, *limit, visit)
			}
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		return o.fail(exitFailed, "import_failed", fmt.Sprintf("%s (completed %d imports, skipped %d existing copies; rerun to resume)", err, imported, skipped))
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"messages": total, "imported": imported, "skipped": skipped, "dry_run": *dry})
	} else if *dry {
		fmt.Fprintf(stdout, "Validated %d source messages; no API calls made.\n", total)
	} else {
		fmt.Fprintf(stdout, "Imported %d messages; skipped %d existing copies.\n", imported, skipped)
	}
	return exitOK
}

func postImportedMessage(ctx context.Context, client *http.Client, base *url.URL, token string, account uint32, message apitypes.ImportMessageBody) (apitypes.ImportMessageResult, int, error) {
	var zero apitypes.ImportMessageResult
	body, err := json.Marshal(message)
	if err != nil {
		return zero, 0, errors.New("could not encode the import")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String()+"/api/v1/accounts/"+strconv.FormatUint(uint64(account), 10)+"/messages/import", bytes.NewReader(body))
	if err != nil {
		return zero, 0, errors.New("could not prepare the import")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return zero, 0, errors.New("the import request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zero, resp.StatusCode, nil
	}
	var result endpoint.Data[apitypes.ImportMessageResult]
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&result); err != nil {
		return zero, resp.StatusCode, errors.New("the server returned an invalid import response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return zero, resp.StatusCode, errors.New("the server returned trailing import response data")
	}
	return result.Data, resp.StatusCode, nil
}
