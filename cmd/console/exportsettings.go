package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const exportSettingsUsage = `restmail export-settings — export an account's settings as one JSON file through the public API

  restmail export-settings --server https://mail.example.com --account-id 12 --path ./settings.json

RESTMAIL_API_KEY needs mail:read on the account or its domain. Every account holds
mail:read on its own mailbox. The file holds the account's address and display
name, its folder names, its Sieve script, its vacation reply, its notification
quiet hours and its contact reply-time setting, under a "format" of
` + settingsFormat + ` and the export time in UTC. Mail is exported by
restmail export-maildir.

  --server URL   https; http only for localhost
  --account-id ID the account to export
  --path PATH    the file to write; refused when it exists unless --overwrite
  --overwrite    replace an existing file at --path
  --json         one summary on stdout; one error on stderr

Only the fields named in docs/exporting-settings.md are written: no password
hash, API key, app password, client secret, two-factor seed, session or device
token is ever read or written. The file is written beside --path and renamed
into place when complete, readable only by its owner. Credentials are never
printed. See docs/exporting-settings.md.

Exit status: 0 complete, 2 usage, 1 export failed.
`

// settingsFormat names the layout of an export-settings file. A change that
// renames or removes a field, or changes what one means, is a new version.
const settingsFormat = "restmail.settings.v1"

// settingsMaxResponse bounds one settings response: a Sieve script or a
// vacation reply is small, and nothing here is mail.
const settingsMaxResponse = 4 << 20

// settingsExport is the file. Each section is a type of this command's own,
// so a field the server adds to a response, a secret or not, is never written
// until this command names it.
type settingsExport struct {
	Format           string                   `json:"format"`
	ExportedAt       string                   `json:"exported_at"`
	Account          settingsAccount          `json:"account"`
	Folders          []settingsFolder         `json:"folders"`
	Sieve            settingsSieve            `json:"sieve"`
	Vacation         settingsVacation         `json:"vacation"`
	QuietHours       settingsQuietHours       `json:"quiet_hours"`
	ContactReplyTime settingsContactReplyTime `json:"contact_reply_time"`
}

type settingsAccount struct {
	ID          uint   `json:"id"`
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
}

type settingsFolder struct {
	Name string `json:"name"`
}

type settingsSieve struct {
	Script string `json:"script"`
	Active bool   `json:"active"`
}

type settingsVacation struct {
	Enabled   bool       `json:"enabled"`
	Subject   string     `json:"subject"`
	Body      string     `json:"body"`
	StartDate *time.Time `json:"start_date"`
	EndDate   *time.Time `json:"end_date"`
}

type settingsQuietHours struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start"`
	End      string `json:"end"`
	TimeZone string `json:"time_zone"`
}

type settingsContactReplyTime struct {
	Enabled bool `json:"enabled"`
}

type settingsReader struct {
	ctx    context.Context
	client *http.Client
	base   *url.URL
	token  string
}

func runExportSettings(ctx context.Context, args []string, token string, client *http.Client, now func() time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export-settings", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "server URL")
	account := fs.Uint64("account-id", 0, "account to export")
	path := fs.String("path", "", "file to write")
	overwrite := fs.Bool("overwrite", false, "replace an existing file")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, exportSettingsUsage)
		return exitOK
	}
	if fs.NArg() != 0 || *path == "" || *account == 0 || *account > 1<<32-1 {
		return o.fail(exitUsage, "bad_flag", "give --path and --account-id (1–4294967295)")
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	if token == "" {
		return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY with mail:read on the account")
	}
	if err := settingsDestination(*path, *overwrite); err != nil {
		return o.fail(exitUsage, "bad_destination", err.Error())
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r := settingsReader{ctx: ctx, client: &noRedirect, base: base, token: token}
	export, err := r.read(strconv.FormatUint(*account, 10), now().UTC())
	if err != nil {
		return o.fail(exitFailed, "export_failed", err.Error())
	}
	if err := writeSettingsFile(*path, export); err != nil {
		return o.fail(exitFailed, "export_failed", "write "+*path+": "+err.Error())
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"path": *path, "format": export.Format, "folders": len(export.Folders)})
	} else {
		fmt.Fprintf(stdout, "Exported the settings of %s to %s.\n", export.Account.Address, *path)
	}
	return exitOK
}

// settingsDestination refuses a --path that is not a file this command may
// write: an existing file unless --overwrite, or anything but a regular file.
func settingsDestination(path string, overwrite bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read the destination: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("the destination exists and is not a regular file")
	}
	if !overwrite {
		return errors.New("the destination exists; choose another --path or pass --overwrite")
	}
	return nil
}

func (r settingsReader) read(account string, at time.Time) (settingsExport, error) {
	export := settingsExport{Format: settingsFormat, ExportedAt: at.Format(time.RFC3339)}
	prefix := "/api/v1/accounts/" + account
	var folders []settingsFolder
	for _, part := range []struct {
		name, path  string
		into        any
		mayBeAbsent bool
	}{
		{name: "the account", path: prefix, into: &export.Account},
		{name: "folders", path: prefix + "/folders", into: &folders},
		// The Sieve route answers 404 not_found for an account with no script.
		// That is the same answer as for an account the key cannot read, which
		// is why the account is read first and the vacation route, under the
		// same check, after it: losing access still fails the export.
		{name: "the Sieve script", path: prefix + "/sieve", into: &export.Sieve, mayBeAbsent: true},
		{name: "the vacation reply", path: prefix + "/vacation", into: &export.Vacation},
		{name: "quiet hours", path: prefix + "/quiet-hours", into: &export.QuietHours},
		{name: "the contact reply-time setting", path: prefix + "/contact-reply-time", into: &export.ContactReplyTime},
	} {
		err := r.get(part.path, part.into)
		var refused settingsRefusal
		if part.mayBeAbsent && errors.As(err, &refused) && refused.notStored() {
			continue
		}
		if err != nil {
			return settingsExport{}, fmt.Errorf("read %s: %w", part.name, err)
		}
	}
	if export.Account.Address == "" {
		return settingsExport{}, errors.New("read the account: the server returned no address")
	}
	export.Folders = folders
	if export.Folders == nil {
		export.Folders = []settingsFolder{}
	}
	for _, d := range []**time.Time{&export.Vacation.StartDate, &export.Vacation.EndDate} {
		if *d != nil {
			utc := (*d).UTC()
			*d = &utc
		}
	}
	return export, nil
}

// settingsRefusal is a read the server answered with something other than 200.
type settingsRefusal struct {
	status int
	code   string
}

func (e settingsRefusal) Error() string {
	return fmt.Sprintf("the server refused (HTTP %d); check the account and that the key holds mail:read", e.status)
}

// notStored reports the API's own 404 for something not there, which a proxy's
// or a missing route's 404 is not.
func (e settingsRefusal) notStored() bool {
	return e.status == http.StatusNotFound && e.code == "not_found"
}

// get reads one {"data": ...} response into into. A null is not settings: taken
// as an answer it would write the defaults over what the account really has.
func (r settingsReader) get(path string, into any) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.base.String()+path, nil)
	if err != nil {
		return errors.New("could not prepare the request")
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.client.Do(req)
	if err != nil {
		return errors.New("the request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var refusal struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, settingsMaxResponse)).Decode(&refusal)
		return settingsRefusal{status: resp.StatusCode, code: refusal.Error.Code}
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, settingsMaxResponse)).Decode(&envelope); err != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("the server returned an invalid response")
	}
	if err := json.Unmarshal(envelope.Data, into); err != nil {
		return errors.New("the server returned an invalid response")
	}
	return nil
}

// writeSettingsFile writes the export beside path and renames it into place,
// so path is never seen half written.
func writeSettingsFile(path string, export settingsExport) error {
	body, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".restmail-export-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, writeErr := tmp.Write(append(body, '\n'))
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
