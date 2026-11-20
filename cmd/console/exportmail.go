package main

import (
	"bufio"
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/mailimport"
)

const exportMailUsage = `restmail export-maildir — export an account's mail as a Maildir through the public API

  restmail export-maildir --server https://mail.example.com --account-id 12 --path ./Maildir

RESTMAIL_API_KEY needs mail:read and mail:export on the account or its domain.
Every account holds both on its own mailbox, and an owner on the accounts it
owns; an administrator's grants to read mail do not include mail:export. The
server records the export's start before anything is written, and its end,
completed or aborted, with the messages and bytes written. The output is the layout restmail import-maildir
reads: INBOX is the root, folder Archive/2020 is the Maildir++ folder
.Archive.2020 (each level in modified UTF-7, as Dovecot names it), each message is its stored source byte for byte in cur/, its flags
are the filename's ":2," letters (S Seen, R Answered, F Flagged, D Draft,
T Deleted), its keywords are letters a–z mapped by that folder's
dovecot-keywords file, and its internal date is the file's modification time.

  --server URL           https; http only for localhost
  --account-id ID        the account to export
  --path PATH            destination directory; created when missing
  --allow-non-empty      write into a destination that already holds files
  --max-message-bytes N  refuse a stored message larger than this (default and maximum 1 GiB)
  --skip-folder NAME     leave this folder out (repeatable); the summary names it
  --json                 one summary on stdout; one error on stderr

Messages are downloaded one at a time to the folder's tmp/ and renamed into
cur/ when complete, so a message file is never seen half written. Folder names
containing "." are refused before anything is written: Maildir++ cannot tell
them apart from subfolders. A folder that cannot be exported stops the export
with its name and the narrower command that would succeed: the same export
with --skip-folder for that folder, into an empty destination. Raw messages and credentials are never printed.
See docs/migrating-mail.md.

Exit status: 0 complete, 2 usage, 1 export failed.
`

// exportedMessage is what export needs of a listed message: never its bodies.
type exportedMessage struct {
	ID          uint      `json:"id"`
	RawSize     int64     `json:"raw_size"`
	IsRead      bool      `json:"is_read"`
	IsAnswered  bool      `json:"is_answered"`
	IsFlagged   bool      `json:"is_flagged"`
	IsStarred   bool      `json:"is_starred"`
	IsDraft     bool      `json:"is_draft"`
	IMAPDeleted bool      `json:"imap_deleted"`
	Keywords    []string  `json:"keywords"`
	ReceivedAt  time.Time `json:"received_at"`
}

type exportedFolder struct {
	Name string `json:"name"`
}

type mailExporter struct {
	ctx      context.Context
	client   *http.Client
	base     *url.URL
	token    string
	account  uint64
	maxBytes int64
	messages int
	bytes    int64
	// skip holds the canonical names of the folders --skip-folder leaves out,
	// and skipped the ones the account turned out to hold, as it listed them.
	skip    map[string]bool
	skipped []string
	// pageBytes bounds one list response that carries message bodies
	// (exportListPageBytes); a page over it is asked for again in smaller pages.
	pageBytes int64
	// written counts the bytes the calendar export writes.
	written int64
}

// exportListPageBytes bounds one list response an export reads. The folder
// listing carries each message's decoded bodies, so a page of 100 large
// messages can pass it; such a page is read again in smaller pages, down to
// one message, before the export gives up on its folder. A variable only so a
// test reaches the smaller pages with a few small messages.
var exportListPageBytes int64 = 64 << 20

// folderNames is a repeatable flag of folder names.
type folderNames []string

func (f *folderNames) String() string { return strings.Join(*f, ",") }

func (f *folderNames) Set(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("--skip-folder needs a folder name")
	}
	*f = append(*f, name)
	return nil
}

// skipSet is the canonical names of the folders to leave out.
func skipSet(names folderNames) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[rmail.CanonicalFolder(name)] = true
	}
	return set
}

// skips reports whether the export leaves folder out, and records it.
func (e *mailExporter) skips(folder string) bool {
	if !e.skip[rmail.CanonicalFolder(folder)] {
		return false
	}
	e.skipped = append(e.skipped, folder)
	return true
}

// unknownSkips refuses a --skip-folder the account has no folder for: a typo
// would otherwise export the folder it meant to leave out.
func (e *mailExporter) unknownSkips() error {
	found := map[string]bool{}
	for _, name := range e.skipped {
		found[rmail.CanonicalFolder(name)] = true
	}
	for name := range e.skip {
		if !found[name] {
			return fmt.Errorf("--skip-folder %q names no folder of the account", name)
		}
	}
	return nil
}

// folderFailed names the folder an export could not finish and the narrower
// request that would: the same export without that folder.
func folderFailed(folder string, err error) error {
	return &folderError{folder: folder, err: err}
}

type folderError struct {
	folder string
	err    error
}

func (e *folderError) Error() string {
	return fmt.Sprintf("folder %q could not be exported: %v; the rest of the account exports with --skip-folder %s", e.folder, e.err, strconv.Quote(e.folder))
}

func (e *folderError) Unwrap() error { return e.err }

func runExportMaildir(ctx context.Context, args []string, token string, client *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export-maildir", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "server URL")
	account := fs.Uint64("account-id", 0, "account to export")
	path := fs.String("path", "", "destination directory")
	allowNonEmpty := fs.Bool("allow-non-empty", false, "write into a non-empty destination")
	limit := fs.Int64("max-message-bytes", 1<<30, "maximum stored message bytes")
	var skip folderNames
	fs.Var(&skip, "skip-folder", "leave a folder out")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, exportMailUsage)
		return exitOK
	}
	if fs.NArg() != 0 || *path == "" || *account == 0 || *account > 1<<32-1 || *limit < 1 || *limit > 1<<30 {
		return o.fail(exitUsage, "bad_flag", "give --path, --account-id (1–4294967295) and a message limit between 1 byte and 1 GiB")
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	if token == "" {
		return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY with mail:read and mail:export on the account")
	}
	if err := exportDestination(*path, *allowNonEmpty); err != nil {
		return o.fail(exitUsage, "bad_destination", err.Error())
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	e := &mailExporter{ctx: ctx, client: &noRedirect, base: base, token: token, account: *account, maxBytes: *limit,
		skip: skipSet(skip), pageBytes: exportListPageBytes}
	session, err := openExportSession(ctx, &noRedirect, base, token, *account, "mail", "maildir")
	if err != nil {
		return o.fail(exitFailed, "export_failed", err.Error())
	}
	folders, err := e.run(*path)
	err = session.end(err, int64(e.messages), e.bytes, e.skipped)
	if err != nil {
		return o.fail(exitFailed, "export_failed", fmt.Sprintf("%s (exported %d messages)", err, e.messages))
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"folders": folders, "messages": e.messages, "bytes": e.bytes, "skipped_folders": e.skippedFolders()})
	} else {
		fmt.Fprintf(stdout, "Exported %d messages in %d folders to %s.\n", e.messages, folders, *path)
		if len(e.skipped) > 0 {
			fmt.Fprintf(stdout, "Left out, as asked: %s.\n", strings.Join(e.skipped, ", "))
		}
	}
	return exitOK
}

// skippedFolders is the folders left out, never null in JSON.
func (e *mailExporter) skippedFolders() []string {
	if e.skipped == nil {
		return []string{}
	}
	return e.skipped
}

// exportDestination makes the destination directory, refusing one that holds
// files unless allowed: an export never mixes itself into other mail unasked.
func exportDestination(path string, allowNonEmpty bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(path, 0o700)
	}
	if err != nil {
		return fmt.Errorf("cannot read the destination: %w", err)
	}
	if !info.IsDir() {
		return errors.New("the destination is not a directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("cannot read the destination: %w", err)
	}
	if len(entries) > 0 && !allowNonEmpty {
		return errors.New("the destination is not empty; choose an empty directory or pass --allow-non-empty")
	}
	return nil
}

// maildirFolder is where import-maildir finds a folder: INBOX is the root and
// every other folder a Maildir++ dot folder, "/" becoming "." and each level
// in modified UTF-7, as Dovecot names them.
func maildirFolder(name string) (string, error) {
	if problem := rmail.FolderNameProblem(name); problem != "" {
		return "", fmt.Errorf("its name %s", problem)
	}
	return mailimport.MaildirDirectory(rmail.CanonicalFolder(name))
}

func (e *mailExporter) run(root string) (int, error) {
	var folders []exportedFolder
	page, _, err := listOf(e, "/api/v1/accounts/"+strconv.FormatUint(e.account, 10)+"/folders", false, func(f exportedFolder) error {
		if !e.skips(f.Name) {
			folders = append(folders, f)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("list folders: %w", err)
	}
	if page.HasMore {
		return 0, errUnlistedFolders
	}
	if err := e.unknownSkips(); err != nil {
		return 0, err
	}
	// Every folder is checked before anything is written.
	dirs := make(map[string]string, len(folders))
	for _, folder := range folders {
		dir, err := maildirFolder(folder.Name)
		if err != nil {
			return 0, folderFailed(folder.Name, err)
		}
		if other, ok := dirs[dir]; ok {
			return 0, fmt.Errorf("folders %q and %q would share Maildir folder %s", other, folder.Name, dir)
		}
		dirs[dir] = folder.Name
	}
	for _, folder := range folders {
		dir, _ := maildirFolder(folder.Name)
		if err := e.exportFolder(filepath.Join(root, dir), folder.Name); err != nil {
			return 0, folderFailed(folder.Name, err)
		}
	}
	return len(folders), nil
}

func (e *mailExporter) exportFolder(dir, folder string) error {
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	if rmail.CanonicalFolder(folder) != "INBOX" {
		if err := os.WriteFile(filepath.Join(dir, "maildirfolder"), nil, 0o600); err != nil {
			return err
		}
	}
	keywords, err := readKeywordMap(dir)
	if err != nil {
		return err
	}
	seen := map[uint]bool{}
	cursors := pageCursors{}
	path := "/api/v1/accounts/" + strconv.FormatUint(e.account, 10) + "/folders/" + url.PathEscape(folder) + "/messages?limit=100&include_imap_deleted=true&include_snoozed=true"
	cursor := ""
	for {
		// A page's metadata is kept, never a message, so the list response is
		// closed before any source is downloaded.
		var page []exportedMessage
		next := path
		if cursor != "" {
			next += "&cursor=" + url.QueryEscape(cursor)
		}
		pagination, rows, err := listOf(e, next, true, func(m exportedMessage) error {
			page = append(page, m)
			return nil
		})
		if err != nil {
			return fmt.Errorf("list messages: %w", err)
		}
		for _, message := range page {
			if seen[message.ID] {
				continue
			}
			seen[message.ID] = true
			if err := e.exportMessage(dir, message, keywords); err != nil {
				return fmt.Errorf("message %d: %w", message.ID, err)
			}
		}
		if !pagination.HasMore {
			return nil
		}
		if cursor, err = cursors.next(pagination, rows); err != nil {
			return fmt.Errorf("list messages: %w", err)
		}
	}
}

// errUnlistedFolders refuses a folder list that says it is not all there: the
// folder route has no next page to ask for.
var errUnlistedFolders = errors.New("the server listed only some of the folders")

// pageCursors holds the cursors a folder's listing has given. An export ends
// only on a page that says nothing follows, so a page that says more follows
// has to lead somewhere new: stopping there would report a partial folder as
// exported, and following a cursor already used would never stop.
type pageCursors map[string]bool

// next is the cursor of the page after one holding rows messages.
func (c pageCursors) next(page respond.Pagination, rows int) (string, error) {
	if rows == 0 || page.Cursor == "" || c[page.Cursor] {
		return "", errors.New("the server said more messages follow but gave no new page to read them from")
	}
	c[page.Cursor] = true
	return page.Cursor, nil
}

// keywordMap gives each keyword of a folder its dovecot-keywords letter.
type keywordMap struct {
	dir     string
	letters map[string]rune // lowercased keyword → letter
	names   []string        // index → keyword as first written
}

// readKeywordMap starts from a mapping already in the folder, so a second
// export into the same destination keeps the letters its files already use.
func readKeywordMap(dir string) (*keywordMap, error) {
	k := &keywordMap{dir: dir, letters: map[string]rune{}}
	file, err := os.Open(filepath.Join(dir, "dovecot-keywords"))
	if errors.Is(err, fs.ErrNotExist) {
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(io.LimitReader(file, 65536))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		index, err := strconv.Atoi(fields[0])
		if len(fields) != 2 || err != nil || index != len(k.names) || index > 25 {
			return nil, errors.New("the destination's dovecot-keywords file is not one this command can extend")
		}
		k.names = append(k.names, fields[1])
		k.letters[strings.ToLower(fields[1])] = rune('a' + index)
	}
	return k, scanner.Err()
}

// letter is a keyword's letter, writing the mapping first when it is new so
// no message file ever names a letter the folder does not map.
func (k *keywordMap) letter(keyword string) (rune, error) {
	if letter, ok := k.letters[strings.ToLower(keyword)]; ok {
		return letter, nil
	}
	if len(k.names) == 26 {
		return 0, fmt.Errorf("the folder has more than 26 keywords, which Maildir cannot name; keyword %q is not exported", keyword)
	}
	k.names = append(k.names, keyword)
	letter := rune('a' + len(k.names) - 1)
	k.letters[strings.ToLower(keyword)] = letter
	var b strings.Builder
	for i, name := range k.names {
		fmt.Fprintf(&b, "%d %s\n", i, name)
	}
	tmp := filepath.Join(k.dir, "tmp", "dovecot-keywords.restmail-export")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return 0, err
	}
	return letter, os.Rename(tmp, filepath.Join(k.dir, "dovecot-keywords"))
}

// maildirInfo is the ":2," letters of a message, in ASCII order as Maildir
// wants them.
func maildirInfo(m exportedMessage, keywords *keywordMap) (string, error) {
	var letters []rune
	for _, f := range []struct {
		set    bool
		letter rune
	}{{m.IsDraft, 'D'}, {m.IsFlagged || m.IsStarred, 'F'}, {m.IsAnswered, 'R'}, {m.IsRead, 'S'}, {m.IMAPDeleted, 'T'}} {
		if f.set {
			letters = append(letters, f.letter)
		}
	}
	for _, keyword := range m.Keywords {
		letter, err := keywords.letter(keyword)
		if err != nil {
			return "", err
		}
		if !slices.Contains(letters, letter) {
			letters = append(letters, letter)
		}
	}
	slices.Sort(letters)
	return string(letters), nil
}

func (e *mailExporter) exportMessage(dir string, m exportedMessage, keywords *keywordMap) error {
	if m.ID == 0 || m.ReceivedAt.IsZero() {
		return errors.New("the server listed a message without an id or a received date")
	}
	if m.RawSize > e.maxBytes {
		return fmt.Errorf("its stored source is %d bytes, over --max-message-bytes", m.RawSize)
	}
	info, err := maildirInfo(m, keywords)
	if err != nil {
		return err
	}
	unique := fmt.Sprintf("%d.M%d.restmail-export", m.ReceivedAt.Unix(), m.ID)
	tmp := filepath.Join(dir, "tmp", unique)
	size, err := e.download(m.ID, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if m.RawSize > 0 && size != m.RawSize {
		_ = os.Remove(tmp)
		return fmt.Errorf("downloaded %d bytes, but the server lists %d", size, m.RawSize)
	}
	// import-maildir, like Dovecot, reads the file's mtime as INTERNALDATE.
	if err := os.Chtimes(tmp, m.ReceivedAt, m.ReceivedAt); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	name := fmt.Sprintf("%s,S=%d:2,%s", unique, size, info)
	if err := os.Rename(tmp, filepath.Join(dir, "cur", name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	e.messages++
	e.bytes += size
	return nil
}

// download streams a message's stored source into a new file at path.
func (e *mailExporter) download(id uint, path string) (int64, error) {
	resp, err := e.get("/api/v1/messages/" + strconv.FormatUint(uint64(id), 10) + "/raw")
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return 0, errors.New("the server keeps no source for it (HTTP 404), so it cannot be exported byte for byte")
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the server refused its source (HTTP %d)", resp.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	size, copyErr := io.Copy(file, io.LimitReader(resp.Body, e.maxBytes+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	switch {
	case copyErr != nil:
		return 0, errors.New("the source download failed")
	case size > e.maxBytes:
		return 0, errors.New("its source is over --max-message-bytes")
	case size == 0:
		return 0, errors.New("the server returned an empty source")
	case syncErr != nil:
		return 0, syncErr
	}
	return size, closeErr
}

func (e *mailExporter) get(path string) (*http.Response, error) {
	if err := e.ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(e.ctx, http.MethodGet, e.base.String()+path, nil)
	if err != nil {
		return nil, errors.New("could not prepare the request")
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, errors.New("the request failed")
	}
	return resp, nil
}

// listOf reads one list response, decoding its data one element at a time so
// a page is never held whole, and answers its pagination and how many elements
// it held. A paginated list must say whether more follows: without that, its
// end cannot be told from a cut-off answer.
func listOf[T any](e *mailExporter, path string, paginated bool, each func(T) error) (respond.Pagination, int, error) {
	var page respond.Pagination
	resp, err := e.get(path)
	if err != nil {
		return page, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return page, 0, fmt.Errorf("the server refused (HTTP %d); check the account and that the key holds mail:read", resp.StatusCode)
	}
	invalid := errors.New("the server returned an invalid list")
	dec := json.NewDecoder(resp.Body)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return page, 0, invalid
	}
	seenData, seenPage, rows := false, false, 0
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return page, rows, invalid
		}
		switch tok {
		case "data":
			if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
				return page, rows, invalid
			}
			seenData = true
			for dec.More() {
				var item T
				if err := dec.Decode(&item); err != nil {
					return page, rows, invalid
				}
				if err := each(item); err != nil {
					return page, rows, err
				}
				rows++
			}
			if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
				return page, rows, invalid
			}
		case "pagination":
			var metadata *respond.Pagination
			if err := dec.Decode(&metadata); err != nil || metadata == nil {
				return page, rows, invalid
			}
			page, seenPage = *metadata, true
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return page, rows, invalid
			}
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') || !seenData || (paginated && !seenPage) {
		return page, rows, invalid
	}
	return page, rows, nil
}
