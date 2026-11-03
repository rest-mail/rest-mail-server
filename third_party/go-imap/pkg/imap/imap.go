// Package imap holds the types an IMAP4rev1 ([RFC 3501]) server and the mail
// store behind it share: the [Backend] and [Mailbox] interfaces a host
// implements, the neutral [Message], [Address], [Folder] and [FlagUpdate]
// values they exchange, the optional interfaces that turn extensions on, and
// the per-IP [Limiter].
//
// The protocol lives in [github.com/rest-mail/go-imap/pkg/imap/server], which
// imports this package. Neither has external dependencies (standard library
// only).
//
// # Backend and Mailbox
//
// [Backend] has a single method, Authenticate, which the server calls once per
// session when the client issues LOGIN or AUTHENTICATE. It returns a [Mailbox]:
// the authenticated account's view of its mail, every method scoped to that user.
// The server caches the slice [Mailbox.Messages] returns for the selected folder
// and derives sequence numbers, UIDs, flags, ENVELOPE and SEARCH results from it,
// calling [Mailbox.Fetch] only when a body is requested — and FETCH BODY[] then
// serves whatever Fetch returns, byte-for-byte. A folder exists when [Mailbox.Folders]
// lists it. A Mailbox that implements [FolderMailbox] creates, deletes and renames
// folders itself; for any other, folders are implicit: a folder exists once a
// message names it, and deleting or renaming one is composed from
// [Mailbox.Messages] and [Mailbox.Move].
// The store holds no assumptions about where mail lives: a Backend can be a
// database, a maildir, or a remote API.
//
// # Optional extensions
//
// Extensions that need more from the store are opt-in through optional interfaces
// the server type-asserts on the concrete [Mailbox] after authentication. A
// Mailbox that also implements [UIDPlusMailbox] turns on UIDPLUS ([RFC 4315]) —
// APPENDUID/COPYUID response codes, the real UIDVALIDITY in SELECT/EXAMINE, and
// UID EXPUNGE — and one that implements [Mover] turns the baseline MOVE
// ([RFC 6851]) into a single atomic backend operation. A Mailbox that implements
// [DateAppender] additionally receives the client-supplied INTERNALDATE on
// APPEND, and one that implements [UTF8Mailbox] turns on UTF8=ACCEPT
// ([RFC 6855]) through ENABLE. One that implements [Subscriber] keeps a real
// subscription list for SUBSCRIBE, UNSUBSCRIBE, LSUB and LIST's SUBSCRIBED
// options ([RFC 5258]). One that implements [RightsMailbox] tells the server
// what the user may do with each mailbox, for read-only selections, TRYCREATE
// and keeping APPENDUID and COPYUID from callers who cannot read the
// destination ([RFC 9051] §§6.3.2, 6.3.12, 11.4). The advertised CAPABILITY list reflects only the
// optional interfaces the concrete Mailbox satisfies, so a backend implementing
// just [Mailbox] is unaffected and the list never over-promises.
//
// [RFC 3501]: https://www.rfc-editor.org/rfc/rfc3501
// [RFC 4315]: https://www.rfc-editor.org/rfc/rfc4315
// [RFC 6851]: https://www.rfc-editor.org/rfc/rfc6851
// [RFC 6855]: https://www.rfc-editor.org/rfc/rfc6855
// [RFC 5258]: https://www.rfc-editor.org/rfc/rfc5258
// [RFC 9051]: https://www.rfc-editor.org/rfc/rfc9051
package imap

import (
	"errors"
	"time"
)

// Address is a parsed mail address for ENVELOPE and SEARCH.
type Address struct {
	Name  string // display name, e.g. "Alice"
	Email string // addr-spec, e.g. "alice@example.com"
}

// Message is one message in a mailbox, as IMAP needs to see it. A Server caches
// the slice [Mailbox.Messages] returns for the selected folder and derives
// sequence numbers (position+1), UIDs, flags, ENVELOPE and SEARCH results from
// it, calling [Mailbox.Fetch] only when a body is requested.
type Message struct {
	// UID is the IMAP unique identifier. It must be a positive, stable value that
	// ascends with arrival order within the mailbox. The Server also passes it to
	// the Backend to name this message.
	UID uint32
	// Size is the nonnegative RFC822.SIZE octet count of the complete message,
	// including headers. It backs FETCH RFC822.SIZE and SEARCH LARGER/SMALLER.
	Size int
	// Seen, Answered, Flagged, Draft and Deleted are the persistent system
	// flags IMAP reports; Keywords holds the message's flag-keyword atoms.
	Seen     bool
	Answered bool
	Flagged  bool
	Draft    bool
	Deleted  bool
	Keywords []string
	// Subject, From and Date populate ENVELOPE and INTERNALDATE, and back the
	// SUBJECT/FROM/SINCE/BEFORE/ON SEARCH keys.
	Subject string
	From    Address
	Date    time.Time
	// To is the message's recipient list as a single string; SEARCH TO does a
	// case-insensitive substring match against it, with encoded words decoded.
	To string
}

// Folder is one mailbox folder reported by LIST/LSUB.
type Folder struct {
	// Name is the store's own name for the folder, in UTF-8, with "/" between
	// levels of hierarchy. The server writes it in modified UTF-7 for a client
	// that has not enabled UTF8=ACCEPT (RFC 3501 §5.1.3).
	Name string
	// SpecialUse is the folder's role (RFC 6154 §2): one of the SpecialUse
	// attributes, or empty for an ordinary folder. The store says which folder
	// holds what; the name alone never does.
	SpecialUse string
}

// The special-use attributes of RFC 6154 §2, for [Folder.SpecialUse]. LIST
// reports the one a folder has, and LIST (SPECIAL-USE) lists only the folders
// that have one.
const (
	SpecialUseAll     = `\All`
	SpecialUseArchive = `\Archive`
	SpecialUseDrafts  = `\Drafts`
	SpecialUseFlagged = `\Flagged`
	SpecialUseJunk    = `\Junk`
	SpecialUseSent    = `\Sent`
	SpecialUseTrash   = `\Trash`
)

// FlagUpdate is a change to a message's persistent flags. A nil field leaves that
// flag unchanged; a non-nil field sets it to the pointed-to value.
type FlagUpdate struct {
	Seen        *bool
	Answered    *bool
	Flagged     *bool
	Draft       *bool
	Deleted     *bool
	Keywords    []string
	KeywordsSet bool
}

// Backend authenticates IMAP users. The server calls Authenticate once per
// session, when the client issues LOGIN or AUTHENTICATE.
type Backend interface {
	// Authenticate validates the credentials. A non-nil error rejects the login
	// (counting as an auth failure against the [Limiter]); on success it returns
	// the [Mailbox] the session operates on.
	Authenticate(user, pass string) (Mailbox, error)
}

// Mailbox is a single authenticated account's view of its mail. Every method is
// scoped to the user that [Backend.Authenticate] accepted; a Mailbox is used by
// one session, though [Mailbox.Messages] may be called concurrently by the IDLE
// poller while the client is idling.
//
// Every folder name a Mailbox takes or returns is the store's UTF-8 name. The
// server converts at the wire: until the client enables UTF8=ACCEPT, names are
// modified UTF-7 there (RFC 3501 §5.1.3), decoded before they reach the Mailbox,
// and a name that is not valid modified UTF-7 never does.
type Mailbox interface {
	// Folders lists the account's folders (LIST/LSUB).
	Folders() ([]Folder, error)
	// Messages returns a folder's messages oldest-first (SELECT, STATUS, IDLE).
	Messages(folder string) ([]Message, error)
	// Fetch returns the full RFC 5322 bytes of a message by UID (FETCH BODY[]).
	Fetch(uid uint32) ([]byte, error)
	// Store applies a persistent flag change to a message (STORE, and the
	// auto-\Seen a non-peek BODY[] fetch triggers).
	Store(uid uint32, f FlagUpdate) error
	// Move relocates a message to another folder (MOVE; also how deleting or
	// renaming a folder relocates its messages).
	Move(uid uint32, dest string) error
	// Delete permanently removes a message (EXPUNGE, CLOSE).
	Delete(uid uint32) error
	// Copy duplicates a message into dest (COPY).
	Copy(uid uint32, dest string) error
	// Append delivers raw RFC 5322 bytes into dest with the given flags (APPEND).
	Append(dest string, f FlagUpdate, raw []byte) error
	// Quota returns storage use and limit in bytes (GETQUOTA/GETQUOTAROOT).
	Quota() (used, limit int64, err error)
}

// ErrNoSuchMailbox is the error a [Mailbox] returns, possibly wrapped, from
// Append, Copy or Move (and their [UIDPlusMailbox], [Mover] and [DateAppender]
// forms) when the destination folder does not exist and the store will not
// create it implicitly. The server then answers the APPEND, COPY or MOVE with
// "NO [TRYCREATE]" so the client can CREATE the folder and retry ([RFC 3501]
// §§6.3.11, 6.4.7; [RFC 6851] §3.3). Return another error when creating the
// folder is known to be impossible; any other error gets a plain NO.
//
// [RFC 6851]: https://www.rfc-editor.org/rfc/rfc6851
var ErrNoSuchMailbox = errors.New("imap: no such mailbox")

// UIDPlusMailbox is the optional interface a [Mailbox] may also implement to
// enable the UIDPLUS extension ([RFC 4315]). When the authenticated Mailbox
// satisfies it, the server advertises the UIDPLUS capability, reports the real
// UIDVALIDITY in SELECT/EXAMINE, emits APPENDUID and COPYUID response codes on
// APPEND / COPY / MOVE, and honours UID EXPUNGE. A Mailbox that does not
// implement it keeps exact pre-UIDPLUS behaviour: the capability is not
// advertised and no response codes are sent.
//
// The three methods mirror the base [Mailbox.Append] and [Mailbox.Copy] but
// return the UID the store assigned, which the resp-codes require; a base
// implementation can simply call its UID-returning form and discard the result.
//
// [RFC 4315]: https://www.rfc-editor.org/rfc/rfc4315
type UIDPlusMailbox interface {
	// UIDValidity returns the UIDVALIDITY of folder — the value SELECT/EXAMINE
	// reports and that scopes the UIDs quoted in APPENDUID and COPYUID. A zero
	// value or error makes the Server fall back to reporting UIDVALIDITY 1 and
	// omit the affected response code.
	UIDValidity(folder string) (uint32, error)
	// AppendUID delivers raw into dest exactly like [Mailbox.Append] and returns
	// the UID assigned to the stored message, for the APPENDUID response code.
	AppendUID(dest string, f FlagUpdate, raw []byte) (uid uint32, err error)
	// CopyUID duplicates the message named by srcUID into dest exactly like
	// [Mailbox.Copy] and returns the UID assigned to the copy, for COPYUID.
	// COPYUID is omitted when any returned UID is zero or repeats in the command.
	CopyUID(srcUID uint32, dest string) (uid uint32, err error)
}

// Subscriber is the optional interface a [Mailbox] may also implement to keep
// the user's subscription list (RFC 3501 §§6.3.6-6.3.9, RFC 5258 §3.1). A
// subscribed name need not exist: a folder deleted while subscribed stays on
// the list, which LIST (SUBSCRIBED) reports as \NonExistent \Subscribed. Names
// are store names, as [Folder.Name] holds them, with INBOX in that spelling.
//
// A Mailbox that does not implement it has every existing folder subscribed,
// and nothing else: SUBSCRIBE and UNSUBSCRIBE are acknowledged without effect.
type Subscriber interface {
	// Subscriptions returns every subscribed name, in any order.
	Subscriptions() ([]string, error)
	// Subscribe adds name to the list; subscribing a listed name again is not
	// an error.
	Subscribe(name string) error
	// Unsubscribe removes name from the list; removing an unlisted name is not
	// an error.
	Unsubscribe(name string) error
}

// Rights is what the signed-in user may do with one mailbox.
type Rights struct {
	// Read lets the user SELECT or EXAMINE the mailbox, ask its STATUS and
	// learn the UIDs its messages have, APPENDUID and COPYUID among them
	// (RFC 9051 §§6.3.12, 6.4.7, 11.4).
	Read bool
	// Write lets the user put messages into the mailbox (APPEND, COPY, MOVE)
	// and change or expunge the ones there. A mailbox the user may read but
	// not write is selected read-only, even by SELECT (RFC 9051 §6.3.2).
	Write bool
	// Create says the user could create the mailbox when it does not exist,
	// so a command that needs it answers NO [TRYCREATE]; false means creating
	// it is known to be impossible and the answer is a plain NO (RFC 9051
	// §6.3.12).
	Create bool
}

// RightsMailbox is the optional interface a [Mailbox] may also implement to
// tell the server the user's [Rights] on each mailbox. folder is a store name,
// as [Folder.Name] holds it; it need not exist, and Create then says whether
// it could be made. With it the server:
//
//   - selects a mailbox the user may read but not write read-only, with
//     [READ-ONLY], and refuses SELECT, EXAMINE and STATUS of one the user may
//     not read with NO [NOPERM];
//   - marks a mailbox the user may not read \Noselect in LIST, without STATUS
//     (RFC 9051 §6.3.9.2);
//   - refuses APPEND, COPY and MOVE into a mailbox that does not exist with NO
//     [TRYCREATE], or a plain NO when Create is false, before the store sees
//     it, so no mailbox is ever made implicitly, and into one the user may not
//     write with NO [NOPERM];
//   - sends APPENDUID and COPYUID only for a destination the user may read.
//
// A lookup error counts as no rights for that command. A Mailbox that does not
// implement it gives the user every right on every mailbox, and the store
// alone decides whether a destination exists ([ErrNoSuchMailbox]).
type RightsMailbox interface {
	MailboxRights(folder string) (Rights, error)
}

// DeletedMailbox is the optional interface a [Mailbox] may also implement to
// say that the store keeps the \Deleted flag itself, so every session sees one
// mark: [Message.Deleted] reports it and [Mailbox.Store] writes it through
// [FlagUpdate.Deleted]. RFC 3501 §2.3.2 and RFC 9051 §2.3.2 make \Deleted a
// permanent flag, kept from session to session.
//
// With it, EXPUNGE, CLOSE and UID EXPUNGE remove the messages DeletedUIDs
// reports at that moment, not the marks this session last saw, so a mark
// another session set is expunged and one it cleared is not (RFC 3501
// §§6.4.2-6.4.3, RFC 4315 §2.1). UID EXPUNGE still removes only those in its
// UID set, and a message the session has not yet been told about stays until
// the session has seen it. If DeletedUIDs fails the command answers NO and
// removes nothing.
//
// A Mailbox that does not implement it keeps the marks in the session as
// before: SELECT seeds them from [Message.Deleted] and EXPUNGE removes the
// messages this session marked.
type DeletedMailbox interface {
	// DeletedUIDs returns the UIDs of the messages in folder that carry
	// \Deleted now, in any order. folder is a store name, as [Folder.Name]
	// holds it.
	DeletedUIDs(folder string) ([]uint32, error)
}

// KeywordMailbox is the optional interface a [Mailbox] may also implement to
// say that the store keeps keywords (RFC 3501 §2.3.2 flag-keywords such as
// $Forwarded, $Junk or a client's own tags): [Mailbox.Store] writes
// [FlagUpdate.Keywords] when [FlagUpdate.KeywordsSet] is true, [Mailbox.Append]
// keeps [FlagUpdate.Keywords], and [Mailbox.Messages] reports them back in
// [Message.Keywords]. With it:
//
//   - SELECT's PERMANENTFLAGS ends with \* (RFC 3501 §7.1), so a client may
//     create keywords, and lists every keyword the folder's messages carry;
//   - STORE ±FLAGS and FLAGS change keywords, APPEND keeps them, and FETCH
//     FLAGS and SEARCH KEYWORD/UNKEYWORD use the stored set;
//   - a message carries at most [MaxKeywords] keywords, each an RFC 3501 atom
//     of 1 to [MaxKeywordLength] bytes, and a STORE or APPEND over the cap is
//     answered NO [LIMIT] (RFC 5530 §3).
//
// Without it the store does not keep keywords: PERMANENTFLAGS has no \*, a
// keyword in STORE is ignored (RFC 3501 §6.4.6 lets the server ignore a flag it
// cannot keep) and APPEND drops keywords.
type KeywordMailbox interface {
	// KeywordLimit is the most distinct keywords the store keeps in one
	// folder, or 0 for no limit of its own. Once the selected folder's messages
	// carry that many, PERMANENTFLAGS lists them without \* (RFC 9051 §7.1)
	// and a STORE, or an APPEND to the selected folder, that would add a new
	// one is answered NO [LIMIT]. The store refuses an APPEND to another folder
	// over its limit itself.
	KeywordLimit() int
}

// FolderMailbox is the optional interface a [Mailbox] may also implement to
// create, delete and rename folders itself, so a folder exists apart from the
// messages in it. Every name is a store name, as [Folder.Name] holds it, with
// "/" between levels of hierarchy. With it the server:
//
//   - answers CREATE OK only once CreateFolder has made the folder, so the next
//     LIST shows it even while it is empty; a name that already exists is NO
//     [ALREADYEXISTS], a trailing "/" is dropped, and any superior name the new
//     one needs is created first (RFC 9051 §6.3.4);
//   - answers DELETE by calling DeleteFolder, which removes the folder and its
//     messages for good; a name that does not exist is NO [NONEXISTENT], and
//     the folder's inferiors stay (RFC 9051 §6.3.5);
//   - answers RENAME by calling RenameFolder for the folder and then for each
//     inferior, after creating any superior the new name needs; a source that
//     does not exist is NO [NONEXISTENT] and a destination that does is NO
//     [ALREADYEXISTS] (RFC 9051 §6.3.6).
//
// INBOX is never created or deleted. RenameFolder("INBOX", to) is the one
// exception to an ordinary rename: it makes the folder to, moves every message
// in INBOX into it and leaves INBOX in place and empty; INBOX's inferiors are
// not renamed (RFC 9051 §6.3.6).
//
// A [RightsMailbox] is consulted too: a name the user could not create is NO
// [CANNOT], and a folder the user may not write is not deleted or renamed (NO
// [NOPERM]). An error from any method answers the command with NO.
//
// A Mailbox that does not implement it keeps implicit folders: CREATE
// acknowledges a valid name without creating anything, DELETE moves the
// folder's messages to Trash and RENAME moves them one by one.
type FolderMailbox interface {
	// CreateFolder makes an empty folder named name, which does not exist.
	CreateFolder(name string) error
	// DeleteFolder removes the folder named name and every message in it.
	DeleteFolder(name string) error
	// RenameFolder gives the folder from the name to, which does not exist,
	// keeping its messages. It does not rename from's inferiors: the server
	// renames each of them itself.
	RenameFolder(from, to string) error
}

// UTF8Mailbox is the optional interface a [Mailbox] may also implement to
// support internationalized email in IMAP ([RFC 6855] UTF8=ACCEPT). When the
// authenticated Mailbox satisfies it, the server advertises UTF8=ACCEPT and
// honours "ENABLE UTF8=ACCEPT" in the authenticated state by calling
// EnableUTF8 once and answering "* ENABLED UTF8=ACCEPT". A Mailbox that does
// not implement it keeps exact existing behaviour: the capability is not
// advertised and ENABLE ignores UTF8=ACCEPT like any unknown extension.
//
// After EnableUTF8 returns nil the client accepts raw UTF-8 header fields
// ([RFC 6532]), so the backend should stop downgrading them for this session.
//
// Implementing it also changes the grammar for the whole session, enabled or
// not: quoted-strings may carry UTF-8 and 8-bit text that is not UTF-8 is BAD;
// CREATE and RENAME refuse mailbox names with controls or line and paragraph
// separators, and a UTF-8 mailbox name is taken before the client enables the
// mode (RFC 6855 §3); APPEND takes the message as "UTF8 (" literal8 ")" and, until
// the client enables UTF-8, refuses one with 8-bit header fields. Once enabled,
// SEARCH with a CHARSET is BAD, and mailbox names travel as UTF-8 both ways
// rather than in modified UTF-7.
//
// [RFC 6855]: https://www.rfc-editor.org/rfc/rfc6855
// [RFC 6532]: https://www.rfc-editor.org/rfc/rfc6532
type UTF8Mailbox interface {
	// EnableUTF8 switches this session's Mailbox to UTF-8 mode. An error
	// leaves the mode off and fails the ENABLE command with NO.
	EnableUTF8() error
}

// Mover is the optional interface a [Mailbox] may also implement to perform an
// atomic server-side MOVE ([RFC 6851]). Without it the server still supports
// MOVE and UID MOVE by calling [Mailbox.Move] once per message; with it the move
// runs as a single backend operation and, when the Mailbox also implements
// [UIDPlusMailbox], the returned destination UID is reported in a COPYUID
// response code. The MOVE capability is advertised for every backend either way.
//
// [RFC 6851]: https://www.rfc-editor.org/rfc/rfc6851
type Mover interface {
	// MoveUID relocates the message named by srcUID to dest as one atomic
	// operation and returns the UID assigned to it in dest, for COPYUID. A
	// backend that cannot report the new UID may return 0; COPYUID is then
	// omitted, as it is when a returned UID repeats in the command.
	MoveUID(srcUID uint32, dest string) (uid uint32, err error)
}

// DateAppender is the optional interface a [Mailbox] may also implement to
// receive the client-supplied INTERNALDATE on APPEND ([RFC 3501] §6.3.11). When
// the concrete Mailbox satisfies it and the APPEND command carries a date-time
// argument, the server calls AppendWithDate instead of [Mailbox.Append] (or
// [UIDPlusMailbox.AppendUID]), passing the parsed time so the store can set the
// stored message's internal date. It returns the UID assigned to the message —
// exactly as [UIDPlusMailbox.AppendUID] does — so that when the Mailbox also
// implements [UIDPlusMailbox] an APPENDUID response code can still be emitted; a
// backend that does not track UIDs may return 0. A Mailbox that does not
// implement this interface keeps exact prior behaviour: a client-supplied
// date-time is parsed and validated but not stored.
//
// [RFC 3501]: https://www.rfc-editor.org/rfc/rfc3501
type DateAppender interface {
	// AppendWithDate delivers raw into dest exactly like [Mailbox.Append] but
	// also sets the message's internal date to internalDate, and returns the UID
	// assigned to the stored message (0 if the backend does not track UIDs).
	AppendWithDate(dest string, f FlagUpdate, internalDate time.Time, raw []byte) (uid uint32, err error)
}

// Limiter is the per-IP connection and authentication guard the server
// consults. It is a structural interface: any type with these methods satisfies
// it. Pass [NopLimiter] (or nil) to impose no limits.
type Limiter interface {
	// Accept reports whether a new connection from ip may proceed, incrementing
	// the in-use count when it returns true.
	Accept(ip string) bool
	// Release decrements the in-use count for ip when a connection ends.
	Release(ip string)
	// RecordAuthFail records an authentication failure from ip.
	RecordAuthFail(ip string)
	// IsBanned reports whether ip is currently banned.
	IsBanned(ip string) bool
	// ResetAuth clears the recorded auth-failure history for ip after a success.
	ResetAuth(ip string)
}

// NopLimiter is a [Limiter] that imposes no limits: it accepts every connection
// and never bans. It is the default when a nil Limiter is passed to server.New
// or server.NewSession.
type NopLimiter struct{}

func (NopLimiter) Accept(string) bool    { return true }
func (NopLimiter) Release(string)        {}
func (NopLimiter) RecordAuthFail(string) {}
func (NopLimiter) IsBanned(string) bool  { return false }
func (NopLimiter) ResetAuth(string)      {}
