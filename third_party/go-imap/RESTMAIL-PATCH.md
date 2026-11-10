# Import flag compatibility patch

This is go-imap v0.13.2, retained with its original tests and license, plus the
minimal interfaces and protocol changes needed to preserve imported keywords
and seed the engine's existing Deleted set from stored messages. A local module
replacement makes the server build reproducibly until these changes can move
upstream. No installed module cache is modified.

Import permits at most 100 distinct case-insensitive keywords per message,
255 ASCII bytes each, and five system flags. Keywords use RFC 3501 atom syntax
and cannot contain a backslash. FLAGS reports existing mailbox keywords; PERMANENTFLAGS also advertises `\*`.
Keyword STORE is included only because imapsync filters out keywords before
APPEND unless this wildcard is advertised, and RFC 3501 requires the advertised
capability to work. The same syntax, count and length bounds apply to STORE.

## RFC 3501 corrections

These change protocol behaviour from go-imap v0.13.2, and each corrects a defect
found by the RFC 3501 inventory in rest-mail/rest-mail-server#374.

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
  Under EXAMINE, read-only NO still comes before this check.

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
  - `parseMediaType` keeps every segment of a continued parameter, for
    rest-mail/rest-mail-server#1897, where `mime.ParseMediaType` stops at a missing
    segment. `extendedValue` joins what came in segment order, and only a first segment
    names a charset (RFC 2231 §4.1). With no first segment, a plain value of the same
    name stands instead. A multipart's boundary with a segment missing reads as absent,
    because delimiter lines match a whole value (IMAP-BS-11).
  - `parseMediaType` reads a repeated name and a `*` that isn't RFC 2231 form, for the
    same issue. Neither is a syntax error under RFC 2045 §5.1, so §5.2's default doesn't
    apply, though `mime.ParseMediaType` refuses the first and drops the second. The first
    value of a repeated name stands, as `readableField` already had it. Other malformed
    7-bit fields keep §5.2's default (IMAP-BS-21, IMAP-BS-23, IMAP-CDP-03).
  - `commentClosers` is a walk over sorted positions rather than a map, and
    `parameterField` asks `hasReadableType` rather than parsing every parameter again, so
    a very long field costs less to convert. Neither changes the output.
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

## FETCH sends a space for a NUL instead of refusing the message

RFC 3501 §9 leaves NUL out of CHAR8, so FETCH used to answer `NO` for a message
holding one in a header or in text, and a range stopped there. SMTP can deliver such
a message, so one sender could stop an IMAP client reading the mailbox. The owner
chose on rest-mail/rest-mail-server#1921 to send a space instead:

- A NUL in a header or a textual leaf goes out as a space, one octet for one, and so
  does one decoded into an ENVELOPE or BODYSTRUCTURE string. In an 8-bit field the
  space comes first, so no encoded word spells the NUL.
- A NUL in a multipart's boundary can't be a space, which could end the boundary or
  spell another line, so the field and the delimiter lines take the 7-bit spelling an
  8-bit boundary gets (`wireBoundary`).
- Where a NUL is data, a space would corrupt it, so it is encoded. That covers
  non-textual leaves, an encoding nobody can remove, and text in a charset with NUL
  octets (`utf-16`, `utf-32`, `ucs-2`, `ucs-4`).
- `BINARY[]` and `BINARY[n]` still send the stored octets, as a literal8 when they
  hold a NUL. The stored message never changes.
- An encoding that spells a NUL (`=00`, `%00`, base64) in what is sent stays as it
  is, since no NUL octet goes out. ENVELOPE and BODYSTRUCTURE are the exception,
  because the engine decodes those strings itself, and a client decodes the encoded
  words it leaves in them. So a word in a Subject, a description, a name or a
  parameter goes out with a space for each NUL it spells, in its own charset and
  encoding (`SpaceWordNUL`, rest-mail/rest-mail-server#1968): `=?utf-8?Q?a=00.txt?=`
  becomes `=?utf-8?Q?a_.txt?=`. In a charset with NUL octets only U+0000 is a NUL.
- What a client matches rather than decodes keeps the spelling, because a space would
  make it something else: a boundary, an addr-spec, Message-ID, In-Reply-To, Content-ID,
  Content-Location, Content-Language, Content-MD5 and Date.

`wireform.SpaceNUL` is the same rule for a host whose protocol has neither BINARY nor
this conversion, so data stays exact there rather than encoded. `wireform.SpaceWordNUL`
lets a host read a Subject or a part's name as a client decodes it.
`TestFetchRejectsNUL` became `TestFetchReplacesNUL`.

## ENVELOPE takes no buffer to keep a word

`keptWordReader` also has `WriteTo`, which fails as its `Read` does. `io.Copy` takes that
in place of the 32 KiB buffer it would otherwise take for each word, so a list of words
in a charset with no reader costs what a list costs (rest-mail/rest-mail-server#1915).
Nothing sent changes: `mime.WordDecoder` still fails the word, and net/mail still keeps it
as it was written.

## Reading a long field's words and quoted values

`spacedWord` answers a word with no NUL as it is, with nothing built: `=00` is the only
spelling of one in a single-octet Q word, and a base64 word of up to 64 octets decodes on
the stack. `parseKept` reads a plain kept parameter's quoted value itself once the
parameter is over `longQuoted`, 1 KiB, and nothing in it is escaped, and hands
`mime.ParseMediaType` a placeholder in its place. An escaped one still goes the long way,
which is what decides whether a backslash is one (rest-mail/rest-mail-server#1915).
`TestParseKeptReadsLongQuotedValuesAsParseUncommented` compares the two, including which
fields read at all.
