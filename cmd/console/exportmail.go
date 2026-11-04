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

RESTMAIL_API_KEY needs mail:read on the account or its domain. Every account holds
mail:read on its own mailbox. The output is the layout restmail import-maildir
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
  --json                 one summary on stdout; one error on stderr

Messages are downloaded one at a time to the folder's tmp/ and renamed into
cur/ when complete, so a message file is never seen half written. Folder names
containing "." are refused before anything is written: Maildir++ cannot tell
them apart from subfolders. Raw messages and credentials are never printed.
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
}

func runExportMaildir(ctx context.Context, args []string, token string, client *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export-maildir", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "server URL")
	account := fs.Uint64("account-id", 0, "account to export")
	path := fs.String("path", "", "destination directory")
	allowNonEmpty := fs.Bool("allow-non-empty", false, "write into a non-empty destination")
	limit := fs.Int64("max-message-bytes", 1<<30, "maximum stored message bytes")
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
		return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY with mail:read on the account")
	}
	if err := exportDestination(*path, *allowNonEmpty); err != nil {
		return o.fail(exitUsage, "bad_destination", err.Error())
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	e := &mailExporter{ctx: ctx, client: &noRedirect, base: base, token: token, account: *account, maxBytes: *limit}
	folders, err := e.run(*path)
	if err != nil {
		return o.fail(exitFailed, "export_failed", fmt.Sprintf("%s (exported %d messages)", err, e.messages))
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"folders": folders, "messages": e.messages, "bytes": e.bytes})
	} else {
		fmt.Fprintf(stdout, "Exported %d messages in %d folders to %s.\n", e.messages, folders, *path)
	}
	return exitOK
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
		return "", fmt.Errorf("folder %q %s", name, problem)
	}
	return mailimport.MaildirDirectory(rmail.CanonicalFolder(name))
}

func (e *mailExporter) run(root string) (int, error) {
	var folders []exportedFolder
	if _, err := listOf(e, "/api/v1/accounts/"+strconv.FormatUint(e.account, 10)+"/folders", func(f exportedFolder) error {
		folders = append(folders, f)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("list folders: %w", err)
	}
	// Every folder is checked before anything is written.
	dirs := make(map[string]string, len(folders))
	for _, folder := range folders {
		dir, err := maildirFolder(folder.Name)
		if err != nil {
			return 0, err
		}
		if other, ok := dirs[dir]; ok {
			return 0, fmt.Errorf("folders %q and %q would share Maildir folder %s", other, folder.Name, dir)
		}
		dirs[dir] = folder.Name
	}
	for _, folder := range folders {
		dir, _ := maildirFolder(folder.Name)
		if err := e.exportFolder(filepath.Join(root, dir), folder.Name); err != nil {
			return 0, fmt.Errorf("folder %q: %w", folder.Name, err)
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
		pagination, err := listOf(e, next, func(m exportedMessage) error {
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
		if !pagination.HasMore || pagination.Cursor == "" || pagination.Cursor == cursor {
			return nil
		}
		cursor = pagination.Cursor
	}
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
// a page is never held whole.
func listOf[T any](e *mailExporter, path string, each func(T) error) (respond.Pagination, error) {
	var page respond.Pagination
	resp, err := e.get(path)
	if err != nil {
		return page, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return page, fmt.Errorf("the server refused (HTTP %d); check the account and that the key holds mail:read", resp.StatusCode)
	}
	invalid := errors.New("the server returned an invalid list")
	dec := json.NewDecoder(resp.Body)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return page, invalid
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return page, invalid
		}
		switch tok {
		case "data":
			if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
				return page, invalid
			}
			for dec.More() {
				var item T
				if err := dec.Decode(&item); err != nil {
					return page, invalid
				}
				if err := each(item); err != nil {
					return page, err
				}
			}
			if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
				return page, invalid
			}
		case "pagination":
			if err := dec.Decode(&page); err != nil {
				return page, invalid
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return page, invalid
			}
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return page, invalid
	}
	return page, nil
}
