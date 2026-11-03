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

// Each standard folder has its role and, but for INBOX, its RFC 6154 attribute; INBOX is
// matched in any case and every other name only exactly, so a custom "sent" has no role (#1611).
func TestRoleOf(t *testing.T) {
	for _, tc := range []struct {
		folder     string
		role       FolderRole
		specialUse string
	}{
		{"INBOX", RoleInbox, ""},
		{"inbox", RoleInbox, ""},
		{"Inbox", RoleInbox, ""},
		{"Sent", RoleSent, `\Sent`},
		{"Drafts", RoleDrafts, `\Drafts`},
		{"Trash", RoleTrash, `\Trash`},
		{"Spam", RoleJunk, `\Junk`},
		{"Archive", RoleArchive, `\Archive`},
		{"sent", "", ""},
		{"SPAM", "", ""},
		{"Junk", "", ""},
		{"Projects/Sent", "", ""},
		{"", "", ""},
	} {
		if got := RoleOf(tc.folder); got != tc.role {
			t.Errorf("RoleOf(%q) = %q, want %q", tc.folder, got, tc.role)
		}
		if got := RoleOf(tc.folder).SpecialUse(); got != tc.specialUse {
			t.Errorf("RoleOf(%q).SpecialUse() = %q, want %q", tc.folder, got, tc.specialUse)
		}
	}
	for _, standard := range StandardFolders {
		if RoleOf(standard) == "" {
			t.Errorf("standard folder %q has no role", standard)
		}
	}
}
