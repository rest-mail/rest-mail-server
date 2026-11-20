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

RESTMAIL_API_KEY needs mail:read and mail:export on the account or its domain.
Every account holds both on its own mailbox, and an owner on the accounts it
owns; an administrator's grants to read mail do not include mail:export. The
server records the export's start before anything is written, and its end,
completed or aborted, with the items and bytes written.

  --server URL           https; http only for localhost
  --account-id ID        the account to export
  --path PATH            destination; published only when the export completes
  --overwrite            replace an existing contacts file (contacts only)
  --max-message-bytes N  maximum calendar message source (default/maximum 1 GiB)
  --skip-folder NAME     leave this folder out (calendar only; repeatable)
  --json                 one summary on stdout; one error on stderr

Contacts are a UTF-8 vCard 4.0 file, one card at a time from paginated reads.
Calendar output is a new directory of .ics files, preserving the original
decoded text/calendar parts, including invitation methods, recurrence and
timezones. Messages are read one at a time, including snoozed and IMAP-deleted
mail. A folder listing page over 64 MiB (the listing carries message bodies)
is read again in smaller pages, down to one message. A folder that still
cannot be exported stops the export with its name and the narrower command
that would succeed: the same export with --skip-folder for that folder. Output is private to its owner; credentials and content are never printed.
Neither export is a point-in-time snapshot: avoid concurrent account changes.
See docs/exporting-data.md.

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
	var skip folderNames
	flags.Var(&skip, "skip-folder", "leave a folder out")
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
	if flags.NArg() != 0 || *path == "" || *account == 0 || *account > 1<<32-1 || *maxBytes < 1 || *maxBytes > 1<<30 || (!contacts && *overwrite) || (contacts && len(skip) > 0) {
		return result.fail(exitUsage, "bad_flag", "give --path, --account-id (1–4294967295), and a message limit between 1 byte and 1 GiB; --overwrite is contacts-only, --skip-folder calendar-only")
	}
	base, err := serverURL(*server)
	if err != nil {
		return result.fail(exitUsage, "bad_flag", err.Error())
	}
	if token == "" {
		return result.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY with mail:read and mail:export on the account")
	}
	if err := settingsDestination(*path, *overwrite); err != nil {
		return result.fail(exitUsage, "bad_destination", err.Error())
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	exporter := &mailExporter{ctx: ctx, client: &noRedirect, base: base, token: token, account: *account, maxBytes: *maxBytes,
		skip: skipSet(skip), pageBytes: exportListPageBytes}
	data, format := "calendar", "icalendar"
	if contacts {
		data, format = "contacts", "vcard"
	}
	session, err := openExportSession(ctx, &noRedirect, base, token, *account, data, format)
	if err != nil {
		return result.fail(exitFailed, "export_failed", err.Error())
	}
	var count int
	if contacts {
		err = exportContactsFile(exporter, *path, *overwrite, &count)
	} else {
		err = exportCalendarDirectory(exporter, *path, &count)
	}
	err = session.end(err, int64(count), exporter.written, exporter.skipped)
	if err != nil {
		return result.fail(exitFailed, "export_failed", err.Error())
	}
	kind := "calendars"
	if contacts {
		kind = "contacts"
	}
	if result.json {
		summary := map[string]any{kind: count, "path": *path}
		if !contacts {
			summary["skipped_folders"] = exporter.skippedFolders()
		}
		_ = json.NewEncoder(stdout).Encode(summary)
	} else {
		_, _ = fmt.Fprintf(stdout, "Exported %d %s to %s.\n", count, kind, *path)
		if len(exporter.skipped) > 0 {
			_, _ = fmt.Fprintf(stdout, "Left out, as asked: %s.\n", strings.Join(exporter.skipped, ", "))
		}
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
	if info, err := os.Stat(file.Name()); err == nil {
		exporter.written = info.Size()
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
	var names []string
	folders, _, err := exportDataList(exporter, prefix+"/folders", false, func(folder exportedFolder) error {
		if folder.Name == "" {
			return errors.New("the server returned an unnamed folder")
		}
		if !exporter.skips(folder.Name) {
			names = append(names, folder.Name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if folders.HasMore {
		return errUnlistedFolders
	}
	if err := exporter.unknownSkips(); err != nil {
		return err
	}
	for _, name := range names {
		if err := exportCalendarFolder(exporter, directory, prefix, name, count); err != nil {
			return folderFailed(name, err)
		}
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("the calendar destination appeared during export; it was not replaced")
	}
	return os.Rename(directory, path)
}

// calendarListed is what the calendar export needs of a listed message.
type calendarListed struct {
	ID             uint              `json:"id"`
	RawSize        int64             `json:"raw_size"`
	CalendarEvents []json.RawMessage `json:"calendar_events"`
}

// exportCalendarFolder reads a folder's listing a page at a time, keeping only
// the ids of its calendar messages, and downloads those once the page is
// closed. A page over the exporter's page limit is asked for again with half
// as many messages, from the same cursor, so nothing is exported twice.
func exportCalendarFolder(exporter *mailExporter, directory, prefix, folder string, count *int) error {
	route := prefix + "/folders/" + url.PathEscape(folder) + "/messages?include_imap_deleted=true&include_snoozed=true"
	cursor := ""
	cursors := pageCursors{}
	limit := 100
	for {
		var ids []uint
		next := route + "&limit=" + strconv.Itoa(limit) + "&cursor=" + url.QueryEscape(cursor)
		page, rows, err := exportDataList(exporter, next, true, func(message calendarListed) error {
			if len(message.CalendarEvents) == 0 {
				return nil
			}
			if message.ID == 0 || message.RawSize > exporter.maxBytes {
				return errors.New("a calendar message has no id or exceeds --max-message-bytes")
			}
			ids = append(ids, message.ID)
			return nil
		})
		if errors.Is(err, errPageTooLarge) {
			if limit == 1 {
				return fmt.Errorf("%w, even with one message to the page", err)
			}
			limit /= 2
			continue
		}
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := exportCalendarMessage(exporter, directory, id, count); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		if cursor, err = cursors.next(page, rows); err != nil {
			return err
		}
	}
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
		n, copyErr := io.Copy(output, part.Body)
		if err := errors.Join(copyErr, output.Sync(), output.Close()); err != nil {
			return err
		}
		exporter.written += n
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
	limit := exporter.pageBytes
	if limit <= 0 {
		limit = exportListPageBytes
	}
	capped := &cappedReader{r: response.Body, left: limit}
	invalidList := errors.New("the server returned an invalid export list")
	// invalid is a list that is not one, or one over the page limit, which is
	// told apart so a caller can ask for a smaller page.
	invalid := func() error {
		if capped.hit {
			return fmt.Errorf("%w of %d bytes", errPageTooLarge, limit)
		}
		return invalidList
	}
	decoder := json.NewDecoder(capped)
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return page, 0, invalid()
	}
	seenData, seenPage, rows := false, false, 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return page, rows, invalid()
		}
		switch key {
		case "data":
			if token, err := decoder.Token(); err != nil || token != json.Delim('[') || seenData {
				return page, rows, invalid()
			}
			seenData = true
			for decoder.More() {
				var item T
				if err := decoder.Decode(&item); err != nil {
					return page, rows, invalid()
				}
				if err := each(item); err != nil {
					return page, rows, err
				}
				rows++
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
				return page, rows, invalid()
			}
		case "pagination":
			var metadata *respond.Pagination
			if err := decoder.Decode(&metadata); err != nil || metadata == nil || seenPage {
				return page, rows, invalid()
			}
			page, seenPage = *metadata, true
		default:
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return page, rows, invalid()
			}
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || !seenData || (paginated && !seenPage) {
		return page, rows, invalid()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return page, rows, invalid()
	}
	return page, rows, nil
}

// errPageTooLarge is a list response over the exporter's page limit.
var errPageTooLarge = errors.New("a page of the listing is over the page limit")

// cappedReader reads at most left bytes, and says whether it stopped there.
type cappedReader struct {
	r    io.Reader
	left int64
	hit  bool
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		// One byte past the limit tells a page that ends at it from one over it.
		var probe [1]byte
		for {
			n, err := c.r.Read(probe[:])
			if n > 0 {
				c.hit = true
			}
			if n > 0 || err != nil {
				return 0, io.EOF
			}
		}
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
