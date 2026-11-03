# go-imap

[![CI](https://github.com/rest-mail/go-imap/actions/workflows/ci.yml/badge.svg)](https://github.com/rest-mail/go-imap/actions/workflows/ci.yml)
[![Coverage gate: >=95%](https://img.shields.io/badge/coverage%20gate-%3E%3D95%25-brightgreen)](COVERAGE.md)
[![Partial checklist: 500/707 mapped](https://img.shields.io/badge/partial%20checklist-500%2F707%20mapped-blue)](CHECKLIST.md)
[![Go Reference](https://pkg.go.dev/badge/github.com/rest-mail/go-imap.svg)](https://pkg.go.dev/github.com/rest-mail/go-imap/pkg/imap/server)
[![Go Report Card](https://goreportcard.com/badge/github.com/rest-mail/go-imap)](https://goreportcard.com/report/github.com/rest-mail/go-imap)

An IMAP4rev1 ([RFC 3501]) server engine for Go, with the extensions a modern
mail client expects. Standard library only: no external dependencies.

You supply a `Backend` that authenticates users and returns a `Mailbox`: a view
over one user's folders and messages, expressed as plain `Message` values. The
server speaks the protocol. It makes no assumptions about where mail lives, so a
backend can be a database, a maildir or a remote API.

## Contents

- [Install](#install)
- [Packages](#packages)
- [Quick start](#quick-start): [run a server](#run-a-server), [optional interfaces](#optional-interfaces), [one connection](#one-connection)
- [Features](#features): [capabilities](#capabilities), [commands](#commands), [server API](#server-api)
- [What the library does and what the host must do](#what-the-library-does-and-what-the-host-must-do)
- [RFC conformance](#rfc-conformance): [the checklist](#the-rfc-checklist), [what it does not do](#what-it-does-not-do), [badges and coverage](#badges-and-coverage)
- [Changelog](#changelog)
- [Licence](#licence)

## Install

```sh
go get github.com/rest-mail/go-imap@latest
```

The module declares go 1.25.

## Packages

| Import path | What it holds |
|-------------|---------------|
| `github.com/rest-mail/go-imap/pkg/imap` | The types a backend implements and exchanges with the server: `Backend`, `Mailbox`, `Message`, `Address`, `Folder`, `FlagUpdate`, the `SpecialUse*` values, the optional `UIDPlusMailbox`, `UTF8Mailbox`, `Mover` and `DateAppender` interfaces, `ErrNoSuchMailbox`, `Limiter` and `NopLimiter`. |
| `github.com/rest-mail/go-imap/pkg/imap/server` | The server: `Server`, `New`, `Ports`, `Session`, `NewSession`, `MaxCommandLineLength` and `MaxLiteralSize`. |
| `pkg/imap/internal/wire` | The command and response grammar: atoms, quoted strings, literals, sequence sets, flag lists, dates and FETCH items. Internal: not importable. |
| `pkg/imap/internal/message` | Reads a stored message: sections, BODYSTRUCTURE, ENVELOPE, BINARY and the decoded text SEARCH matches. Internal: not importable. |
| `pkg/imap/internal/mutf7` | Modified UTF-7, the 7-bit form of mailbox names. Internal: not importable. |
| `pkg/imap/rfctest` | The black-box RFC test suite, driving only the public API. Tests only. |

Upgrading from v0.12 or earlier: the root package is gone and `NewServer` is
`server.New`. [CHANGELOG.md](CHANGELOG.md#0130---2026-10-26) has the full
old-to-new table.

## Quick start

### Run a server

Implement `Backend` and `Mailbox`, then hand the server a TLS config and the
ports to listen on. The `account` type is a sketch: fill in the method bodies
against your own store.

```go
package main

import (
	"crypto/tls"
	"log"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// store is your mail store. Authenticate returns a Mailbox scoped to one user.
type store struct{} // your database handle, etc.

func (s *store) Authenticate(user, pass string) (imap.Mailbox, error) {
	// verify the credentials, then return the user's view
	return &account{}, nil
}

// account is one user's mail. Fill in the method bodies against your store.
type account struct{}

func (a *account) Folders() ([]imap.Folder, error) {
	return []imap.Folder{
		{Name: "INBOX"},
		{Name: "Sent", SpecialUse: imap.SpecialUseSent},
		{Name: "Trash", SpecialUse: imap.SpecialUseTrash},
	}, nil
}

func (a *account) Messages(folder string) ([]imap.Message, error) {
	// oldest first; each UID stable and ascending with arrival
	return []imap.Message{
		{UID: 101, Size: 4213, Subject: "hello",
			From: imap.Address{Name: "Alice", Email: "alice@example.com"}},
	}, nil
}

func (a *account) Fetch(uid uint32) ([]byte, error)                        { return nil, nil } // the stored RFC 5322 octets
func (a *account) Store(uid uint32, f imap.FlagUpdate) error               { return nil }
func (a *account) Move(uid uint32, dest string) error                      { return nil }
func (a *account) Delete(uid uint32) error                                 { return nil } // EXPUNGE
func (a *account) Copy(uid uint32, dest string) error                      { return nil }
func (a *account) Append(dest string, f imap.FlagUpdate, raw []byte) error { return nil }
func (a *account) Quota() (used, limit int64, err error)                   { return 0, 0, nil }

func main() {
	cert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
	if err != nil {
		log.Fatal(err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}

	// A nil Limiter is imap.NopLimiter: no per-IP limits.
	srv := server.New("mail.example.com", &store{}, tlsConfig, nil)
	if err := srv.ListenAndServe(server.Ports{IMAP: 143, IMAPTLS: 993}); err != nil {
		log.Fatal(err)
	}
	select {} // serve until srv.Shutdown(ctx) or srv.Close()
}
```

### Optional interfaces

Richer extensions are opt-in. The server type-asserts the `Mailbox` that
`Authenticate` returns, and a `Mailbox` that implements only the base interface
keeps its exact behaviour. Add the methods and the matching capability and
response codes turn on by themselves:

```go
// UIDPlusMailbox: UIDPLUS, APPENDUID and COPYUID, the real UIDVALIDITY.
func (a *account) UIDValidity(folder string) (uint32, error) { return 1, nil }
func (a *account) AppendUID(dest string, f imap.FlagUpdate, raw []byte) (uint32, error) {
	return 0, nil // store the message, return its new UID
}
func (a *account) CopyUID(srcUID uint32, dest string) (uint32, error) {
	return 0, nil // copy the message, return the copy's UID
}

// Mover: MOVE as one atomic backend operation.
func (a *account) MoveUID(srcUID uint32, dest string) (uint32, error) {
	return 0, nil // move the message, return its UID in dest
}
```

| Interface | Methods | What it turns on |
|-----------|---------|------------------|
| `UIDPlusMailbox` | `UIDValidity`, `AppendUID`, `CopyUID` | UIDPLUS ([RFC 4315]): `APPENDUID` on APPEND, `COPYUID` on COPY and MOVE, the real `UIDVALIDITY` in SELECT, and `UID EXPUNGE`. |
| `Mover` | `MoveUID` | MOVE ([RFC 6851]) as one atomic backend call per message. Without it MOVE still works, through `Mailbox.Move`; with it and `UIDPlusMailbox`, MOVE reports `COPYUID`. |
| `UTF8Mailbox` | `EnableUTF8` | UTF8=ACCEPT ([RFC 6855]), switched on per session with `ENABLE UTF8=ACCEPT`: UTF-8 quoted strings and mailbox names, raw UTF-8 headers (RFC 6532), and APPEND's `UTF8 (~{n})` data item. |
| `DateAppender` | `AppendWithDate` | APPEND's date-time argument reaches the store as the message's INTERNALDATE. Without it the date is checked and then dropped. |

### One connection

To serve a connection you accepted yourself, behind your own listener, build a
session and run it:

```go
sess := server.NewSession(conn, backend, "mail.example.com", tlsConfig, limiter)
sess.Handle() // returns when the connection ends
```

`Example` in [`pkg/imap/server/example_test.go`](pkg/imap/server/example_test.go)
drives one whole conversation this way over an in-memory pipe.

## Features

### Capabilities

| Capability | RFC | When the server advertises it |
|------------|-----|-------------------------------|
| IMAP4rev1 | [RFC 3501] | always |
| STARTTLS, LOGINDISABLED | RFC 3501, RFC 2595 | a TLS config is set and the connection is still cleartext |
| AUTH=PLAIN | RFC 3501, RFC 4616 | once the connection is protected, or always when no TLS config is set (for development) |
| SASL-IR | RFC 4959 | with AUTH=PLAIN |
| LITERAL- | RFC 7888 | always: non-synchronizing literals up to 4096 octets |
| IDLE | RFC 2177 | always |
| MOVE | [RFC 6851] | always; atomic when the `Mailbox` implements `Mover` |
| QUOTA | RFC 2087 | always |
| UNSELECT | RFC 3691 | always |
| ENABLE | RFC 5161 | always |
| SPECIAL-USE | RFC 6154 | always; each folder's role comes from `Folder.SpecialUse` |
| NAMESPACE | RFC 2342 | always: one personal namespace, `(("" "/"))` |
| ESEARCH | RFC 4731 | always: `SEARCH RETURN (MIN MAX ALL COUNT)` |
| SEARCHRES | RFC 5182 | always: `SEARCH RETURN (SAVE)` and `$` |
| LIST-EXTENDED | RFC 5258 | always |
| LIST-STATUS | RFC 5819 | always |
| STATUS=SIZE | RFC 8438 | always: the sum of the folder's `RFC822.SIZE` |
| BINARY | RFC 3516 | always: sections with their transfer encoding removed |
| UIDPLUS | [RFC 4315] | the `Mailbox` implements `UIDPlusMailbox` |
| UTF8=ACCEPT | [RFC 6855] | the `Mailbox` implements `UTF8Mailbox` |

The list is computed per session from what the concrete `Mailbox` implements,
so it never promises more than the backend does.

### Commands

CAPABILITY, NOOP, LOGOUT, STARTTLS, LOGIN, AUTHENTICATE PLAIN, SELECT, EXAMINE,
CREATE, DELETE, RENAME, SUBSCRIBE, UNSUBSCRIBE, LIST, LSUB, NAMESPACE, STATUS,
APPEND, CHECK, CLOSE, UNSELECT, EXPUNGE, SEARCH, FETCH, STORE, COPY, MOVE, the
UID forms of SEARCH, FETCH, STORE, COPY, MOVE and EXPUNGE, ENABLE, IDLE,
GETQUOTA and GETQUOTAROOT.

FETCH answers `BODY[]`, `BODY.PEEK[]` and every section form (`HEADER`,
`HEADER.FIELDS[.NOT]`, `TEXT`, `MIME`, numeric part paths, partial ranges),
`BINARY[]`, `BINARY.PEEK[]`, `BINARY.SIZE[]`, `BODY`, `BODYSTRUCTURE`,
`ENVELOPE`, `FLAGS`, `INTERNALDATE`, `RFC822`, `RFC822.HEADER`, `RFC822.SIZE`,
`RFC822.TEXT`, `UID` and the `ALL`, `FAST` and `FULL` macros. `BODY.PEEK[]`
never sets `\Seen`; a plain `BODY[]` does (RFC 3501 §6.4.5).

### Server API

- `server.New(hostname, backend, tlsConfig, limiter)` and
  `(*Server).ListenAndServe(Ports{IMAP, IMAPTLS})` open the listeners (143 for
  STARTTLS, 993 for implicit TLS; a zero port is not opened) and serve in the
  background.
- `(*Server).Shutdown(ctx)` stops accepting and waits for the open sessions to
  finish (or returns `ctx.Err()`), telling each client why with an untagged
  `BYE`; `(*Server).Close()` stops at once and closes live connections. It is
  the same split as `net/http.Server`.
- `server.NewSession(conn, backend, hostname, tlsConfig, limiter)` and
  `(*Session).Handle()` serve one connection you accepted yourself.
- `SetIdlePollInterval` on a `Server` or a `Session` sets how often IDLE re-reads
  the selected folder to report other sources' changes. The default is 15
  seconds.
- `server.MaxCommandLineLength` (8 KiB) bounds a command line, and
  `server.MaxLiteralSize` (8 KiB) bounds a literal inside a command's arguments.
  APPEND's message literal has its own 10 MiB bound. Both are package variables
  you can tune.
- `imap.Limiter` is the per-IP guard: `Accept`/`Release` cap connections, and
  `RecordAuthFail`/`IsBanned`/`ResetAuth` ban an address after failed sign-ins.
  Any type with those methods satisfies it; `imap.NopLimiter` (or nil) imposes
  no limits. A refused connection is greeted with `BYE` before it closes.
- A panic in a command handler, the backend included, ends that session with
  `BYE` and leaves the server running.
- A session idle for 30 minutes is logged out with `BYE` (RFC 3501 §5.4).

## What the library does and what the host must do

Each area below says what go-imap handles and what stays with the host, the
program that implements the backend. The [checklist](CHECKLIST.md) records
these boundaries row by row.

### Authentication and TLS

The library:

- Answers LOGIN and AUTHENTICATE PLAIN, with an initial response on the
  command line (SASL-IR) or after a continuation. Both go through
  `Backend.Authenticate`.
- With a TLS config, refuses LOGIN and withholds AUTH=PLAIN on a cleartext
  connection until STARTTLS, advertises LOGINDISABLED meanwhile, and discards
  any cleartext the client pipelined after STARTTLS.
- Refuses a second LOGIN or AUTHENTICATE once the session is authenticated, and
  an authorization identity that names a different user.
- Answers `BAD` to 8-bit text outside a literal, unless the backend supports
  UTF8=ACCEPT (RFC 3501 §9). A client sends 8-bit credentials in a literal or
  through AUTHENTICATE PLAIN.
- Reports every failed sign-in to the `Limiter`, and closes the connection once
  the `Limiter` reports the address banned.

The host:

- Verifies credentials in `Authenticate`; any error is a `NO` to the client.
- Supplies the TLS certificate. With no TLS config, plaintext AUTH is offered:
  use that for local development only.
- Implements a `Limiter` for per-IP connection caps and sign-in bans.

### Folders and mailbox names

The library:

- Takes and returns folder names in UTF-8. Until a client enables UTF8=ACCEPT,
  it writes names in modified UTF-7 on the wire and decodes them on the way in
  (RFC 3501 §5.1.3), and refuses a name that is not valid modified UTF-7.
- Treats INBOX case-insensitively, and only INBOX.
- Answers LIST, LSUB and LIST-EXTENDED from `Mailbox.Folders`, with hierarchy
  levels under `%` and each folder's special-use role.
- Composes DELETE and RENAME from `Messages` and `Move`, since folders are
  implicit: DELETE moves the folder's messages to `Trash`, and RENAME moves
  them, with any inferior folders, to the new name. It refuses to delete or
  rename INBOX, Sent, Drafts and Trash.

The host:

- Lists every folder in `Folders` and says which holds what through
  `Folder.SpecialUse`; the name alone never does.
- Creates a folder when a message first names it, or returns
  `imap.ErrNoSuchMailbox` (wrapped is fine) from `Append`, `Copy` or `Move`, which
  the server answers with `NO [TRYCREATE]` so the client can CREATE and retry.
- Applies Unicode normalisation (NFC) to UTF-8 names if it wants it; the library
  has no normalisation tables.

### Messages, UIDs and flags

The library:

- Caches the slice `Messages` returns for the selected folder and derives
  sequence numbers, flags, `RFC822.SIZE`, SEARCH results and ENVELOPE fallbacks
  from it. It calls `Fetch` only when content is needed.
- Sets `\Seen` through `Store` on a non-peek body fetch, and answers `NO` when
  the backend refuses. In a folder selected with EXAMINE it never writes.
- Keeps `\Deleted` marks in the session until EXPUNGE or CLOSE calls `Delete`.
- Re-reads the selected folder on NOOP, CHECK and during IDLE, and reports what
  other sources changed: expunges, flag changes and new mail (RFC 3501 §5.2).

The host:

- Assigns UIDs: positive, stable and ascending with arrival in each folder, and
  returns messages oldest first.
- Reports `Size` as the exact octet count of the stored message.
- Returns the stored octets from `Fetch` unchanged; FETCH `BODY[]` serves them
  byte for byte.
- Reports `Quota` in octets; the server converts to the 1024-octet units of
  RFC 2087.
- Makes `Append`, `Copy`, `Move` and `Delete` atomic per message. The server calls
  them one message at a time and cannot roll back earlier ones when a later one
  fails.

### Message structure and SEARCH

The library:

- Builds BODY, BODYSTRUCTURE and ENVELOPE from the stored octets, and bounds
  MIME nesting at depth 64.
- Treats an unknown Content-Transfer-Encoding as `application/octet-stream`
  (RFC 2045 §6.4) everywhere: BODYSTRUCTURE, sections and SEARCH.
- Removes base64 and quoted-printable for BINARY, and answers
  `NO [UNKNOWN-CTE]` when it cannot.
- Decodes encoded words, transfer encodings and ISO-8859-1 before SEARCH
  compares text. It accepts the US-ASCII and UTF-8 charsets.
- Refuses to FETCH a message whose octets, or whose decoded ENVELOPE and MIME
  strings, hold NUL.

The host:

- Stores messages as received. The library does not downgrade 8-bit headers or
  binary bodies for clients that did not ask for them.

## RFC conformance

### The RFC checklist

[CHECKLIST.md](CHECKLIST.md) is a partial, CI-checked list of RFC 3501
requirements and those of every extension the server advertises. Each mapped
row names the wire tests in [`pkg/imap/rfctest`](pkg/imap/rfctest) that prove
it: they drive a public `server.Session` over `net.Pipe`, as a client would.
Rows the library does not meet, or that belong to the host or the client, are
`n/a` with the reason. The checklist is growing; it is not a full conformance
claim. The [core grammar crosswalk](RFC-GRAMMAR.md) inventories all
section-9 production names independently of the narrower wire-test claims.

[RFC-SESSION-EXTENSIONS.md](RFC-SESSION-EXTENSIONS.md) records the grouped
seven-spec negotiation/session audit, source pins, narrow proofs and explicit
host/client/unsupported boundaries. PLAIN grammar and base64 refusals, required
credential lengths, ENABLE pipelining, literal framing and live IDLE updates
have focused wire regressions. Core and other-extension reconciliation remains.

[RFC-MAILBOX-EXTENSIONS.md](RFC-MAILBOX-EXTENSIONS.md) groups the five mailbox
discovery/status specifications with source pins, narrow wire proofs and explicit
host/client/unsupported boundaries. It records the SIZE numeric guard and
LIST-STATUS race, recursive-selection and duplicate-option corrections; wider
core/search/message reconciliation remains.

[RFC-MESSAGE-EXTENSIONS.md](RFC-MESSAGE-EXTENSIONS.md) groups six search/message
specifications with source pins, BINARY text canonicalization, saved-search/UID
pipeline and UTF8 output proofs, and explicit host/client/unsupported boundaries.
Core non-keyword/alternative and complete conformance work remains.

The checklist is a Markdown file of tables with this header:

```markdown
| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-GREET-01 | RFC 3501 §§3.1, 7.1.1 | Normative | Greet with an untagged OK, which starts the not authenticated state; refuse SELECT before authentication. | TestGreeting |
```

Each test names the rows it covers in its doc comment:

```go
// checklist: IMAP-GREET-01
func TestGreeting(t *testing.T) {
```

[`internal/checklist`](internal/checklist/main.go) checks one against the
other in CI. It is the same program as the other `go-*` libraries'.
`go run ./internal/checklist [-file CHECKLIST.md] [package dir ...]` fails when
an ID is used twice, a row names no test and is not `n/a`, a row and a test
disagree about each other, or a `Test` or `Fuzz` function names no row:

```sh
go run ./internal/checklist pkg/imap/rfctest
```

Tests that need the server's internals, such as the autologout timer, live
beside it in `pkg/imap/server`; the checklist names them in the `n/a` reason.

### What it does not do

Some gaps are deliberate, and go-imap will not fill them. Others are planned,
each with an issue. The checklist records every one as an `n/a` row with its
reason.

#### Not supported, by design

| Area | What go-imap does | Why |
|------|-------------------|-----|
| Authentication | PLAIN and LOGIN only: no challenge-response mechanism (CRAM-MD5, DIGEST-MD5, SCRAM), EXTERNAL or ANONYMOUS. | When a TLS config is supplied, PLAIN is only offered inside TLS. A nil TLS config explicitly permits development/legacy cleartext authentication. CRAM-MD5 and DIGEST-MD5 are obsolete and need passwords stored in plaintext-equivalent form. SCRAM needs a second, SCRAM-specific credential store and helps only when TLS is already broken. ANONYMOUS has no use on a server of personal accounts. |
| `\Recent` | Not tracked: RECENT, NEW and OLD approximate it with unseen. | IMAP4rev2 (RFC 9051) removes `\Recent`. |
| Subscriptions | No subscription store: every folder counts as subscribed. | Clients show every folder anyway; a second list of folders would only go stale. |
| UIDs | Without `UIDPlusMailbox`, UIDVALIDITY is 1 and UIDNEXT is the highest UID plus one. UIDNOTSTICKY is never reported. | A backend that has real values implements `UIDPlusMailbox`; UIDs are expected to persist. |
| ENVELOPE | Source routes are always NIL, and obsolete address syntax that `net/mail` rejects gives NIL. | Source routes are obsolete. |

#### Not planned

| Area | What go-imap does |
|------|-------------------|
| Folder permissions | No per-folder read-only state through SELECT. Nothing needs it until mailboxes can be shared. |
| METADATA (RFC 5464) | Not implemented. |
| QUOTA configuration (RFC 2087 SETQUOTA) | Only GETQUOTA/GETQUOTAROOT are implemented; the backend has no setter or distinct unlimited-resource representation. |

#### Planned

| Gap | Issue |
|-----|-------|
| Keywords (`$Forwarded`, `$Junk`, tags) are accepted but not stored, and PERMANENTFLAGS offers no `\*`. | [#158](https://github.com/rest-mail/go-imap/issues/158) |
| `\Deleted` lives in one session until EXPUNGE, so other sessions do not see it. | [#159](https://github.com/rest-mail/go-imap/issues/159) |
| CREATE succeeds without creating anything, and DELETE and RENAME move messages. An empty folder does not exist. | [#160](https://github.com/rest-mail/go-imap/issues/160) |
| COPY and MOVE cannot roll back a partly applied command. | [#161](https://github.com/rest-mail/go-imap/issues/161) |
| CONDSTORE and QRESYNC (RFC 7162). | [#162](https://github.com/rest-mail/go-imap/issues/162) |
| ID (RFC 2971). | [#163](https://github.com/rest-mail/go-imap/issues/163) |
| The IMAP4rev2 capability (RFC 9051). The checklist inventories its requirements. | [rest-mail/rest-mail-server#718](https://github.com/rest-mail/rest-mail-server/issues/718) |

### Badges and coverage

The badges at the top are checked by `TestReadmeChecklistBadge` and
`TestReadmeCoverageBadge` in `internal/checklist`. The checklist badge counts
mapped rows over all rows: the `n/a` rows are in the denominator, so the badge
never claims more than the tests prove. The coverage badge shows the floor CI
enforces, not a measured figure; each CI run prints the measured statement
coverage in its **Require at least 95% statement coverage** step. CI measures
with `-coverpkg=./...`, so the black-box suite counts toward the packages it
exercises. Coverage is in [COVERAGE.md](COVERAGE.md).

Nine fuzz targets run in CI, against the FETCH, SEARCH, MIME and mailbox-name
grammars.

## Changelog

Recent releases; see [CHANGELOG.md](CHANGELOG.md) for the complete history.

### v0.13.2 (2026-10-27)

Command names, keywords and atoms stay ASCII under UTF8=ACCEPT, untrusted SEARCH
values never reach response text, and sequence ranges stay unsigned on 32-bit
hosts. No API changes.

### v0.13.1 (2026-10-26)

Stricter input: QUOTA commands, SASL PLAIN payloads and mailbox SIZE metadata
are validated before anything reaches the backend or the wire, LIST-STATUS
handles a mailbox that disappears mid-listing, and BINARY text has its bare CR
and LF canonicalized before sizes and partials are counted. No API changes.

### v0.13.0 (2026-10-26)

The library is split into `pkg/imap` and `pkg/imap/server`, and `NewServer` is
`server.New` (breaking; the changelog has a migration table). 8-bit text outside
a literal gets `BAD` unless the backend supports UTF8=ACCEPT.

### v0.12.0 (2026-10-24)

LIST-EXTENDED. LIST under `%` returns hierarchy levels that are not folders
themselves.

### v0.11.0 (2026-10-24)

SASL-IR, LITERAL-, NAMESPACE, ESEARCH, LIST-STATUS, STATUS=SIZE, BINARY and
SEARCHRES. Response codes carry text, and argument-free commands follow the
grammar.

### v0.10.0 (2026-10-24)

A stopping server tells every session's client why, with `BYE`.

### v0.9.0 (2026-10-24)

Mailbox names are modified UTF-7 on the wire (breaking). NOOP and CHECK report
other sources' changes.

## Licence

[MIT](LICENSE) © 2026 rest-mail

[RFC 3501]: https://www.rfc-editor.org/rfc/rfc3501
[RFC 4315]: https://www.rfc-editor.org/rfc/rfc4315
[RFC 6851]: https://www.rfc-editor.org/rfc/rfc6851
[RFC 6855]: https://www.rfc-editor.org/rfc/rfc6855
