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
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/mailimport"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
)

const importMailUsage = `restmail import-maildir / import-mbox — import historical mail through the public API

  restmail import-maildir --server https://mail.example.com --account-id 12 --path ./Maildir
  restmail import-mbox --server https://mail.example.com --account-id 12 --path ./Inbox.mbox --mbox-format mboxrd

RESTMAIL_API_KEY needs mail:import on the destination account or its domain.
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

Maildir uses file mtime for INTERNALDATE and reads cur/new plus Maildir++ folders,
decoding their modified UTF-7 names as Dovecot writes them. A folder name never
stops the import: an invalid or colliding name is imported under a derived name
or "<name> (2)", and each rename is reported (renamed_folders in --json).
Source symlinks, Content-Length-framed mbox and mbox
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
	destinations := mailimport.NewDestinations()
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
		supplied := message.Folder
		message.Folder = destinations.For(supplied)
		if session == nil {
			opened, err := openImportSession(ctx, client, base, token, uint32(*account), mode)
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
		if result.ID == 0 || result.MessageID == "" || !destinations.Record(supplied, result.Folder) {
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
		err = mailimport.Maildir(ctx, *path, *limit, visit, report)
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
	result := map[string]any{"messages": total, "imported": imported, "skipped": skipped, "dry_run": *dry, "renamed_folders": renames}
	if closeErr := finishImportSession(session, o, result, err != nil); err == nil {
		err = closeErr
	}
	if err != nil {
		return o.fail(exitFailed, "import_failed", fmt.Sprintf("%s (completed %d imports, skipped %d existing copies; rerun to resume)", err, imported, skipped))
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(result)
	} else if *dry {
		fmt.Fprintf(stdout, "Validated %d source messages; no API calls made.\n", total)
	} else {
		fmt.Fprintf(stdout, "Imported %d messages; skipped %d existing copies.\n", imported, skipped)
	}
	return exitOK
}

// importSessionRun is one import run's session (#1498): opened before the first message,
// named on every import, and closed when the run ends however it ends, so the run is
// audited as one summary.
type importSessionRun struct {
	client  *http.Client
	base    *url.URL
	token   string
	account uint32
	id      string
}

// importSessionCloseTimeout bounds the close of a run that was interrupted or failed.
const importSessionCloseTimeout = 30 * time.Second

func (s *importSessionRun) path() string {
	return s.base.String() + "/api/v1/accounts/" + strconv.FormatUint(uint64(s.account), 10) + "/messages/import-sessions"
}

func (s *importSessionRun) do(ctx context.Context, method, path string, body []byte, want int, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return errors.New("could not prepare the import session request")
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	noRedirect := *s.client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return errors.New("the import session request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		return fmt.Errorf("server refused the import session (HTTP %d)", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errors.New("the server returned an invalid import session response")
	}
	return nil
}

// openImportSession opens the run's session, naming this tool as its client.
func openImportSession(ctx context.Context, client *http.Client, base *url.URL, token string, account uint32, mode string) (*importSessionRun, error) {
	s := &importSessionRun{client: client, base: base, token: token, account: account}
	body, _ := json.Marshal(apitypes.OpenImportSessionBody{ClientName: "restmail import-" + mode})
	var out endpoint.Data[apitypes.ImportSession]
	if err := s.do(ctx, http.MethodPost, s.path(), body, http.StatusCreated, &out); err != nil {
		return nil, err
	}
	if len(out.Data.ID) != 32 || strings.Trim(out.Data.ID, "0123456789abcdef") != "" {
		return nil, errors.New("the server returned an invalid import session id")
	}
	s.id = out.Data.ID
	return s, nil
}

// close closes the session and answers its summary. It does not use the run's context:
// an interrupted run still closes its session.
func (s *importSessionRun) close() (apitypes.ImportSessionSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), importSessionCloseTimeout)
	defer cancel()
	var out endpoint.Data[apitypes.ImportSessionSummary]
	err := s.do(ctx, http.MethodDelete, s.path()+"/"+s.id, nil, http.StatusOK, &out)
	return out.Data, err
}

// importSummaryLine is the closed session's summary as the text output prints it.
func importSummaryLine(summary apitypes.ImportSessionSummary) string {
	return fmt.Sprintf("Import summary: %d imported, %d deduplicated, %d refused, %d bytes (session %s).",
		summary.Imported, summary.Deduplicated, summary.Refused, summary.Bytes, summary.ID)
}

// finishImportSession closes the run's session and reports its summary: in text output
// a line on stdout, in JSON output under "summary" of the run's own summary. A close
// that fails turns a complete run into a failed one.
func finishImportSession(session *importSessionRun, o output, report map[string]any, failed bool) error {
	if session == nil {
		return nil
	}
	summary, err := session.close()
	if err != nil {
		return fmt.Errorf("closing import session %s: %w", session.id, err)
	}
	if o.json {
		report["summary"] = summary
	} else if !failed {
		fmt.Fprintln(o.stdout, importSummaryLine(summary))
	} else {
		fmt.Fprintln(o.stderr, importSummaryLine(summary))
	}
	return nil
}

func postImportedMessage(ctx context.Context, client *http.Client, base *url.URL, token string, account uint32, session string, message apitypes.ImportMessageBody) (apitypes.ImportMessageResult, int, error) {
	var zero apitypes.ImportMessageResult
	body, err := json.Marshal(message)
	if err != nil {
		return zero, 0, errors.New("could not encode the import")
	}
	target := base.String() + "/api/v1/accounts/" + strconv.FormatUint(uint64(account), 10) + "/messages/import"
	if session != "" {
		target += "?import_session=" + url.QueryEscape(session)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
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
