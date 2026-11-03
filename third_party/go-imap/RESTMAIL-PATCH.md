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

## CI

GitHub runs only the workflows at a repository's root, so this copy's gates run
from the server's: coverage, the 32-bit check, the checklist, `go vet` and
`golangci-lint` in `.github/workflows/ci.yml`, whenever this directory or that
workflow changes, and the fuzz targets nightly in
`.github/workflows/scheduled.yml`. This copy has no `.github/workflows/`, because
one here would never run. `.github/scripts/check_coverage.py` and `.github/tests`
stay: those steps run them, and `TestReadmeCoverageBadge` reads the gate.

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
