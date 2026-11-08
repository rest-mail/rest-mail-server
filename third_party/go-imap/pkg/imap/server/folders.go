package server

import (
	"sort"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// This file holds DELETE and RENAME for a Mailbox that implements
// [imap.FolderMailbox]: the store makes, removes and renames folders, and the
// server answers only once it has (RFC 9051 §§6.3.4-6.3.6).

// deleteFolder answers DELETE of name, a store name that is not a standard
// folder. A folder with inferiors is deleted and they stay; a name that is not
// a mailbox but has inferiors is refused with HASCHILDREN (RFC 9051 §6.3.5).
func (s *Session) deleteFolder(tag string, fm imap.FolderMailbox, name string) {
	folders, err := s.mailbox.Folders()
	if err != nil {
		s.tagged(tag, "NO", "DELETE failed")
		return
	}
	if !folderIn(folders, name) {
		if hasInferiors(folders, name) {
			s.tagged(tag, "NO", "[HASCHILDREN] Mailbox has inferior mailboxes")
			return
		}
		s.tagged(tag, "NO", "[NONEXISTENT] No such mailbox")
		return
	}
	if rights, _ := s.mailboxRights(name); !rights.Write {
		s.tagged(tag, "NO", "[NOPERM] Mailbox cannot be deleted")
		return
	}
	if err := fm.DeleteFolder(name); err != nil {
		s.tagged(tag, "NO", "DELETE failed")
		return
	}
	s.tagged(tag, "OK", "DELETE completed")
}

// renameFolder answers RENAME of from to to, store names with from not a
// standard folder other than INBOX. Renaming INBOX moves its messages into the
// new folder and leaves INBOX empty, its inferiors untouched; renaming any
// other folder renames its inferiors with it (RFC 9051 §6.3.6).
func (s *Session) renameFolder(tag string, fm imap.FolderMailbox, from, to string) {
	to = strings.TrimRight(to, "/")
	if to == "" {
		s.tagged(tag, "NO", "Invalid folder name")
		return
	}
	if strings.EqualFold(to, "INBOX") {
		s.tagged(tag, "NO", "[ALREADYEXISTS] Mailbox already exists")
		return
	}
	if from != "INBOX" && (to == from || strings.HasPrefix(to, from+"/")) {
		s.tagged(tag, "NO", "[CANNOT] A mailbox cannot be renamed into itself")
		return
	}
	folders, err := s.mailbox.Folders()
	if err != nil {
		s.tagged(tag, "NO", "RENAME failed")
		return
	}
	// The renames to make, the source first and then its inferiors; INBOX's
	// inferiors are not renamed with it.
	type rename struct{ from, to string }
	var renames []rename
	if folderIn(folders, from) {
		renames = append(renames, rename{from, to})
	}
	if from != "INBOX" {
		var inferiors []string
		for _, f := range folders {
			if rest, ok := strings.CutPrefix(f.Name, from+"/"); ok {
				inferiors = append(inferiors, rest)
			}
		}
		sort.Strings(inferiors)
		for _, rest := range inferiors {
			renames = append(renames, rename{from + "/" + rest, to + "/" + rest})
		}
	}
	if len(renames) == 0 {
		s.tagged(tag, "NO", "[NONEXISTENT] No such mailbox")
		return
	}
	for _, r := range renames {
		if folderIn(folders, r.to) {
			s.tagged(tag, "NO", "[ALREADYEXISTS] Mailbox already exists")
			return
		}
	}
	for _, r := range renames {
		if rights, _ := s.mailboxRights(r.from); !rights.Write {
			s.tagged(tag, "NO", "[NOPERM] Mailbox cannot be renamed")
			return
		}
		if rights, _ := s.mailboxRights(r.to); !rights.Create {
			s.tagged(tag, "NO", "[CANNOT] Mailbox cannot be created")
			return
		}
	}
	if !s.createSuperiors(fm, folders, to) {
		s.tagged(tag, "NO", "RENAME failed")
		return
	}
	for _, r := range renames {
		if err := fm.RenameFolder(r.from, r.to); err != nil {
			s.tagged(tag, "NO", "RENAME failed")
			return
		}
	}
	s.tagged(tag, "OK", "RENAME completed")
}
