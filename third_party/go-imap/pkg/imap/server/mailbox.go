package server

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/rest-mail/go-imap/pkg/imap/internal/mutf7"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// errInvalidMailboxName is a mailbox name a command carries that names no
// folder: not modified UTF-7, or 8-bit where UTF-8 is not accepted.
var errInvalidMailboxName = errors.New("imap: invalid mailbox name")

// mailboxName is the store's name for the mailbox name a command carries. Once
// the client has enabled UTF8=ACCEPT the name is already UTF-8. Before, it is
// modified UTF-7, decoded here; a UTF8=ACCEPT server also takes a UTF-8 name
// then, as RFC 6855 §3 requires. ok is false for a name that is neither, which
// names no folder.
func (s *Session) mailboxName(encoded string) (string, bool) {
	switch {
	case s.utf8:
		return encoded, true
	case !wire.IsASCII(encoded):
		return encoded, s.utf8Accept() && utf8.ValidString(encoded)
	default:
		return mutf7.Decode(encoded)
	}
}

// folderName is mailboxName with INBOX, in any case, folded to INBOX (RFC 3501
// §5.1).
func (s *Session) folderName(encoded string) (string, bool) {
	name, ok := s.mailboxName(encoded)
	if !ok {
		return "", false
	}
	return canonicalizeInbox(name), true
}

// wireName is how a response writes the store's folder name: unchanged once the
// client has enabled UTF8=ACCEPT, in modified UTF-7 before, since a server must
// not send UTF-8 to a client that has not (RFC 3501 §5.1.3, RFC 6855 §3).
func (s *Session) wireName(name string) string {
	if s.utf8 {
		return name
	}
	return mutf7.Encode(name)
}

// handleSubscribe implements SUBSCRIBE (RFC 3501 §6.3.6) and handleUnsubscribe
// implements UNSUBSCRIBE (§6.3.7) — both mandatory IMAP4rev1 commands. This
// engine keeps no persistent per-user subscription list: folders are implicit
// (there is no folder-management backend method) and LSUB already reports every
// existing folder as subscribed. The commands therefore validate the request and
// acknowledge it without maintaining separate state, rather than answering the
// BAD "Unknown command" a client would otherwise get for a mandatory command.
func (s *Session) handleSubscribe(tag, args string) {
	s.handleSubscription(tag, args, "SUBSCRIBE")
}

func (s *Session) handleUnsubscribe(tag, args string) {
	s.handleSubscription(tag, args, "UNSUBSCRIBE")
}

func (s *Session) handleSubscription(tag, args, cmd string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}
	mailbox := wire.Unquote(strings.TrimSpace(args))
	if mailbox == "" {
		s.tagged(tag, "BAD", cmd+" requires a mailbox name")
		return
	}
	if _, ok := s.mailboxName(mailbox); !ok {
		s.tagged(tag, "NO", "Invalid mailbox name")
		return
	}
	s.tagged(tag, "OK", cmd+" completed")
}

func (s *Session) handleCreate(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	folder := wire.Unquote(strings.TrimSpace(args))
	if folder == "" {
		s.tagged(tag, "NO", "Missing folder name")
		return
	}
	// A name with "&" must be modified UTF-7 without superfluous shifts or
	// encoded ASCII (RFC 3501 §5.1.3), and an 8-bit name is refused unless the
	// server takes UTF-8 (§5.1, RFC 6855 §3).
	folder, ok := s.mailboxName(folder)
	if !ok {
		s.tagged(tag, "NO", "Invalid folder name")
		return
	}
	// INBOX always exists and is case-insensitive (RFC 3501 §5.1, §6.3.3), so
	// CREATE of any-case "INBOX" must fail rather than silently succeed.
	if strings.EqualFold(folder, "INBOX") {
		s.tagged(tag, "NO", "INBOX already exists")
		return
	}
	// Reject folder names that are too long or contain path separators
	if len(folder) > 200 {
		s.tagged(tag, "NO", "Folder name too long")
		return
	}
	if strings.ContainsAny(folder, "\x00\r\n") {
		s.tagged(tag, "NO", "Invalid folder name")
		return
	}
	// A UTF8=ACCEPT server's mailbox names are Net-Unicode without controls or
	// line and paragraph separators (RFC 6855 §3).
	if s.utf8Accept() && !wire.ValidUTF8MailboxName(folder) {
		s.tagged(tag, "NO", "Invalid folder name")
		return
	}

	// Folders are implicit — they exist once a message is moved into them.
	// CREATE just validates and acknowledges.
	s.tagged(tag, "OK", "CREATE completed")
}

func (s *Session) handleDelete(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	folder := wire.Unquote(strings.TrimSpace(args))
	if folder == "" {
		s.tagged(tag, "NO", "Missing folder name")
		return
	}
	// "INBOX" is case-insensitive (RFC 3501 §5.1): fold to canonical so any-case
	// spelling ("inbox", "InBoX") hits the standard-folder guard below rather
	// than bypassing it. Other standard names stay case-sensitive.
	folder, ok := s.folderName(folder)
	if !ok {
		s.tagged(tag, "NO", "No such folder")
		return
	}

	// Prevent deleting standard folders
	standard := map[string]bool{"INBOX": true, "Sent": true, "Drafts": true, "Trash": true}
	if standard[folder] {
		s.tagged(tag, "NO", "Cannot delete standard folder")
		return
	}

	// Move all messages in the folder to Trash.
	if messages, err := s.mailbox.Messages(folder); err == nil {
		for _, msg := range messages {
			_ = s.mailbox.Move(msg.UID, "Trash")
		}
	}

	s.tagged(tag, "OK", "DELETE completed")
}

func (s *Session) handleRename(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	parts := strings.SplitN(args, " ", 2)
	if len(parts) < 2 {
		s.tagged(tag, "BAD", "RENAME requires old and new name")
		return
	}

	oldName := wire.Unquote(strings.TrimSpace(parts[0]))
	newName := wire.Unquote(strings.TrimSpace(parts[1]))

	if oldName == "" || newName == "" {
		s.tagged(tag, "BAD", "RENAME requires old and new name")
		return
	}
	var oldOK, newOK bool
	oldName, oldOK = s.mailboxName(oldName)
	newName, newOK = s.mailboxName(newName)
	if !oldOK || !newOK {
		s.tagged(tag, "NO", "Invalid folder name")
		return
	}
	// The new name follows CREATE's UTF8=ACCEPT rules (RFC 6855 §3).
	if s.utf8Accept() && !wire.ValidUTF8MailboxName(newName) {
		s.tagged(tag, "NO", "Invalid folder name")
		return
	}
	// "INBOX" is case-insensitive (RFC 3501 §5.1): fold the source name so any-case
	// spelling matches the standard-folder guard below rather than bypassing it.
	oldName = canonicalizeInbox(oldName)

	standard := map[string]bool{"INBOX": true, "Sent": true, "Drafts": true, "Trash": true}
	if standard[oldName] {
		s.tagged(tag, "NO", "Cannot rename standard folder")
		return
	}

	// Move all messages from old folder to new folder. Inferior hierarchical
	// names are renamed with it (RFC 3501 §6.3.5): "old/x" becomes "new/x".
	renames := map[string]string{oldName: newName}
	if folders, err := s.mailbox.Folders(); err == nil {
		for _, f := range folders {
			if rest, ok := strings.CutPrefix(f.Name, oldName+"/"); ok {
				renames[f.Name] = newName + "/" + rest
			}
		}
	}
	for from, to := range renames {
		if messages, err := s.mailbox.Messages(from); err == nil {
			for _, msg := range messages {
				_ = s.mailbox.Move(msg.UID, to)
			}
		}
	}

	s.tagged(tag, "OK", "RENAME completed")
}
