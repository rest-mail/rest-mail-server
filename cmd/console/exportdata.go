package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gomessage "github.com/emersion/go-message"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
)

const exportDataUsage = `restmail %s — export account data through the public API

  restmail %s --server https://mail.example.com --account-id 12 --path %s

RESTMAIL_API_KEY needs mail:read on the account or its domain. The existing
public API checks access; this command grants no new administrator authority.

  --server URL           https; http only for localhost
  --account-id ID        the account to export
  --path PATH            destination; published only when the export completes
  --overwrite            replace an existing contacts file (contacts only)
  --max-message-bytes N  maximum calendar message source (default/maximum 1 GiB)
  --json                 one summary on stdout; one error on stderr

Contacts are a UTF-8 vCard 4.0 file, one card at a time from paginated reads.
Calendar output is a new directory of .ics files, preserving the original
decoded text/calendar parts, including invitation methods, recurrence and
timezones. Messages are read one at a time, including snoozed and IMAP-deleted
mail. Output is private to its owner; credentials and content are never printed.
Neither export is a point-in-time snapshot: avoid concurrent account changes.
These reads do not create an export audit entry. See docs/exporting-data.md.

Exit status: 0 complete, 2 usage, 1 export failed.
`

type exportedContact struct {
	ID         uint      `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	TrustLevel string    `json:"trust_level"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func runExportData(ctx context.Context, command string, args []string, token string, client *http.Client, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	server := flags.String("server", "", "server URL")
	account := flags.Uint64("account-id", 0, "account to export")
	path := flags.String("path", "", "destination")
	overwrite := flags.Bool("overwrite", false, "replace an existing contacts file")
	maxBytes := flags.Int64("max-message-bytes", 1<<30, "maximum calendar message bytes")
	asJSON := flags.Bool("json", false, "JSON output")
	help := flags.Bool("help", false, "show help")
	result := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := flags.Parse(args); err != nil {
		return result.fail(exitUsage, "bad_flag", err.Error())
	}
	result.json = *asJSON
	contacts := command == "export-contacts"
	if *help {
		example := "./calendar"
		if contacts {
			example = "./contacts.vcf"
		}
		_, _ = fmt.Fprintf(stdout, exportDataUsage, command, command, example)
		return exitOK
	}
	if flags.NArg() != 0 || *path == "" || *account == 0 || *account > 1<<32-1 || *maxBytes < 1 || *maxBytes > 1<<30 || (!contacts && *overwrite) {
		return result.fail(exitUsage, "bad_flag", "give --path, --account-id (1–4294967295), and a message limit between 1 byte and 1 GiB; --overwrite is contacts-only")
	}
	base, err := serverURL(*server)
	if err != nil {
		return result.fail(exitUsage, "bad_flag", err.Error())
	}
	if token == "" {
		return result.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY with mail:read on the account")
	}
	if err := settingsDestination(*path, *overwrite); err != nil {
		return result.fail(exitUsage, "bad_destination", err.Error())
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	exporter := &mailExporter{ctx: ctx, client: &noRedirect, base: base, token: token, account: *account, maxBytes: *maxBytes}
	var count int
	if contacts {
		err = exportContactsFile(exporter, *path, *overwrite, &count)
	} else {
		err = exportCalendarDirectory(exporter, *path, &count)
	}
	if err != nil {
		return result.fail(exitFailed, "export_failed", err.Error())
	}
	kind := "calendars"
	if contacts {
		kind = "contacts"
	}
	if result.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{kind: count, "path": *path})
	} else {
		_, _ = fmt.Fprintf(stdout, "Exported %d %s to %s.\n", count, kind, *path)
	}
	return exitOK
}

func exportContactsFile(exporter *mailExporter, path string, overwrite bool, count *int) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".restmail-export-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	for offset := 0; ; {
		route := fmt.Sprintf("/api/v1/accounts/%d/contacts?limit=100&offset=%d", exporter.account, offset)
		page, rows, err := exportDataList(exporter, route, true, func(contact exportedContact) error {
			if err := writeVCard(file, exporter, contact); err != nil {
				return err
			}
			*count += 1
			return nil
		})
		if err != nil {
			return fmt.Errorf("list contacts: %w", err)
		}
		if !page.HasMore {
			break
		}
		if rows == 0 {
			return errors.New("the server returned an empty contacts page with more to follow")
		}
		offset += rows
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if overwrite {
		return os.Rename(file.Name(), path)
	}
	return os.Link(file.Name(), path)
}

func writeVCard(writer io.Writer, exporter *mailExporter, contact exportedContact) error {
	address, err := mail.ParseAddress(contact.Email)
	if contact.ID == 0 || err != nil || address.Address != contact.Email {
		return errors.New("the server returned a contact without an id or valid email")
	}
	name := contact.Name
	if name == "" {
		name = contact.Email
	}
	lines := []string{"BEGIN:VCARD", "VERSION:4.0", "UID:" + exporter.base.String() + fmt.Sprintf("/api/v1/accounts/%d/contacts/%d", exporter.account, contact.ID),
		"FN:" + vcardText(name), "EMAIL:" + vcardText(contact.Email)}
	for _, field := range []struct{ name, value string }{
		{"X-RESTMAIL-TRUST-LEVEL", contact.TrustLevel}, {"X-RESTMAIL-SOURCE", contact.Source},
	} {
		if field.value != "" {
			lines = append(lines, field.name+":"+vcardText(field.value))
		}
	}
	if !contact.CreatedAt.IsZero() {
		lines = append(lines, "X-RESTMAIL-CREATED-AT:"+contact.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	if !contact.UpdatedAt.IsZero() {
		lines = append(lines, "REV:"+contact.UpdatedAt.UTC().Format("20060102T150405Z"), "X-RESTMAIL-UPDATED-AT:"+contact.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	for _, line := range append(lines, "END:VCARD") {
		if err := writeVCardLine(writer, line); err != nil {
			return err
		}
	}
	return nil
}

func vcardText(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	return strings.NewReplacer("\\", "\\\\", ";", "\\;", ",", "\\,", "\n", "\\n").Replace(value)
}

func writeVCardLine(writer io.Writer, line string) error {
	if !utf8.ValidString(line) || strings.ContainsFunc(line, func(character rune) bool {
		return character < 0x20 && character != '\t' || character == 0x7f
	}) {
		return errors.New("the server returned invalid vCard text")
	}
	width := 75
	for len(line) > width {
		end := width
		for !utf8.RuneStart(line[end]) {
			end--
		}
		if _, err := io.WriteString(writer, line[:end]+"\r\n "); err != nil {
			return err
		}
		line, width = line[end:], 74
	}
	_, err := io.WriteString(writer, line+"\r\n")
	return err
}

func exportCalendarDirectory(exporter *mailExporter, path string, count *int) error {
	directory, err := os.MkdirTemp(filepath.Dir(path), "."+filepath.Base(path)+".restmail-export-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	prefix := "/api/v1/accounts/" + strconv.FormatUint(exporter.account, 10)
	_, _, err = exportDataList(exporter, prefix+"/folders", false, func(folder exportedFolder) error {
		if folder.Name == "" {
			return errors.New("the server returned an unnamed folder")
		}
		route := prefix + "/folders/" + url.PathEscape(folder.Name) + "/messages?limit=100&include_imap_deleted=true&include_snoozed=true"
		cursor := ""
		for {
			next := route + "&cursor=" + url.QueryEscape(cursor)
			page, rows, err := exportDataList(exporter, next, true, func(message struct {
				ID             uint              `json:"id"`
				RawSize        int64             `json:"raw_size"`
				CalendarEvents []json.RawMessage `json:"calendar_events"`
			}) error {
				if len(message.CalendarEvents) == 0 {
					return nil
				}
				if message.ID == 0 || message.RawSize > exporter.maxBytes {
					return errors.New("a calendar message has no id or exceeds --max-message-bytes")
				}
				return exportCalendarMessage(exporter, directory, message.ID, count)
			})
			if err != nil {
				return err
			}
			if !page.HasMore {
				return nil
			}
			if rows == 0 || page.Cursor == "" || page.Cursor == cursor {
				return errors.New("the server returned a calendar message page without an advancing cursor")
			}
			cursor = page.Cursor
		}
	})
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("the calendar destination appeared during export; it was not replaced")
	}
	return os.Rename(directory, path)
}

func exportCalendarMessage(exporter *mailExporter, directory string, id uint, count *int) error {
	source := filepath.Join(directory, ".source")
	defer func() { _ = os.Remove(source) }()
	if _, err := exporter.download(id, source); err != nil {
		return fmt.Errorf("calendar message %d: %w", id, err)
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	entity, err := gomessage.Read(file)
	if err != nil {
		return fmt.Errorf("calendar message %d has invalid MIME or an unsupported encoding", id)
	}
	parts, calendars := 0, 0
	err = entity.Walk(func(partPath []int, part *gomessage.Entity, partErr error) error {
		parts++
		if len(partPath) > rmime.DefaultMaxNestingDepth || parts > rmime.DefaultMaxParts {
			return errors.New("calendar message exceeds the MIME structure limit")
		}
		if partErr != nil {
			return partErr
		}
		mediaType, _, err := part.Header.ContentType()
		if err != nil {
			return err
		}
		if !strings.EqualFold(mediaType, "text/calendar") {
			return nil
		}
		calendars++
		path := filepath.Join(directory, fmt.Sprintf("%d-%d.ics", id, calendars))
		output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, part.Body)
		if err := errors.Join(copyErr, output.Sync(), output.Close()); err != nil {
			return err
		}
		*count += 1
		return nil
	})
	if err != nil {
		return fmt.Errorf("calendar message %d: could not decode or write its MIME parts", id)
	}
	if calendars == 0 {
		return fmt.Errorf("calendar message %d has no text/calendar source part", id)
	}
	return nil
}

func exportDataList[T any](exporter *mailExporter, path string, paginated bool, each func(T) error) (respond.Pagination, int, error) {
	var page respond.Pagination
	response, err := exporter.get(path)
	if err != nil {
		return page, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return page, 0, fmt.Errorf("the server refused (HTTP %d); check the account and that the key holds mail:read", response.StatusCode)
	}
	invalid := errors.New("the server returned an invalid export list (maximum page size 64 MiB)")
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<20))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return page, 0, invalid
	}
	seenData, seenPage, rows := false, false, 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return page, rows, invalid
		}
		switch key {
		case "data":
			if token, err := decoder.Token(); err != nil || token != json.Delim('[') || seenData {
				return page, rows, invalid
			}
			seenData = true
			for decoder.More() {
				var item T
				if err := decoder.Decode(&item); err != nil {
					return page, rows, invalid
				}
				if err := each(item); err != nil {
					return page, rows, err
				}
				rows++
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
				return page, rows, invalid
			}
		case "pagination":
			var metadata *respond.Pagination
			if err := decoder.Decode(&metadata); err != nil || metadata == nil || seenPage {
				return page, rows, invalid
			}
			page, seenPage = *metadata, true
		default:
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return page, rows, invalid
			}
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || !seenData || (paginated && !seenPage) {
		return page, rows, invalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return page, rows, invalid
	}
	return page, rows, nil
}
