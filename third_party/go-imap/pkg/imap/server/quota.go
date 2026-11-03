package server

import (
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// handleGetQuota returns quota for a named quota root (RFC 2087).
func (s *Session) handleGetQuota(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	root, rest, ok := wire.ParseAstring(args)
	if !ok || rest != "" || !wire.ValidAstring(args) {
		s.tagged(tag, "BAD", "GETQUOTA requires one quota root")
		return
	}
	// The backend exposes one account-wide root, whose name is the empty
	// string. A different name must not receive that root's usage by accident.
	if root != "" {
		s.tagged(tag, "NO", "No such quota root")
		return
	}

	used, limit, err := s.mailbox.Quota()
	if err != nil {
		s.tagged(tag, "NO", "Failed to get quota")
		return
	}
	used, limit, valid := quotaUnits(used, limit)
	if !valid {
		s.tagged(tag, "NO", "Invalid quota data")
		return
	}

	// Report in KB (IMAP QUOTA uses 1024-byte units)
	s.send("* QUOTA \"\" (STORAGE %d %d)", used, limit)
	s.tagged(tag, "OK", "GETQUOTA completed")
}

// handleGetQuotaRoot returns the quota root for a mailbox (RFC 2087).
func (s *Session) handleGetQuotaRoot(tag, args string) {
	if !s.auth.authenticated {
		s.tagged(tag, "NO", "Not authenticated")
		return
	}

	mailbox, rest, ok := wire.ParseAstring(args)
	if !ok || rest != "" || !wire.ValidAstring(args) {
		s.tagged(tag, "BAD", "GETQUOTAROOT requires one mailbox")
		return
	}
	if mailbox == "" {
		s.tagged(tag, "BAD", "Missing mailbox name")
		return
	}
	folder, ok := s.folderName(mailbox)
	if !ok {
		s.tagged(tag, "NO", "No such mailbox")
		return
	}

	folders, err := s.mailbox.Folders()
	if err != nil {
		s.tagged(tag, "NO", "Failed to list mailboxes")
		return
	}
	found := false
	for _, candidate := range folders {
		if canonicalizeInbox(candidate.Name) == folder {
			found = true
			break
		}
	}
	if !found {
		s.tagged(tag, "NO", "No such mailbox")
		return
	}
	used, limit, err := s.mailbox.Quota()
	if err != nil {
		s.tagged(tag, "NO", "Failed to get quota")
		return
	}
	used, limit, valid := quotaUnits(used, limit)
	if !valid {
		s.tagged(tag, "NO", "Invalid quota data")
		return
	}

	s.send("* QUOTAROOT %s \"\"", wire.EncodeMailboxName(s.wireName(folder)))
	s.send("* QUOTA \"\" (STORAGE %d %d)", used, limit)
	s.tagged(tag, "OK", "GETQUOTAROOT completed")
}

// quotaUnits preserves the backend's byte-to-1024-octet conversion, while
// rejecting negative data and values the advertised rev1 number grammar
// cannot represent. Call it before emitting either untagged response.
func quotaUnits(used, limit int64) (int64, int64, bool) {
	if used < 0 || limit < 0 {
		return 0, 0, false
	}
	used, limit = used/1024, limit/1024
	const maxUnits = int64(1<<32 - 1)
	return used, limit, used <= maxUnits && limit <= maxUnits
}
