package server

import (
	"errors"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// errNotReadable is the lookup error of a mailbox the user may not read: its
// STATUS is refused and LIST marks it \Noselect (RFC 9051 §6.3.9.2).
var errNotReadable = errors.New("imap: mailbox not readable")

// refusedDestination is the error of an APPEND, COPY or MOVE whose destination
// the user's rights refuse before the store sees it; code is the response code
// the tagged NO carries, or "" for none.
type refusedDestination struct{ code string }

func (e refusedDestination) Error() string { return "imap: destination refused " + e.code }

// mailboxRights returns the user's rights on folder, a store name. A Mailbox
// without [imap.RightsMailbox] gives every right; a lookup error gives none.
func (s *Session) mailboxRights(folder string) (imap.Rights, bool) {
	rm, ok := s.mailbox.(imap.RightsMailbox)
	if !ok {
		return imap.Rights{Read: true, Write: true, Create: true}, false
	}
	rights, err := rm.MailboxRights(folder)
	if err != nil {
		return imap.Rights{}, true
	}
	return rights, true
}

// mayRead reports whether the user may read folder, and so learn the UIDs of
// its messages: APPENDUID and COPYUID are not sent otherwise (RFC 9051
// §§6.3.12, 6.4.7, 11.4).
func (s *Session) mayRead(folder string) bool {
	rights, _ := s.mailboxRights(folder)
	return rights.Read
}

// destinationRefusal is the error refusing an APPEND, COPY or MOVE into dest
// before the store sees it, or nil. Only a Mailbox that reports rights is
// checked: a destination that does not exist is refused with TRYCREATE unless
// creating it is known to be impossible, so no command creates it implicitly
// (RFC 9051 §§6.3.12, 6.4.7), and one the user may not write with NOPERM (RFC
// 5530 §3). When the folders cannot be listed, the store decides as before.
func (s *Session) destinationRefusal(dest string) error {
	rights, reported := s.mailboxRights(dest)
	if !reported {
		return nil
	}
	exists, err := s.folderExists(dest)
	if err != nil {
		return nil
	}
	switch {
	case !exists && rights.Create:
		return refusedDestination{code: "TRYCREATE"}
	case !exists:
		return refusedDestination{code: "NONEXISTENT"}
	case !rights.Write:
		return refusedDestination{code: "NOPERM"}
	}
	return nil
}

// folderExists reports whether folder, a store name, is one of the user's
// folders.
func (s *Session) folderExists(folder string) (bool, error) {
	folders, err := s.mailbox.Folders()
	if err != nil {
		return false, err
	}
	return folderIn(folders, folder), nil
}

// folderIn reports whether folder, a store name, is one of folders.
func folderIn(folders []imap.Folder, folder string) bool {
	for _, f := range folders {
		if canonicalizeInbox(f.Name) == folder {
			return true
		}
	}
	return false
}

// hasInferiors reports whether some folder lies below folder in the hierarchy.
func hasInferiors(folders []imap.Folder, folder string) bool {
	for _, f := range folders {
		if strings.HasPrefix(f.Name, folder+"/") {
			return true
		}
	}
	return false
}
