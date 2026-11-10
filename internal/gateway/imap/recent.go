package imap

import (
	"sync"

	goimap "github.com/rest-mail/go-imap/pkg/imap"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// recentMarks is the gateway's \Recent state (RFC 3501 §2.3.2): for each
// account's folder, the highest UID a read-write IMAP session has been told
// of. A message above the mark is \Recent and no session has claimed it yet.
//
// It lives in the gateway process, not the API: only IMAP sessions see
// \Recent, and every one of an account's sessions on this gateway shares it.
// A gateway that starts afresh, or a second replica, cannot know which session
// saw a message first, and then counts it recent again, which is what §2.3.2
// asks: "If it is not possible to determine whether or not this session is
// the first session to be notified about a message, then that message SHOULD
// be considered recent."
type recentMarks struct {
	mu    sync.Mutex
	marks map[recentKey]uint32
}

type recentKey struct {
	account uint
	folder  string
}

func newRecentMarks() *recentMarks {
	return &recentMarks{marks: map[recentKey]uint32{}}
}

// A nil recentMarks keeps no state: every message is recent, as §2.3.2 asks.
func (r *recentMarks) mark(key recentKey) uint32 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.marks[key]
}

// claim raises key's mark to upTo unless it is already higher, and returns the
// mark it had before.
func (r *recentMarks) claim(key recentKey, upTo uint32) uint32 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.marks[key]
	if upTo > prev {
		r.marks[key] = upTo
	}
	return prev
}

var _ goimap.RecentMailbox = (*mailbox)(nil)

func (m *mailbox) recentKey(folder string) recentKey {
	return recentKey{account: m.accountID, folder: rmail.CanonicalFolder(folder)}
}

// RecentMark reports folder's \Recent mark (RecentMailbox) without moving it,
// for EXAMINE and STATUS.
func (m *mailbox) RecentMark(folder string) (uint32, error) {
	if err := validateFolder(folder); err != nil {
		return 0, err
	}
	return m.recent.mark(m.recentKey(folder)), nil
}

// ClaimRecent raises folder's \Recent mark to upTo (RecentMailbox) for a
// read-write session and returns the mark before it.
func (m *mailbox) ClaimRecent(folder string, upTo uint32) (uint32, error) {
	if err := validateFolder(folder); err != nil {
		return 0, err
	}
	return m.recent.claim(m.recentKey(folder), upTo), nil
}
