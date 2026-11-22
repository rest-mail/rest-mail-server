// Package mailimport reads migration sources without modifying them. Each
// callback receives one bounded original message and its source metadata.
package mailimport

import (
	"context"
	"errors"
	"fmt"
	goimap "github.com/rest-mail/go-imap/pkg/imap"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	shared "github.com/rest-mail/rest-mail-server/pkg/mailimport"
)

type Visit func(source string, message apitypes.ImportMessageBody) error

// foldInbox is name with INBOX, in any case, written INBOX. Any other name stays as the source
// spells it, because the API has to see a name that is not in NFC to apply its rule for one.
func foldInbox(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

// sourceFolders names one run's source folders at the destination. Two that match after
// conversion to NFC are not refused and are not mixed: the first in the source's order keeps the
// name and the next is numbered, so a rerun of the same source chooses the same names.
type sourceFolders struct {
	names map[string]string // source folder's name to the name it was given
	taken map[string]bool   // names in NFC given so far
}

func newSourceFolders() *sourceFolders {
	return &sourceFolders{names: map[string]string{}, taken: map[string]bool{}}
}

// destination is the folder to give messages from the source folder called name.
func (f *sourceFolders) destination(name string) string {
	if given, ok := f.names[name]; ok {
		return given
	}
	canonical := rmail.CanonicalFolder(name)
	given := name
	for n := 2; f.taken[canonical]; n++ {
		canonical = rmail.NumberedFolder(rmail.CanonicalFolder(name), n)
		given = canonical
	}
	f.taken[canonical] = true
	f.names[name] = given
	return given
}

type Destinations = shared.Destinations

func NewDestinations() *Destinations { return shared.NewDestinations() }

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

func Mbox(ctx context.Context, source io.Reader, folder, format string, maxBytes int64, visit Visit) error {
	return shared.Mbox(ctx, source, folder, format, maxBytes, shared.Visit(visit))
}
