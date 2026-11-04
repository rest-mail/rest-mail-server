package server

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/rest-mail/go-imap/pkg/imap"
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
// names no folder. A store's normal form applies to the result; see
// [imap.NameNormalizer].
func (s *Session) mailboxName(encoded string) (string, bool) {
	name, ok := s.decodedName(encoded)
	if !ok {
		return name, false
	}
	return s.normalizedName(name), true
}

// decodedName is the mailbox name a command carries as the client spelled it.
func (s *Session) decodedName(encoded string) (string, bool) {
	switch {
	case s.utf8:
		return encoded, true
	case !wire.IsASCII(encoded):
		return encoded, s.utf8Accept() && utf8.ValidString(encoded)
	default:
		return mutf7.Decode(encoded)
	}
}

// normalizedName is name in the store's normal form, or name itself for a store
// that has none.
func (s *Session) normalizedName(name string) string {
	if n, ok := s.mailbox.(imap.NameNormalizer); ok {
		return n.NormalizeName(name)
	}
	return name
}

// announceOldName sends the untagged LIST that says the store used another
// spelling of a name the command carried: name is the name now in use, and
// OLDNAME is the first of supplied, as the client sent it. It sends nothing when
// every one of supplied is already in the normal form. A deleted mailbox is
// \NonExistent (RFC 9051 §§6.3.5, 6.3.9.7); any other carries its role, because
// the attributes must be accurate (§6.3.2). Only a changed name is reported, so a
// client that spells its names in NFC never sees this response.
func (s *Session) announceOldName(name string, deleted bool, supplied ...string) {
	changed := false
	for _, encoded := range supplied {
		decoded, ok := s.decodedName(encoded)
		changed = changed || ok && s.normalizedName(decoded) != decoded
	}
	if !changed {
		return
	}
	old, _ := s.decodedName(supplied[0])
	attrs := ""
	if deleted {
		attrs = `\NonExistent`
	} else if folders, err := s.mailbox.Folders(); err == nil {
		for _, f := range folders {
			if f.Name == name && specialUses[f.SpecialUse] {
				attrs = f.SpecialUse
			}
		}
	}
	s.send(`* LIST (%s) "/" %s ("OLDNAME" (%s))`, attrs,
		wire.EncodeMailboxName(s.wireName(name)), wire.EncodeMailboxName(s.wireName(old)))
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
// implements UNSUBSCRIBE (§6.3.7) — both mandatory IMAP4rev1 commands. A
// Mailbox that implements [imap.Subscriber] keeps the list; for any other, every
// existing folder is subscribed, as LSUB reports, and the commands validate the
// request and acknowledge it without effect.
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
	name, ok := s.folderName(mailbox)
	if !ok {
		s.tagged(tag, "NO", "Invalid mailbox name")
		return
	}
	if sub, ok := s.mailbox.(imap.Subscriber); ok {
		// The name need not exist: RFC 3501 §6.3.6 lets a server refuse one that
		// does not, and RFC 5258 example 2 keeps one deleted while subscribed.
		update := sub.Subscribe
		if cmd == "UNSUBSCRIBE" {
			update = sub.Unsubscribe
		}
		if err := update(name); err != nil {
			s.tagged(tag, "NO", cmd+" failed")
			return
		}
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
	supplied := folder
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

	if creator, ok := s.mailbox.(imap.FolderCreator); ok {
		s.createFolder(tag, creator, folder, supplied)
		return
	}

	// Folders are implicit — they exist once a message is moved into them.
	// CREATE just validates and acknowledges.
	s.announceOldName(folder, false, supplied)
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
	supplied := folder
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

	if fm, ok := s.mailbox.(imap.FolderMailbox); ok {
		s.deleteFolder(tag, fm, folder, supplied)
		return
	}

	// A name that is not a mailbox but has inferiors is \Noselect, and deleting
	// it is an error (RFC 9051 §6.3.5); HASCHILDREN says why (RFC 5530 §3).
	if folders, err := s.mailbox.Folders(); err == nil && !folderIn(folders, folder) && hasInferiors(folders, folder) {
		s.tagged(tag, "NO", "[HASCHILDREN] Mailbox has inferior mailboxes")
		return
	}

	// Move all messages in the folder to Trash.
	if messages, err := s.mailbox.Messages(folder); err == nil {
		for _, msg := range messages {
			_ = s.mailbox.Move(msg.UID, "Trash")
		}
	}

	s.announceOldName(folder, true, supplied)
	s.tagged(tag, "OK", "DELETE completed")
}

func (s *Session) handleRename(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	names, ok := mailboxArguments(args, 2)
	if !ok {
		s.tagged(tag, "BAD", "RENAME requires old and new name")
		return
	}
	oldName, newName := names[0], names[1]

	if oldName == "" || newName == "" {
		s.tagged(tag, "BAD", "RENAME requires old and new name")
		return
	}
	suppliedOld, suppliedNew := oldName, newName
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

	standard := map[string]bool{"Sent": true, "Drafts": true, "Trash": true}
	if standard[oldName] {
		s.tagged(tag, "NO", "Cannot rename standard folder")
		return
	}
	// A store that renames folders itself renames INBOX by moving its messages
	// to the new name (RFC 9051 §6.3.6).
	if fm, ok := s.mailbox.(imap.FolderMailbox); ok {
		s.renameFolder(tag, fm, oldName, newName, suppliedOld, suppliedNew)
		return
	}
	if oldName == "INBOX" {
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

	s.announceOldName(newName, false, suppliedOld, suppliedNew)
	s.tagged(tag, "OK", "RENAME completed")
}
