// Package mailimport reads migration sources without modifying them. Each
// callback receives one bounded original message and its source metadata.
package mailimport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	goimap "github.com/rest-mail/go-imap/pkg/imap"
	"io"
	"io/fs"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

type Visit func(source string, message apitypes.ImportMessageBody) error

// Maildir reads cur/new and Maildir++ dot folders in deterministic order. The
// source file's mtime is its Dovecot INTERNALDATE. tmp is never imported.
// Folder names are planned before any message is read: renamed, if not nil,
// hears each source folder imported under another name than its own.
func Maildir(ctx context.Context, directory string, maxBytes int64, visit Visit, renamed func(FolderRename)) error {
	if maxBytes < 1 || maxBytes > 1<<30 {
		return errors.New("message size limit must be between 1 byte and 1 GiB")
	}
	for _, name := range []string{"cur", "new"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("Maildir must contain cur and new directories")
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	folders, err := maildirFolderPlan(directory, renamed)
	if err != nil {
		return err
	}
	return filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source symlink is not supported: %s", path)
		}
		if entry.IsDir() {
			if entry.Name() == "tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		parent := filepath.Base(filepath.Dir(path))
		if parent != "cur" && parent != "new" {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("source message is not a regular file: %s", path)
		}
		rel, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		folder, planned := folders[filepath.ToSlash(filepath.Dir(filepath.Dir(rel)))]
		if !planned {
			return fmt.Errorf("source folder of %s changed during import; rerun to resume", rel)
		}
		keywords, err := maildirKeywords(root, filepath.Dir(filepath.Dir(rel)))
		if err != nil {
			return err
		}
		flags, err := maildirFlags(entry.Name(), keywords)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		file, err := root.Open(rel)
		if err != nil {
			return err
		}
		info, statErr := file.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
		closeErr := file.Close()
		if statErr != nil || readErr != nil || closeErr != nil {
			return fmt.Errorf("could not read source message %s", rel)
		}
		if int64(len(raw)) > maxBytes {
			return fmt.Errorf("source message exceeds the size limit: %s", rel)
		}
		return visit(rel, apitypes.ImportMessageBody{RawMessage: raw, Folder: folder, Flags: flags, InternalDate: info.ModTime()})
	})
}

// maildirFolderPlan maps every source folder directory (one holding cur or
// new, outside tmp) to its destination folder.
func maildirFolderPlan(directory string, renamed func(FolderRename)) (map[string]string, error) {
	var claims []folderClaim
	seen := make(map[string]bool)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == "tmp" {
			return filepath.SkipDir
		}
		if entry.Name() != "cur" && entry.Name() != "new" {
			return nil
		}
		rel, err := filepath.Rel(directory, filepath.Dir(path))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !seen[rel] {
			seen[rel] = true
			name, reason := maildirFolder(rel)
			claims = append(claims, folderClaim{source: rel, name: name, reason: reason})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return planFolders(claims, renamed), nil
}

func maildirFlags(name string, keywords map[rune]string) ([]string, error) {
	_, encoded, found := strings.Cut(name, ":2,")
	if !found {
		return nil, nil
	}
	var flags []string
	for _, flag := range encoded {
		value := map[rune]string{'S': `\Seen`, 'R': `\Answered`, 'F': `\Flagged`, 'D': `\Draft`, 'T': `\Deleted`}[flag]
		if value == "" {
			value = keywords[flag]
		}
		if value == "" {
			return nil, fmt.Errorf("Maildir flag %q has no keyword mapping", flag)
		}
		flags = append(flags, value)
	}
	return goimap.NormalizeImportFlags(flags)
}

// Mbox accepts From-framed rfc4155, mboxrd or mboxo. The caller chooses the
// escaping format explicitly; no Content-Length-framed variant is guessed.
func Mbox(ctx context.Context, source io.Reader, folder, format string, maxBytes int64, visit Visit) error {
	if format != "rfc4155" && format != "mboxrd" && format != "mboxo" {
		return errors.New("mbox format must be rfc4155, mboxrd or mboxo")
	}
	if maxBytes < 1 || maxBytes > 1<<30 {
		return errors.New("message size limit must be between 1 byte and 1 GiB")
	}
	if folder == "" {
		folder = "INBOX"
	}
	if problem := rmail.FolderNameProblem(folder); problem != "" {
		return fmt.Errorf("destination folder: %s", problem)
	}
	folder = rmail.CanonicalFolder(folder)
	r := bufio.NewReader(source)
	var raw []byte
	var date time.Time
	count := 0
	inBody := false
	flush := func() error {
		if date.IsZero() {
			return nil
		}
		// Remove only mbox's framing blank line, preserving the message's
		// own header/body separator, line endings and final body newline.
		boundary, width := bytes.Index(raw, []byte("\r\n\r\n")), 4
		if boundary < 0 {
			boundary, width = bytes.Index(raw, []byte("\n\n")), 2
		}
		if boundary >= 0 && boundary+width < len(raw) {
			if bytes.HasSuffix(raw, []byte("\r\n\r\n")) {
				raw = raw[:len(raw)-2]
			} else if bytes.HasSuffix(raw, []byte("\n\n")) {
				raw = raw[:len(raw)-1]
			}
		}
		if int64(len(raw)) > maxBytes {
			return fmt.Errorf("mbox message %d exceeds the size limit", count)
		}
		message, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("mbox message %d has invalid headers", count)
		}
		if _, present := message.Header["Content-Length"]; present {
			return errors.New("Content-Length-framed mbox is unsupported; export as From-framed mboxrd or RFC4155")
		}
		if _, present := message.Header["X-Imap"]; present {
			return errors.New("a Dovecot metadata-only mbox pseudo-message requires an IMAP export; it was not imported as mail")
		}
		var keywords []string
		for _, value := range message.Header["X-Keywords"] {
			keywords = append(keywords, strings.Fields(value)...)
		}
		if _, err := goimap.NormalizeImportFlags(keywords); err != nil {
			return fmt.Errorf("mbox keywords: %w", err)
		}
		for _, keyword := range keywords {
			if strings.Contains(keyword, `\`) {
				return errors.New("mbox keywords must not contain a backslash")
			}
		}
		// Header.Get sees only the first occurrence. Preserve supported flags
		// from every occurrence, and refuse unsupported metadata wherever it is.
		status := strings.Join(message.Header["Status"], "")
		xStatus := strings.Join(message.Header["X-Status"], "")
		for _, metadata := range []struct{ name, value, allowed string }{{"Status", status, "RO"}, {"X-Status", xStatus, "AFTD"}} {
			for _, flag := range metadata.value {
				if !strings.ContainsRune(metadata.allowed, flag) {
					return fmt.Errorf("mbox %s flag %q is unsupported; it was not silently discarded", metadata.name, flag)
				}
			}
		}
		flags := make([]string, 0, 5)
		if strings.Contains(status, "R") {
			flags = append(flags, `\Seen`)
		}
		for _, pair := range []struct{ letter, flag string }{{"D", `\Deleted`}, {"A", `\Answered`}, {"F", `\Flagged`}, {"T", `\Draft`}} {
			if strings.Contains(xStatus, pair.letter) {
				flags = append(flags, pair.flag)
			}
		}
		flags, err = goimap.NormalizeImportFlags(append(flags, keywords...))
		if err != nil {
			return err
		}
		// The metadata is now in flags. Like Dovecot serving the message over
		// IMAP, store it without them: a later flag edit on the source then
		// leaves the same octets, so a rerun skips it instead of copying it.
		return visit(fmt.Sprintf("message %d", count), apitypes.ImportMessageBody{RawMessage: withoutMboxMetadata(raw), Folder: folder, Flags: flags, InternalDate: date})
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The From separator is framing, not message content. Even a tiny
		// message limit needs room to read it; raw message bytes stay bounded below.
		line, err := boundedLine(r, max(maxBytes+2, 4096))
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(line) != 0 {
			if next, ok := mboxDate(line); ok {
				if err := flush(); err != nil {
					return err
				}
				date, raw, inBody = next, nil, false
				count++
			} else {
				if date.IsZero() {
					return errors.New("mbox must begin with a complete From separator and timestamp")
				}
				if inBody && format != "rfc4155" && len(line) > 0 && line[0] == '>' {
					unquoted := bytes.TrimLeft(line, ">")
					if bytes.HasPrefix(unquoted, []byte("From ")) && (format == "mboxrd" || bytes.HasPrefix(line, []byte(">From "))) {
						line = line[1:]
					}
				}
				if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
					inBody = true
				}
				if int64(len(raw)+len(line)) > maxBytes+2 {
					return fmt.Errorf("mbox message %d exceeds the size limit", count)
				}
				raw = append(raw, line...)
			}
		}
		if errors.Is(err, io.EOF) {
			return flush()
		}
	}
}

// mboxMetadataFields are the header fields Dovecot keeps a message's mailbox
// state in, not part of the message itself.
var mboxMetadataFields = []string{"status", "x-status", "x-keywords", "x-uid", "x-imapbase"}

// withoutMboxMetadata is raw with every mboxMetadataFields field removed from
// its header, continuation lines included. Every other octet is kept.
func withoutMboxMetadata(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	dropping := false
	for rest := raw; len(rest) > 0; {
		end := bytes.IndexByte(rest, '\n') + 1
		if end == 0 {
			end = len(rest)
		}
		line := rest[:end]
		if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
			return append(out, rest...)
		}
		if line[0] != ' ' && line[0] != '\t' {
			name, _, _ := bytes.Cut(line, []byte(":"))
			dropping = slices.Contains(mboxMetadataFields, strings.ToLower(string(bytes.TrimSpace(name))))
		}
		if !dropping {
			out = append(out, line...)
		}
		rest = rest[end:]
	}
	return out
}

func boundedLine(r *bufio.Reader, limit int64) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if int64(len(line)+len(part)) > limit {
			return nil, errors.New("mbox line exceeds the message size limit")
		}
		line = append(line, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func mboxDate(line []byte) (time.Time, bool) {
	text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
	if !strings.HasPrefix(text, "From ") {
		return time.Time{}, false
	}
	sender, stamp, found := strings.Cut(text[len("From "):], " ")
	if !found || sender == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.ANSIC, "Mon Jan _2 15:04:05 -0700 2006", "Mon Jan _2 15:04:05 2006 -0700"} {
		if date, err := time.ParseInLocation(layout, stamp, time.UTC); err == nil {
			return date, true
		}
	}
	return time.Time{}, false
}
