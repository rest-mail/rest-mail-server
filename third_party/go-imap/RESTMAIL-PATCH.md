# Import flag compatibility patch

This is go-imap v0.13.2, retained with its original tests and license, plus the
minimal interfaces and protocol changes needed to preserve imported keywords
and seed the engine's existing Deleted set from stored messages. A local module
replacement makes the server build reproducibly until these changes can move
upstream. No installed module cache is modified.

Import permits at most 100 distinct case-insensitive keywords per message,
255 ASCII bytes each, and five system flags. Keywords use RFC 3501 atom syntax
and cannot contain a backslash. FLAGS reports existing mailbox keywords; PERMANENTFLAGS also advertises `\*`
when the store implements `imap.KeywordMailbox` and the selected folder is
under the store's `KeywordLimit`. A STORE or APPEND over either bound is
answered `NO [LIMIT]` (RFC 5530 §3).
Keyword STORE is included only because imapsync filters out keywords before
APPEND unless this wildcard is advertised, and RFC 3501 requires the advertised
capability to work. The same syntax, count and length bounds apply to STORE.

## CI

GitHub runs only the workflows at a repository's root, so this copy's gates run
from the server's: coverage, the 32-bit check, the checklist, `go vet` and
`golangci-lint` in `.github/workflows/ci.yml`, whenever this directory or that
workflow changes, and the fuzz targets nightly in
`.github/workflows/scheduled.yml`. This copy has no `.github/workflows/`, because
one here would never run. `.github/scripts/check_coverage.py` and `.github/tests`
stay: those steps run them, and `TestReadmeCoverageBadge` reads the gate.

## Unsolicited FETCH UIDs

Unsolicited flag updates include both UID and FLAGS, including after EXPUNGE
renumbers messages. NOOP, CHECK and IDLE share this formatter. This satisfies
RFC 9051 §§6.3.13 and 7.5.2 without an IMAP4rev2 mode switch: RFC 3501 allows
the additional UID item. Solicited STORE responses are unchanged, and ordinary
FETCH responses already include UID.

`TestUnsolicitedFetchIncludesUID` proves the public-session behavior with sparse
UIDs, external expunges, flag changes and new arrivals, under SELECT and EXAMINE.
The gateway's `TestServer_UnsolicitedFetchIncludesUID` also proves that a silent
STORE in another gateway session produces the correct UID-bearing update.

## RFC 3501 corrections

These change protocol behaviour from go-imap v0.13.2, and each corrects a defect
found by the RFC 3501 inventory in rest-mail/rest-mail-server#374.

- SELECT, EXAMINE and STATUS ask a store that implements `imap.UIDNextMailbox`
  for UIDNEXT, so it no longer falls when the newest message is expunged
  (RFC 3501 §2.3.1.1, rest-mail/rest-mail-server#804). A store without it
  keeps the highest UID present plus one.

- A SELECT or EXAMINE refused with BAD no longer drops the selected mailbox,
  because "a rejected command (BAD response) never changes the state of the
  connection or of the selected mailbox" (RFC 3501 §6). That holds both for a
  parse failure in `handleSelect` and for a refusal while the command line and
  its literals are read in `Handle`. A failed selection, answered NO, still
  deselects. `TestSelectionInvalidArguments` asserted the old deselection, so
  it is corrected rather than retained as it was upstream.
- STORE and UID STORE naming `\Recent`, in any case, are refused with BAD and
  write nothing, because the §9 `flag` production "does not include" it. A
  keyword spelled `Recent` is unaffected. `TestStoreLeavesRecentAlone` asserted
  that the flag was accepted and ignored, so it is corrected in the same way.
- A STORE or UID STORE that breaks the grammar is BAD under EXAMINE too. The
  read-only NO now comes after the syntax checks (§2.2.2).
- Three fixes are taken from rest-mail/go-imap, where they are already merged:
  a literal is refused before its octets are read where the grammar takes an
  atom, number or date (rest-mail/go-imap#173, `literal_type.go`); an empty
  command line answers `* BAD` (#176); and CREATE, DELETE, RENAME, SUBSCRIBE and
  UNSUBSCRIBE take exactly their mailbox astrings (#175). The last is enforced
  before dispatch here rather than in each handler as upstream does, because
  open pull requests in this repository change those handlers.
- These go further than upstream and are proposed there as
  rest-mail/go-imap#179, found by the command-grammar rows of
  rest-mail/rest-mail-server#374:
  - A line ends only in CRLF (§2.2). A bare LF or a stray CR is BAD, tagged when
    the line starts with a tag, and a line that ends in a non-synchronizing
    literal marker ends the session because its octets are in flight.
  - SEARCH, UID SEARCH, LIST and LSUB refuse a leading, trailing or doubled SP,
    and an SP just inside parentheses (§§2.2.2, 9). LIST and LSUB need both their
    reference and pattern; they used to default a missing one, and LSUB ignored
    what followed.
  - A literal holding NUL is BAD in any argument (§9), since LIST and LSUB took
    one as a pattern.
  - AUTHENTICATE with a mechanism that is not an atom is BAD, not the NO of an
    unsupported mechanism (§9).
  - APPEND naming `\Recent` is BAD, not NO, before any continuation (§§2.3.2, 9).
  - APPEND whose message literal is not followed by CRLF says BYE before it
    closes, as a server must (§3.4).
  - Response text carries no client literal or host error text (§9): STATUS and
    LIST no longer echo an unknown item, which a literal could fill with CR, LF
    and 8-bit octets, and STORE gives the fixed text UID STORE already did. The
    host's error goes to the log.
- ENVELOPE addresses, in top-level and encapsulated envelopes alike:
  - A source route is kept, because addr-adl "Holds route from [RFC-2822]
    route-addr if non-NIL" (RFC 3501 §9). `net/mail` refuses a route, which made
    the whole header NIL, so the route is taken out before `net/mail` reads the
    list and put back by address. Worth sending upstream. It goes no further than
    routes: other legal obsolete forms that `net/mail` refuses are
    rest-mail/rest-mail-server#1862.
  - A local part ends at the last `@`, because addr-mailbox holds the "local-part
    after removing [RFC-2822] quoting" (§9). `net/mail` removes that quoting, so
    `"a@b"@example.com` reaches the engine as `a@b@example.com`, which it used to
    split at the first `@`. Worth sending upstream.

## Search zone

`SearchZoneMailbox` is an optional interface for rest-mail/rest-mail-server#1557.
RFC 3501 §6.4.4 compares the internal date "disregarding time and timezone", and
IMAP gives a client no way to say which zone its dates are in, so the store's
stored instants need a zone to have a calendar day at all. REST Mail stores them
in UTC and names the account's own zone here. A mailbox without the interface
keeps upstream behaviour.

- FETCH converts a message as it is sent, for rest-mail/rest-mail-server#1861,
  because headers "MUST be 7-bit" and "non-textual data such as binary data MUST
  be transfer encoded" (RFC 3501 §7.4.2). The octets `Mailbox.Fetch` returns are
  never changed, and BINARY and SEARCH still read them; `RFC822`, `BODY[...]`,
  partial offsets and `BODYSTRUCTURE` use the converted form, and a host reports
  its length as `Message.Size`, measured with `wireform.Convert`. Only what breaks
  the rule is rewritten, so a message already 7-bit goes out byte for byte and a
  signature over it still verifies from FETCH output. A converted message's
  signature fails if a client re-verifies it from FETCH output, and still verifies
  over the store. These are implementation defaults, not owner decisions:
  - Valid UTF-8 header text becomes RFC 2047 encoded words in charset `UTF-8`;
    any other 8-bit text uses `UNKNOWN-8BIT`, so no charset is guessed.
    Content-Type and Content-Disposition parameters become RFC 2231 extended
    values instead, because an encoded word may not stand in a parameter.
  - 8-bit text stays when its section names a charset. Text naming none goes out
    QUOTED-PRINTABLE, and non-textual data BASE64. A body already declared base64
    or quoted-printable keeps that encoding, so nothing is encoded twice.
  - After `ENABLE UTF8=ACCEPT`, valid UTF-8 stays in headers and in text naming no
    charset, whose default there is UTF-8 (RFC 6532 §3).
  - ENVELOPE and BODYSTRUCTURE read the converted message, for
    rest-mail/rest-mail-server#1882, because those strings have nowhere to name a
    charset (RFC 3501 §4.3.1). Without UTF-8, a string a decoder produced (a name
    from an encoded word, a parameter from RFC 2231) goes out as encoded words,
    even a parameter, since the rule against that (RFC 2047 §5) is about message
    headers and an IMAP string can hold nothing else. An address with an 8-bit
    mailbox has no 7-bit form, so it is left out of its field, which reads NIL when
    none remains, at the top of a message and in an encapsulated one alike. After `ENABLE UTF8=ACCEPT`, valid
    UTF-8 goes out as a literal, as before.
  - An address name in a charset go-imap has no reader for stays the encoded word
    it was sent as, in either session, for rest-mail/rest-mail-server#1885. It
    already names its charset, so no byte is guessed, and the address stays. The
    module has no dependencies, so that is every charset but UTF-8, US-ASCII and
    ISO-8859-1. This relies on `net/mail` keeping a word whose charset reader
    fails on read, which the session tests would catch if it changed.
  - An RFC 2231 parameter in such a charset gets the same treatment, for
    rest-mail/rest-mail-server#1886, and go-imap reads these parameters itself
    because `mime.ParseMediaType` drops them. The value goes out as one encoded word
    naming the charset, in either session, however long, because go-imap can't tell
    where a character of that charset ends and a word may not split one (RFC 2047
    §5). That outranks the 75-character limit (§2). A boundary or a charset name
    that is ASCII stays plain text, because the structure depends on it.
  - Converting a Content-Type or Content-Disposition field changes only the
    parameter holding an 8-bit octet, and the field is otherwise as it was sent.
    A plain 8-bit value beside an extended form of the same parameter becomes encoded
    words in its quoted-string, against RFC 2047 §5, because a second extended form
    would make the field unreadable. Readers take the extended form.
  - A Content-Type or Content-Disposition holding an 8-bit octet is read without
    refusing it, for rest-mail/rest-mail-server#1890 and #1894, because
    `mime.ParseMediaType` refuses the whole field and the entity would read as
    `text/plain`. `readField` splits it at each `;` outside a quoted-string or comment
    that closes, in time linear in the field. A `"` or `(` that never closes and a
    stray `)` are text. A value runs from its first character that isn't white space
    or a comment to its last, so a comment with value text on both sides is that text,
    and one around the value is still a comment. A field with no 8-bit octet keeps the
    strict reading (IMAP-BS-21, IMAP-BS-23, IMAP-CDP-03).
  - RFC 2045 has no spelling for an 8-bit parameter name, and neither RFC 2231 nor an
    encoded word reaches one, so it goes out as a token with `%XX`, as in
    `n%C3%A4me=x`. BODYSTRUCTURE reads that too, or the UTF-8 name after UTF8=ACCEPT.
    Conversion rewrites a 7-bit parameter only when the 7-bit result couldn't be read
    otherwise, because such a field no longer gets the lenient reading: a malformed
    value becomes a quoted-string. A repeated name, and an unbalanced delimiter after
    the type, go in a comment as an encoded word, because a second value would make
    the field unreadable.
  - A boundary is octets that delimiter lines match, so `keptValue` no longer decodes
    an 8-bit one, whatever charset its RFC 2231 form names. `wireMultipart` gives the
    header and every delimiter line one spelling for a boundary that can't go out as
    stored, for rest-mail/rest-mail-server#1893: each octet outside RFC 2046 §5.1.1's
    characters is `=XX`, in at most 70 characters. That spelling, or a 7-bit boundary
    conversion would make collide, takes a suffix hashed from the converted body when
    a line there would otherwise match, so the parts stay where the stored message has
    them, and every FETCH and size measurement gets the same octets.
  - `parseMediaType` keeps a continued parameter with no first segment, for
    rest-mail/rest-mail-server#1897, where `mime.ParseMediaType` and `extendedValue`
    both stop at a missing `*0`. `laterSegments` joins what came in segment order, with
    no charset, since only a first segment can name one (RFC 2231 §4.1). A plain value
    of the same name stands instead, and a multipart's boundary stays absent, because
    delimiter lines match a whole value (IMAP-BS-11).
  - `SevenBitBoundary` is new and exported through `wireform`, for the same issue. FETCH
    doesn't use it. It gives a host that sends the body as stored, as a POP3 server does,
    what `wireMultipart` gives FETCH for a top-level 8-bit boundary: one spelling in
    Content-Type and on the delimiter lines. Other octets stay. It adds no dependency.

- SEARCH decodes a header one encoded word at a time, for
  rest-mail/rest-mail-server#1887, rather than through
  `mime.WordDecoder.DecodeHeader`, which gives up on the whole header at a charset
  with no reader. It finds each word's bounds itself to do that: a word no longer
  spans lines, and a word it can't decode ends at white space.

## RFC 9051 additions

These add IMAP4rev2 behaviour from RFC 9051 ahead of the `IMAP4rev2`
capability, for rest-mail/rest-mail-server#718. `IMAP4rev2` is still not
advertised.

- STATUS, and LIST's STATUS return option, take `DELETED`: "the number of
  messages that have the \Deleted flag set" (RFC 9051 §6.3.11). It counts the
  stored `Deleted` flag, which STORE writes through to the backend. Other
  unknown STATUS items are still refused with BAD. A rev1 client never asks
  for it, so it is accepted in every session.
- SELECT and EXAMINE can send `* OK [CLOSED] Previous mailbox is now closed`
  before any response about the new mailbox, because "when deselecting a
  selected mailbox, the server MUST return an untagged OK response with the
  "[CLOSED]" response code" (RFC 9051 §6.3.2). It is sent when a well-formed
  selection replaces one, even if the new selection then fails; never after a
  BAD, CLOSE or UNSELECT ("There is no need to return the CLOSED response code
  on completion of the CLOSE or the UNSELECT command", §7.1). It is sent only
  to an IMAP4rev2 session, and no command makes one yet: RFC 3501 has no
  CLOSED and asks that other codes "SHOULD be prefixed with an "X" until they
  are added to a revision of this protocol" (§7.1), and RFC 7162 §3.2.11
  requires CLOSED of a server that "advertises "QRESYNC" or "CONDSTORE"",
  which this one does not. ENABLE IMAP4rev2 will turn it on.

## Banned clients are refused before their credentials are checked

The [Limiter] ban used to be read only after a failed LOGIN or AUTHENTICATE,
so a banned client could open a new connection and sign in with the right
password (rest-mail/rest-mail-server#1678). A banned address is now refused in
the greeting with `* BYE`, and LOGIN or AUTHENTICATE from a session opened
before the ban gets `* BYE` and a tagged `NO [UNAVAILABLE]` (RFC 5530: a
temporary refusal, unlike AUTHENTICATIONFAILED) without calling the backend,
then the connection closes. The failure that earns the ban gets the same reply.
`TestAuthenticationFailureBudgetAppliesToBothForms` asserted the old reply text,
so it is corrected.

  to an IMAP4rev2 session, or when the store keeps mod-sequences: RFC 3501
  has no CLOSED and asks that other codes "SHOULD be prefixed with an "X"
  until they are added to a revision of this protocol" (§7.1), and RFC 7162
  §3.2.11 requires CLOSED of a server that "advertises "QRESYNC" or
  "CONDSTORE"", which this one does only with such a store. ENABLE IMAP4rev2
  will turn it on for every session.
- Mailbox access rights (RFC 9051 §§6.3.2, 6.3.9.2, 6.3.12, 6.4.7, 11.4). The
  rev2 rules on READ-ONLY selections, TRYCREATE without implicit creation,
  \Noselect for unopenable LIST-STATUS entries and withholding APPENDUID and
  COPYUID from a caller who cannot read the destination all need the user's
  rights, which upstream's Mailbox does not carry. The optional
  `imap.RightsMailbox` interface reports them; a Mailbox without it keeps every
  right, so existing hosts see no change. DELETE of a \Noselect level with
  inferiors answers `NO [HASCHILDREN]` (§6.3.5) for every host.
- Mailbox name normalization (RFC 9051 §§5.1, 6.3.9.7, go-imap#178,
  rest-mail/rest-mail-server#1669). Upstream keeps every name as the client
  spelled it and never sends `OLDNAME`, though §5.1 asks for Net-Unicode, whose
  form is NFC. This module stays free of dependencies, so the optional
  `imap.NameNormalizer` interface lets the store supply the form, and the gateway
  gives it the same NFC the API saves names in. Every name a command carries goes
  through it, a LIST pattern included, so a client's other spelling reaches the
  same folder. SELECT, EXAMINE, CREATE, DELETE, RENAME and APPEND send the untagged
  LIST with `OLDNAME` when a name changed, and only then. That includes an
  IMAP4rev1 session: RFC 3501 §7 has a client accept any response at any time, and
  it is drawn only by a client that sent a name in another form. STATUS, COPY, MOVE,
  SUBSCRIBE and GETQUOTAROOT convert the name without reporting it, because §6.3.9.7
  does not list them. INBOX in another case is folded as before and not reported;
  the `select inbox` example in RFC 9051 Appendix B waits on ENABLE IMAP4rev2. A
  Mailbox without the interface keeps every name as spelled.
- Folder management (RFC 9051 §§6.3.4-6.3.6, go-imap#160). Upstream's
  folders are implicit, so CREATE answers OK without creating anything and the
  new folder vanishes from the next LIST, though "the server MUST NOT return
  OK unless a new mailbox was created with that name" (§6.3.4). The optional
  `imap.FolderMailbox` interface lets the store create, delete and rename
  folders: CREATE then answers once the folder exists, DELETE removes it for
  good and RENAME renames it with its inferiors, INBOX by moving its messages.
  A Mailbox without it keeps implicit folders.
- Atomic set COPY and MOVE (RFC 3501 §6.4.7, RFC 6851 §3.3, go-imap#161).
  Upstream calls the backend once per message, so a later failure leaves the
  earlier copies in the destination. The optional `imap.SetMailbox`
  interface hands the store the whole set in one call, all or none; a Mailbox
  without it keeps the per-message path.
- `\Deleted` kept by the store (RFC 3501 §2.3.2, go-imap#159). Upstream keeps
  the mark in the session until EXPUNGE, so the user's other devices never see
  it and a dropped connection loses it. The optional `imap.DeletedMailbox`
  interface says the store keeps it; EXPUNGE, CLOSE and UID EXPUNGE then remove
  what the store marks at that moment, so a mark another session cleared is
  never expunged. A Mailbox without it keeps the session's marks.
- LIST-EXTENDED in full (RFC 5258, folded into RFC 9051 §6.3.9) needs a real
  subscription list: "the list MUST include exactly those mailbox names that
  match the canonical list pattern and are subscribed to" (§3.1), names that
  no longer exist included. The optional `imap.Subscriber` interface gives the
  host that list; SUBSCRIBE, UNSUBSCRIBE, LSUB and the SUBSCRIBED selection and
  return options use it, and RFC 5258 §5 examples 2, 3, 5, 6, 8, 9 and 10 run
  against it in `rfctest`. A Mailbox without it keeps the all-folders-subscribed
  model, so existing hosts see no change.

## RFC 2971 ID

Added for rest-mail/rest-mail-server#1614, so an IMAP import summary (#1498)
and the logs can name the client, such as imapsync, which sends ID whenever the
server advertises it. New code only: `pkg/imap/id.go`, `internal/wire/id.go`
and `server/id.go`, plus the dispatch case, the CAPABILITY token, the session
fields and one call after authentication.

- ID is advertised in every state, because "This command is valid in any
  state" (RFC 2971 §3.1). IMAP4rev2 is still not advertised.
- `ID NIL` and `ID (field value ...)` are answered with one untagged ID and
  OK. The server's list is the name and version set with
  `Server.SetServerID`/`Session.SetServerID`, or NIL when none is set, which
  §3.2 allows ("a server MAY send NIL in place of the list"). No host, OS or
  contact detail is sent (§7).
- The client's data is held to §3.3: "Field strings MUST NOT be longer than 30
  octets. Value strings MUST NOT be longer than 1024 octets. Implementations
  MUST NOT send more than 30 field-value pairs." and "MUST NOT send the same
  field name more than once", with "Strings are not case-sensitive". Data past
  a bound, or breaking §4's grammar, is answered BAD ("BAD command unknown or
  arguments invalid", §3.1), with no ID response; the fields already held stay.
- The fields are kept on the session and handed, in order, to a `Mailbox`
  implementing the new optional `imap.IDMailbox`, once the session is
  authenticated or when ID arrives later. The session logs the client's name
  and version only, at info level. Nothing about how a session is served reads
  them, as §3 requires: "Implementations MUST NOT make operational changes
  based on the data sent as part of the ID command or response."
- `TestIDExamples` replays the RFC's §3.1 and §3.2 examples;
  `TestIDStoredForTheMailbox` and `TestIDLimits` cover storage and the bounds
  (CHECKLIST.md IMAP-ID-01 to IMAP-ID-05). The CAPABILITY example output in
  `server/example_test.go` gains ID.
- CONDSTORE and QRESYNC (RFC 7162, go-imap#162). Without them a client that
  reconnects must FETCH every message's flags to learn what changed. The
  optional `imap.ModSeqMailbox` interface (`HighestModSeq`, `StoreModSeq`)
  gives the server a folder's HIGHESTMODSEQ and each message's
  `Message.ModSeq`, and turns on CONDSTORE: ENABLE CONDSTORE, SELECT/EXAMINE
  (CONDSTORE) with HIGHESTMODSEQ or NOMODSEQ, STATUS HIGHESTMODSEQ, FETCH
  MODSEQ and CHANGEDSINCE, STORE UNCHANGEDSINCE with MODIFIED, and SEARCH
  MODSEQ (with ESEARCH's MODSEQ). `imap.VanishedMailbox` (`Vanished`) adds the
  record of expunged UIDs and turns on QRESYNC: ENABLE QRESYNC, SELECT/EXAMINE
  (QRESYNC ...), UID FETCH (CHANGEDSINCE n VANISHED), and VANISHED in place of
  EXPUNGE once enabled. A Mailbox without them advertises neither.
## FETCH sends a space for a NUL instead of refusing the message

RFC 3501 §9 leaves NUL out of CHAR8, so FETCH used to answer `NO` for a message
holding one in a header or in text, and a range stopped there. SMTP can deliver such
a message, so one sender could stop an IMAP client reading the mailbox. The owner
chose on rest-mail/rest-mail-server#1921 to send a space instead:

- A NUL in a header or a textual leaf goes out as a space, one octet for one, and so
  does one decoded into an ENVELOPE or BODYSTRUCTURE string. In an 8-bit field the
  space comes first, so no encoded word spells the NUL.
- Where a NUL is data, a space would corrupt it, so it is encoded. That covers
  non-textual leaves, an encoding nobody can remove, and text in a charset with NUL
  octets (`utf-16`, `utf-32`, `ucs-2`, `ucs-4`).
- `BINARY[]` and `BINARY[n]` still send the stored octets, as a literal8 when they
  hold a NUL. The stored message never changes.
- An encoding that spells a NUL (`=00`, `%00`, base64) in what is sent stays as it
  is, since no NUL octet goes out. ENVELOPE and BODYSTRUCTURE are the exception,
  because the engine decodes those strings itself.

`wireform.SpaceNUL` is the same rule for a host whose protocol has neither BINARY nor
this conversion, so data stays exact there rather than encoded.
`TestFetchRejectsNUL` became `TestFetchReplacesNUL`.
