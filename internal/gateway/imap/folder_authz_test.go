package imap

import (
	"strings"
	"testing"

	imapsrv "github.com/rest-mail/go-imap"
)

// TestValidateFolder_Allow covers the folder names the gateway must let through:
// ordinary flat and hierarchical mailbox names, names with spaces, and non-ASCII
// letters. OSI-9 hardening must not reject legitimate folders.
func TestValidateFolder_Allow(t *testing.T) {
	allowed := []string{
		"INBOX",
		"Sent",
		"Drafts",
		"Trash",
		"Work/Projects",
		"[Gmail]/All Mail",
		"Clients & Partners",
		"Ärchiv", // non-ASCII letters
		"folder.with.dots",
		strings.Repeat("a", maxFolderNameLen), // exactly at the length cap
	}
	for _, f := range allowed {
		if err := validateFolder(f); err != nil {
			t.Errorf("validateFolder(%q) = %v, want nil (legitimate folder rejected)", f, err)
		}
	}
}

// TestValidateFolder_Deny covers the structurally dangerous folder names the
// gateway must refuse before building a downstream API request: empty, oversize,
// control characters (incl. CR/LF/NUL/TAB used for request/JSON injection),
// invalid UTF-8, and path-traversal sequences (OSI-9).
func TestValidateFolder_Deny(t *testing.T) {
	denied := map[string]string{
		"empty":           "",
		"too long":        strings.Repeat("a", maxFolderNameLen+1),
		"CRLF injection":  "INBOX\r\nX-Injected: 1",
		"bare LF":         "INBOX\nSent",
		"bare CR":         "INBOX\rSent",
		"NUL byte":        "IN\x00BOX",
		"TAB":             "IN\tBOX",
		"C0 control":      "IN\x01BOX",
		"DEL":             "IN\x7fBOX",
		"C1 control":      "IN\x9fBOX",
		"invalid UTF-8":   "IN\xffBOX",
		"path traversal":  "../../etc/passwd",
		"embedded dotdot": "Work/../../secrets",
	}
	for name, f := range denied {
		if err := validateFolder(f); err == nil {
			t.Errorf("%s: validateFolder(%q) = nil, want error (dangerous folder accepted)", name, f)
		}
	}
}

// TestMailboxOps_RejectInvalidFolder proves the guard is wired into every
// folder-accepting mailbox operation: an invalid destination folder must be
// refused at the gateway BEFORE any API call is made. The mailbox has a nil api
// client, so any code path that reached the backend would panic — the test
// passing (an error, no panic) demonstrates validation short-circuits first.
func TestMailboxOps_RejectInvalidFolder(t *testing.T) {
	m := &mailbox{email: "user@example.test"} // api deliberately nil

	bad := "Sent\r\nBcc: victim@example.test"

	if _, err := m.Messages(bad); err == nil {
		t.Error("Messages accepted an injection folder name")
	}
	if _, err := m.MoveUID(1, bad); err == nil {
		t.Error("MoveUID accepted an injection destination folder")
	}
	if _, err := m.CopyUID(1, bad); err == nil {
		t.Error("CopyUID accepted an injection destination folder")
	}
	if _, err := m.AppendUID(bad, imapsrv.FlagUpdate{}, []byte("From: a@b\r\n\r\nhi")); err == nil {
		t.Error("AppendUID accepted an injection destination folder")
	}
}
