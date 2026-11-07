package server

import (
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
)

func (s *Session) createFolder(tag string, creator imap.FolderCreator, name string) {
	name = strings.TrimRight(name, "/")
	if name == "" || strings.EqualFold(name, "INBOX") {
		s.tagged(tag, "NO", "Invalid folder name")
		return
	}
	folders, err := s.mailbox.Folders()
	if err != nil {
		s.tagged(tag, "NO", "CREATE failed")
		return
	}
	if folderIn(folders, name) {
		s.tagged(tag, "NO", "[ALREADYEXISTS] Mailbox already exists")
		return
	}
	if rights, _ := s.mailboxRights(name); !rights.Create {
		s.tagged(tag, "NO", "[CANNOT] Mailbox cannot be created")
		return
	}
	if !s.createSuperiors(creator, folders, name) {
		s.tagged(tag, "NO", "CREATE failed")
		return
	}
	if err := creator.CreateFolder(name); err != nil {
		s.tagged(tag, "NO", "CREATE failed")
		return
	}
	s.tagged(tag, "OK", "CREATE completed")
}

func (s *Session) createSuperiors(creator imap.FolderCreator, folders []imap.Folder, name string) bool {
	for index := strings.Index(name, "/"); index >= 0; {
		superior := name[:index]
		if superior != "" && !folderIn(folders, canonicalizeInbox(superior)) {
			if err := creator.CreateFolder(superior); err != nil {
				return false
			}
		}
		next := strings.Index(name[index+1:], "/")
		if next < 0 {
			break
		}
		index += next + 1
	}
	return true
}
