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

// A folder name is saved and looked up in NFC, so both spellings are one folder.
func TestCanonicalFolderIsNFC(t *testing.T) {
	for name, want := range map[string]string{
		"Cafe\u0301":        "Caf\u00e9",
		"Caf\u00e9":         "Caf\u00e9",
		"Work/Cafe\u0301":   "Work/Caf\u00e9",
		"inbox":             "INBOX",
		"Receipts":          "Receipts",
		"\u1100\u1161":      "\uac00", // Hangul jamo compose too.
		"\u212bngstr\u00f6": "\u00c5ngstr\u00f6",
	} {
		if got := CanonicalFolder(name); got != want {
			t.Errorf("CanonicalFolder(%q) = %q, want %q", name, got, want)
		}
	}
}

// NFC can make a name longer, and it is the converted name that is saved.
func TestFolderNameProblemCountsTheConvertedName(t *testing.T) {
	// U+0958 is three bytes, and composition exclusion makes it six in NFC.
	name := strings.Repeat("\u0958", 50)
	if len(name) > MaxFolderNameLen || len(NormalizeFolderName(name)) <= MaxFolderNameLen {
		t.Fatalf("test name: %d bytes, %d in NFC", len(name), len(NormalizeFolderName(name)))
	}
	if got := FolderNameProblem(name); got != "must be at most 255 bytes" {
		t.Errorf("FolderNameProblem = %q", got)
	}
}

func TestNumberedFolder(t *testing.T) {
	if got := NumberedFolder("Caf\u00e9", 2); got != "Caf\u00e9 (2)" {
		t.Errorf("NumberedFolder = %q", got)
	}
	// A name with no room for the suffix is cut, and never inside a character.
	long := strings.Repeat("a", MaxFolderNameLen-5) + "\u00e9\u00e9\u00e9"
	got := NumberedFolder(NormalizeFolderName(long), 12)
	if len(got) > MaxFolderNameLen || FolderNameProblem(got) != "" || !strings.HasSuffix(got, " (12)") {
		t.Errorf("NumberedFolder of a long name = %q (%d bytes): %q", got, len(got), FolderNameProblem(got))
	}
	for folder, want := range map[string]bool{
		"Caf\u00e9 (2)":  true,
		"Caf\u00e9 (12)": true,
		"Caf\u00e9":      false,
		"Caf\u00e9 (1)":  false,
		"Caf\u00e9 (x)":  false,
		"Other (2)":      false,
	} {
		if got := IsNumberedFolder(folder, "Caf\u00e9"); got != want {
			t.Errorf("IsNumberedFolder(%q) = %v, want %v", folder, got, want)
		}
	}
	if !IsNumberedFolder(got, NormalizeFolderName(long)) {
		t.Errorf("IsNumberedFolder does not know the cut name %q", got)
	}
}
