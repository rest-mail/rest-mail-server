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
- Mailbox access rights (RFC 9051 §§6.3.2, 6.3.9.2, 6.3.12, 6.4.7, 11.4). The
  rev2 rules on READ-ONLY selections, TRYCREATE without implicit creation,
  \Noselect for unopenable LIST-STATUS entries and withholding APPENDUID and
  COPYUID from a caller who cannot read the destination all need the user's
  rights, which upstream's Mailbox does not carry. The optional
  `imap.RightsMailbox` interface reports them; a Mailbox without it keeps every
  right, so existing hosts see no change. DELETE of a \Noselect level with
  inferiors answers `NO [HASCHILDREN]` (§6.3.5) for every host.
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
