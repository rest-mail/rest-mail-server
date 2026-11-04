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
