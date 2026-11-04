package mail

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// MaxFolderNameLen bounds a folder name in bytes: the messages.folder column is size:255, so a
// longer name could never match a stored row.
const MaxFolderNameLen = 255

// StandardFolders are the folders every mailbox has. They are not renamed or deleted, and no
// folder is renamed to one of them.
var StandardFolders = []string{"INBOX", "Sent", "Drafts", "Trash", "Spam", "Archive"}

// NormalizeFolderName is name in Unicode Normalization Form C, the form a folder name is saved
// in and looked up by, which RFC 9051 §5.1 asks of a mailbox name.
func NormalizeFolderName(name string) string {
	return norm.NFC.String(name)
}

// CanonicalFolder is the stored name of folder: INBOX in any case is INBOX, as IMAP has it
// (RFC 9051 §5.1); every other name is case-sensitive and kept as it is, in NFC.
func CanonicalFolder(folder string) string {
	if strings.EqualFold(folder, "INBOX") {
		return "INBOX"
	}
	return NormalizeFolderName(folder)
}

// NumberedFolder is the name an import uses when name is taken: name with " (n)" appended,
// such as "Café (2)". A name too long to take the suffix is cut at a character boundary first,
// so the result still fits MaxFolderNameLen.
func NumberedFolder(name string, n int) string {
	suffix := fmt.Sprintf(" (%d)", n)
	if room := MaxFolderNameLen - len(suffix); len(name) > room {
		name = name[:room]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	return name + suffix
}

// IsNumberedFolder reports whether folder is a NumberedFolder of name.
func IsNumberedFolder(folder, name string) bool {
	number, found := strings.CutSuffix(folder, ")")
	if !found {
		return false
	}
	i := strings.LastIndex(number, " (")
	if i < 0 {
		return false
	}
	n, err := strconv.Atoi(number[i+2:])
	return err == nil && n >= 2 && folder == NumberedFolder(name, n)
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
// MaxFolderNameLen bytes, in the NFC it is saved in too, has something besides spaces and
// neither begins nor ends with one, and holds no control character (C0, DEL or C1: NUL, TAB, CR
// and LF among them) and no "..". A "/" is allowed: IMAP clients read it as the hierarchy
// delimiter ("Work/Projects").
func FolderNameProblem(name string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "required"
	case len(name) > MaxFolderNameLen, len(NormalizeFolderName(name)) > MaxFolderNameLen:
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
