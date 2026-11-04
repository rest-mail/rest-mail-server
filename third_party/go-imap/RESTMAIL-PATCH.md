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
    mailbox has no 7-bit form, so its whole field reads NIL, at the top of a
    message and in an encapsulated one alike. After `ENABLE UTF8=ACCEPT`, valid
    UTF-8 goes out as a literal, as before.
  - An address name in a charset go-imap has no reader for stays the encoded word
    it was sent as, in either session, for rest-mail/rest-mail-server#1885. It
    already names its charset, so no byte is guessed, and the address stays. The
    module has no dependencies, so that is every charset but UTF-8, US-ASCII and
    ISO-8859-1. This relies on `net/mail` keeping a word whose charset reader
    fails on read, which the session tests would catch if it changed.

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
