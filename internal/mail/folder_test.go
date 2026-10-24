package mail

import (
	"strings"
	"testing"
)

func TestFolderNameProblem(t *testing.T) {
	for _, name := range []string{
		"Receipts",
		"Work/Projects",
		"[Gmail]/All Mail",
		"Clients & Partners",
		"Ärchiv",
		"folder.with.dots",
		"a",
		strings.Repeat("a", MaxFolderNameLen),
	} {
		if problem := FolderNameProblem(name); problem != "" {
			t.Errorf("FolderNameProblem(%q) = %q, want none", name, problem)
		}
	}
	for name, want := range map[string]string{
		"":                                      "required",
		"   ":                                   "required",
		strings.Repeat("a", MaxFolderNameLen+1): "must be at most 255 bytes",
		strings.Repeat("ä", MaxFolderNameLen/2+1): "must be at most 255 bytes",
		"IN\xffBOX":              "must be valid UTF-8",
		" Receipts":              "must not begin or end with a space",
		"Receipts ":              "must not begin or end with a space",
		"../../etc/passwd":       `must not contain ".."`,
		"Work/../secrets":        `must not contain ".."`,
		"INBOX\r\nX-Injected: 1": "must not contain a control character",
		"IN\x00BOX":              "must not contain a control character",
		"IN\tBOX":                "must not contain a control character",
		"IN\x7fBOX":              "must not contain a control character",
		"IN\u009fBOX":            "must not contain a control character",
	} {
		if got := FolderNameProblem(name); got != want {
			t.Errorf("FolderNameProblem(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestStandardFolders(t *testing.T) {
	for _, name := range []string{"INBOX", "inbox", "Inbox", "Sent", "Drafts", "Trash", "Spam", "Archive"} {
		if !IsStandardFolder(name) {
			t.Errorf("IsStandardFolder(%q) = false, want true", name)
		}
	}
	// Only INBOX is matched in any case (RFC 9051 §5.1).
	for _, name := range []string{"sent", "trash", "Receipts", "INBOX/Receipts", ""} {
		if IsStandardFolder(name) {
			t.Errorf("IsStandardFolder(%q) = true, want false", name)
		}
	}
	for name, want := range map[string]string{"inbox": "INBOX", "InBoX": "INBOX", "sent": "sent", "Work": "Work"} {
		if got := CanonicalFolder(name); got != want {
			t.Errorf("CanonicalFolder(%q) = %q, want %q", name, got, want)
		}
	}
}
