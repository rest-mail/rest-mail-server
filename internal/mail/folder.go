package mail

import (
	"strings"
	"unicode/utf8"
)

// MaxFolderNameLen bounds a folder name in bytes: the messages.folder column is size:255, so a
// longer name could never match a stored row.
const MaxFolderNameLen = 255

// StandardFolders are the folders every mailbox has. They are not renamed or deleted, and no
// folder is renamed to one of them.
var StandardFolders = []string{"INBOX", "Sent", "Drafts", "Trash", "Spam", "Archive"}

// CanonicalFolder is the stored name of folder: INBOX in any case is INBOX, as IMAP has it
// (RFC 9051 §5.1); every other name is case-sensitive and kept as it is.
func CanonicalFolder(folder string) string {
	if strings.EqualFold(folder, "INBOX") {
		return "INBOX"
	}
	return folder
}

// IsStandardFolder reports whether folder is one of the StandardFolders.
func IsStandardFolder(folder string) bool {
	folder = CanonicalFolder(folder)
	for _, standard := range StandardFolders {
		if folder == standard {
			return true
		}
	}
	return false
}

// FolderNameProblem says what is wrong with a folder name a client chose, or "" when nothing
// is. The words are the field message a refusal gives. A name is valid UTF-8 of at most
// MaxFolderNameLen bytes, has something besides spaces and neither begins nor ends with one,
// and holds no control character (C0, DEL or C1: NUL, TAB, CR and LF among them) and no "..".
// A "/" is allowed: IMAP clients read it as the hierarchy delimiter ("Work/Projects").
func FolderNameProblem(name string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "required"
	case len(name) > MaxFolderNameLen:
		return "must be at most 255 bytes"
	case !utf8.ValidString(name):
		return "must be valid UTF-8"
	case strings.TrimSpace(name) != name:
		return "must not begin or end with a space"
	case strings.Contains(name, ".."):
		return `must not contain ".."`
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return "must not contain a control character"
		}
	}
	return ""
}
