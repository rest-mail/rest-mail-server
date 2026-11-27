# go-pop3

[![CI](https://github.com/rest-mail/go-pop3/actions/workflows/ci.yml/badge.svg)](https://github.com/rest-mail/go-pop3/actions/workflows/ci.yml)
[![Coverage gate: >=95%](https://img.shields.io/badge/coverage%20gate-%3E%3D95%25-brightgreen)](COVERAGE.md)
[![RFC checklist: 155/204 mapped](https://img.shields.io/badge/RFC%20checklist-155%2F204%20mapped-blue)](CHECKLIST.md)
[![Go Reference](https://pkg.go.dev/badge/github.com/rest-mail/go-pop3.svg)](https://pkg.go.dev/github.com/rest-mail/go-pop3/pkg/pop3/server)
[![Go Report Card](https://goreportcard.com/badge/github.com/rest-mail/go-pop3)](https://goreportcard.com/report/github.com/rest-mail/go-pop3)

A POP3 ([RFC 1939]) server engine for Go, with TLS, SASL PLAIN and UTF-8 mail.
Standard library only: no external dependencies.

You supply a `Backend` that authenticates users and returns a `Mailbox`: one
user's maildrop, expressed as plain `Message` values and stored RFC 5322 bytes.
The server handles greeting, capabilities, authentication, listing, retrieval
and deletion. Mail can live in a database, a maildir or a remote API.

## Contents

- [Install](#install)
- [Packages](#packages)
- [Quick start](#quick-start): [run a server](#run-a-server), [optional interfaces](#optional-interfaces), [one connection](#one-connection)
- [Features](#features)
- [What the library does and what the host must do](#what-the-library-does-and-what-the-host-must-do)
- [RFC conformance](#rfc-conformance): [the checklist](#the-rfc-checklist), [what it does not do](#what-it-does-not-do), [badges and coverage](#badges-and-coverage), [independent client verification](#independent-client-verification)
- [Changelog](#changelog)
- [Licence](#licence)

## Install

```sh
go get github.com/rest-mail/go-pop3@latest
```

The module declares go 1.25.

## Packages

| Import path | What it holds |
|-------------|---------------|
| `github.com/rest-mail/go-pop3/pkg/pop3` | Backend-facing contracts: `Backend`, `Mailbox`, `Message`, `Limiter`, `NopLimiter`, `OctetCount`, the optional `SASLBackend`, `UTF8Backend`, `LoginDelayBackend`, `LoginDelayMailbox` and `ImplementationBackend` interfaces, and the `Err*` sentinels. |
| `github.com/rest-mail/go-pop3/pkg/pop3/server` | The server: `Server`, `New`, `Ports`, `Session` and `NewSession`. |
| `pkg/pop3/internal/wire` | Command parsing, log redaction, UID validation, CRLF canonicalization and RETR/TOP framing. Internal: not importable by consumers. |
| `pkg/pop3/rfctest` | The black-box RFC wire suite, driving only the public API. Tests only. |
| `pkg/pop3/internal/referencetest` | Comparison with CPython's independent `poplib` client. Tests only. |

Upgrading from v0.3 or earlier: the root package is gone, and `NewServer` is
`server.New`. [CHANGELOG.md](CHANGELOG.md#040---2026-10-26) has the migration table.

## Quick start

### Run a server

Implement `Backend` and `Mailbox`, then provide a TLS certificate and ports.
This compiles as a complete program. The fixed maildrop is a demonstration;
replace its methods with your credential store and durable message operations.

```go
package main

import (
	"crypto/tls"
	"errors"
	"log"

	"github.com/rest-mail/go-pop3/pkg/pop3"
	"github.com/rest-mail/go-pop3/pkg/pop3/server"
)

type store struct{}

func (*store) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if user != "me@example.com" || pass != "change-me" {
		return nil, errors.New("invalid credentials")
	}
	return &maildrop{}, nil
}

type maildrop struct{}

var raw = []byte("From: alice@example.com\r\nSubject: hello\r\n\r\nHello!\r\n")

func (*maildrop) Messages() ([]pop3.Message, error) {
	// Oldest first, with stable UIDs and the size of the bytes Retrieve serves.
	return []pop3.Message{{UID: "1001", Size: pop3.OctetCount(raw)}}, nil
}
func (*maildrop) Retrieve(string) ([]byte, error) { return raw, nil }
func (*maildrop) MarkSeen(string) error          { return nil } // persist read state, if any
func (*maildrop) Delete(string) error            { return nil } // replace with durable deletion

func main() {
	cert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
	if err != nil {
		log.Fatal(err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
	// A nil Limiter is pop3.NopLimiter: no per-IP limits.
	srv := server.New(&store{}, tlsConfig, nil)
	if err := srv.ListenAndServe(server.Ports{POP3: 110, POP3TLS: 995}); err != nil {
		log.Fatal(err)
	}
	select {} // serve until srv.Shutdown(ctx) or srv.Close()
}
```

### Optional interfaces

The server checks which optional interfaces the concrete backend and mailbox
implement. A plain `Backend` keeps USER/PASS and the base capabilities.

| Interface | Methods | What it enables |
|-----------|---------|-----------------|
| `pop3.SASLBackend` | `AuthenticatePlain(authzid, authcid, pass string, utf8 bool) (pop3.Mailbox, error)` | AUTH PLAIN, with an initial response or an empty `+ ` challenge; CAPA lists `SASL PLAIN` while it is accepted. |
| `pop3.UTF8Backend` | `AuthenticateUTF8(user, pass string, utf8 bool) (pop3.Mailbox, error)` | CAPA `UTF8` and the UTF8 command before login. Replaces `Authenticate` for USER/PASS; the mode tells the backend which message form to serve. |
| `pop3.LoginDelayBackend` | `LoginDelay() (seconds int, perUser bool)` | CAPA `LOGIN-DELAY`; `USER` is appended before login when delay varies by account. A negative delay omits the tag. |
| `pop3.LoginDelayMailbox` | `LoginDelay() int` | The authenticated account's delay after login, without `USER`. Requires a `LoginDelayBackend`; otherwise the tag is absent. |
| `pop3.ImplementationBackend` | `Implementation() (text string, transactionOnly bool)` | Informational CAPA `IMPLEMENTATION`, optionally hidden until login. Empty, non-ASCII, malformed or overlong text omits the tag. |

Each optional backend interface embeds `Backend`; `LoginDelayMailbox` embeds
`Mailbox`. Without a mailbox-specific delay, CAPA uses the backend's value.
Announcing LOGIN-DELAY does not enforce it: the backend tracks login times and
returns `pop3.ErrLoginDelay` when needed.

### One connection

For a connection accepted by your own listener or proxy:

```go
sess := server.NewSession(conn, backend, tlsConfig, limiter)
sess.Handle() // returns when the connection ends
```

[`Example`](pkg/pop3/server/example_test.go) runs a full conversation over
`net.Pipe` with an in-memory backend.

## Features

| Feature | RFC | Behavior |
|---------|-----|----------|
| USER/PASS, STAT, LIST, RETR, DELE, NOOP, RSET, QUIT | [RFC 1939] | Authentication followed by one session snapshot; deletion commits only on QUIT. |
| TOP and UIDL | RFC 1939 §7 | Advertised by CAPA; TOP leaves read state unchanged, UIDL validates IDs and rejects duplicate IDs in a full listing. |
| CAPA, RESP-CODES, PIPELINING | [RFC 2449] | Available in both states; commands are processed in wire order, including queued authentication attempts. |
| AUTH-RESP-CODE | [RFC 3206] | Credential failures carry `[AUTH]`; system failures have separate codes. |
| STLS and implicit TLS | [RFC 2595], [RFC 8314] | STLS before login, or TLS before the greeting on the secure port. With a TLS config, credentials require encryption. |
| AUTH PLAIN | RFC 5034, RFC 4616 | Optional `SASLBackend`; the same TLS and error-classification rules as USER/PASS. |
| UTF8 | [RFC 6856] | Optional `UTF8Backend`; negotiated before login and reset by STLS. The host serves the selected message form. |
| LOGIN-DELAY and IMPLEMENTATION | RFC 2449 §§6.5, 6.9 | Optional interfaces described above; state-dependent capability arguments. |
| Per-IP limits | Host interface | `Limiter` supplies connection caps and authentication-failure bans; nil means no limits. |
| Lifecycle and bounds | RFC 1939 §3 | Graceful drain or immediate close; ten-minute idle deadlines, bounded command lines, isolated session panics and credential-safe debug logs. |

`server.New(backend, tlsConfig, limiter)` builds the server.
`ListenAndServe(server.Ports{POP3, POP3TLS})` returns after opening listeners;
a zero port is skipped. `Shutdown(ctx)` stops accepting and waits for sessions
to finish, returning `ctx.Err()` if needed. `Close()` force-closes active
connections immediately, following the `net/http.Server` lifecycle split.

## What the library does and what the host must do

### Authentication and TLS

The library requires a fresh USER immediately before each PASS, opens the
maildrop before entering TRANSACTION, and permits retry after failure. With a
TLS config it refuses cleartext USER and AUTH PLAIN until STLS. With no config,
plaintext authentication is available for local development. The implicit-TLS
port requires a config; `ListenAndServe` also refuses a config without a
certificate source before opening listeners. STLS discards buffered plaintext,
clears authentication preparation and UTF8 mode, and is refused after login.
TLS version and cipher appear in STLS and authentication logs.

The host verifies credentials, supplies the TLS certificate and policy, and
implements `Limiter` if limits are wanted. `Accept`/`Release` cap connections;
`RecordAuthFail`/`IsBanned`/`ResetAuth` control bans. The server checks bans at
accept time and after credential failures. A bare `Session` serves an already
accepted connection; the host owns connection admission around it.

For SASL PLAIN the host receives `authzid`, `authcid`, password and UTF8 mode.
Reject a non-empty authorization identity different from the authentication
identity unless acting as another user is allowed. Credential preparation,
normalization and password storage belong to the host. The engine validates
PLAIN framing and UTF-8, and accepts fields of at least 255 octets each.

### Login failures and response codes

| Backend result | Reply and limiter behavior |
|----------------|----------------------------|
| Unclassified `Authenticate` error | `[AUTH]`; records a credential failure, checks for a ban and closes if banned. |
| `pop3.ErrInUse` from `Authenticate` or `Messages` | `[IN-USE]`; verified credentials but a locked maildrop. Clears failure history without recording a failure or checking for a ban. |
| `pop3.ErrLoginDelay` from `Authenticate` | `[LOGIN-DELAY]`; verified credentials used too soon. Same limiter treatment as `ErrInUse`. USER never reveals the code. |
| `pop3.ErrSystemTemporary` or `pop3.ErrSystemPermanent` from `Authenticate` | `[SYS/TEMP]` or `[SYS/PERM]`; preserves credential-failure history without recording, clearing or checking for a ban, since credentials may be unverified. |
| `Messages` failure | `[SYS/PERM]` for `ErrSystemPermanent`, otherwise `[SYS/TEMP]`, apart from `ErrInUse`. Successful authentication already cleared failure history. |
| A nil mailbox with no error | `[SYS/PERM]`; rejects login and remains in AUTHORIZATION. |

Sentinels may be wrapped with `%w`: the server uses `errors.Is`. It discards
partial results, keeps private errors out of replies, and requires a fresh
USER/PASS after refusal. Use `ErrInUse` and `ErrLoginDelay` only after verifying
credentials. These sentinels classify login-opening errors; transaction
commands retain their own error handling.

### Messages, UIDs and deletion

`Mailbox.Messages` returns the maildrop oldest first, once after each successful
authentication. The server snapshots it; slice positions plus one are the POP3
message numbers throughout that session. It translates them back into stable
UIDs for `Retrieve`, `MarkSeen` and `Delete`. Supply unique, persistent IDs of
1–70 printable ASCII bytes (`0x21`–`0x7e`), with no spaces or controls. UIDL
checks grammar and duplicate IDs in a full listing; persistence and UID reuse
across sessions remain host duties.

RETR and TOP canonicalize stored LF line endings to CRLF, preserve bare CR,
add the final CRLF when needed, and dot-stuff on the wire. Message contents
otherwise pass through unchanged. `Message.Size` must count the same canonical
form, excluding stuffed dots and the terminator: use `pop3.OctetCount` on the
bytes `Retrieve` returns. STAT and LIST trust that supplied size. RETR calls
`MarkSeen` for a previously unseen message; TOP never does. A backend without
read state may return nil from `MarkSeen`.

DELE marks messages within the session, RSET clears the marks, and QUIT calls
`Delete` for every marked message. All deletions are attempted; any failure
makes QUIT answer `-ERR`. A disconnect, timeout or panic does not enter UPDATE.
The host owns durable deletion and exclusive maildrop access. There is no
maildrop cleanup callback yet; arrange resource and lock release in the host.

For a `UTF8Backend`, the login's mode selects the message representation.
Outside UTF8 mode, downgrade raw UTF-8 headers (RFC 6857) or refuse retrieval
with `pop3.ErrUTF8Required`; RETR and TOP then answer `[UTF8]` without marking
seen. In UTF8 mode serve the internationalized form. Compute sizes from the
selected representation. Charset conversion, header downgrading, retention
policy and storage transactions belong to the host.

## RFC conformance

### The RFC checklist

[CHECKLIST.md](CHECKLIST.md) records a line-by-line inventory of RFC 1939,
2449, 2595, 3206, 4616, 5034, 6856 and 8314. Each row maps to a public wire proof
in [`pkg/pop3/rfctest`](pkg/pop3/rfctest), or says `n/a` with a reason: a host or
client duty, a deliberate omission or an unmet requirement. A mapped row proves
its stated behavior, not an entire section or certification.

The table format is:

```markdown
| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-AUTH-01 | RFC 1939 §§3–4 | Normative | Greet positively; refuse transaction commands before authentication. | TestAuthorizationUserPass |
```

Each proof names its rows in a doc comment:

```go
// checklist: POP3-AUTH-01 POP3-AUTH-02
func TestAuthorizationUserPass(t *testing.T) {
```

[`internal/checklist`](internal/checklist/main.go) checks both directions in CI:

```sh
go run ./internal/checklist pkg/pop3/rfctest
```

`go run ./internal/checklist [-file CHECKLIST.md] [package dir ...]` fails on
duplicate IDs, missing or unannotated tests, inconsistent mappings and rows
without tests or an `n/a` reason. Server-internal, helper, fuzz and reference
client tests sit separately from this scoped wire suite.

### What it does not do

#### Not supported, by design

| Area | Behavior and reason |
|------|---------------------|
| APOP and additional SASL mechanisms | USER/PASS and optional PLAIN only. APOP's MD5 shared-secret scheme and challenge-response mechanisms require different credential storage; this engine uses TLS to protect the existing credential seam. No APOP timestamp, GSSAPI, EXTERNAL or ANONYMOUS is offered. |
| Client-certificate login | Certificates may be verified by `tls.Config`, but do not authenticate a maildrop. Mapping a certificate identity into an account needs a different authentication contract; USER/PASS or PLAIN still establishes the account. |
| SASLprep and `UTF8 USER` | No built-in Unicode preparation tables, and CAPA never advertises `UTF8 USER`. The backend owns credential preparation and authorization-identity policy, including the RFC 5034 recommendation; blindly normalizing inside the protocol layer could disagree with its credential store. |
| Message conversion and storage policy | No MIME conversion, UTF-8 header downgrading, retention enforcement, UID history or storage locks. The host owns the stored representation and policy; the engine reports negotiated mode and calls its backend. A cleanup callback is planned below. |
| POP3 client | Server engine only; no production client or capability cache. The RFC and independent-client tests are verification tools. |
| Obsolete TLS cipher requirements | The library uses the supplied `tls.Config` and Go's `crypto/tls`; it does not add RFC 2595's obsolete DSS/3DES suite. Current policy follows the host's configuration and RFC 8314. |

#### Not planned

| Area | Current behavior |
|------|------------------|
| LANG (RFC 6856 §3) | No language negotiation or translated response catalogue; replies use English text and protocol response codes. |

#### Planned

| Gap | Issue |
|-----|-------|
| Reliably release maildrop locks and resources on every session exit and failed opening. | [#84](https://github.com/rest-mail/go-pop3/issues/84) |
| Announce host retention policy with EXPIRE. | [#85](https://github.com/rest-mail/go-pop3/issues/85) |
| Define and enforce UTF-8 credential validation while preserving legacy credential bytes outside that contract. | [#86](https://github.com/rest-mail/go-pop3/issues/86) |
| Give authentication backends connection security information for per-account TLS policy. | [#87](https://github.com/rest-mail/go-pop3/issues/87) |

These decisions do not turn unmet checklist rows into conformance claims.

### Badges and coverage

`TestReadmeChecklistBadge` and `TestReadmeCoverageBadge` in `internal/checklist`
check the exact badges against the real mappings and CI gate. The checklist
badge counts 155 mapped rows out of 204; its 49 `n/a` rows stay in the denominator.
The coverage badge shows the enforced 95% floor, with measured statement
coverage printed in each CI run. CI measures `-coverpkg=./...`, so external wire
proofs count toward library coverage. [COVERAGE.md](COVERAGE.md) lists the
remaining uncovered statements. Seven fuzz targets exercise command parsing,
redaction, UID grammar, CRLF, octet counts and RETR/TOP framing.

### Independent client verification

CI requires `TestPythonPoplibReference` against Python 3.13's standard-library
[`poplib`](https://docs.python.org/3.13/library/poplib.html) client:

```sh
POP3_REFERENCE_PYTHON=python3 go test -count=1 -run '^TestPythonPoplibReference$' ./pkg/pop3/internal/referencetest
```

An ephemeral loopback listener drives the public Session with a memory backend.
The independent client parses CAPA, authentication errors, STAT, single and
multiline LIST/UIDL, RETR, TOP, DELE, RSET, NOOP and QUIT. Fixed expected bytes
check decoded messages, octet counts and `OctetCount`, including leading dots,
empty messages, LF normalization, an unterminated final line and opaque bytes.
Backend checks distinguish successful QUIT from disconnect without UPDATE.

This is a limited interoperability check, not another server or a full RFC
oracle. `poplib` accepts some noncanonical syntax; Go wire tests assert strict
CRLF and status framing. TLS/STLS, pipelining, arbitrary malformed inputs and
production backend durability are outside this reference test. Python is
optional for ordinary Go tests: they skip it unless `POP3_REFERENCE_PYTHON` is
set. CI sets it and fails if Python or the comparison fails. There is no new
runtime dependency.

## Changelog

Recent releases; see [CHANGELOG.md](CHANGELOG.md) for the complete history.

### v0.4.0 (2026-10-26)

The root package moves to `pkg/pop3` and `pkg/pop3/server`; `NewServer` becomes
`server.New` (breaking). Internal wire helpers and the public RFC suite move
with it. The changelog has a migration table.

### v0.3.0 (2026-10-23)

SASL PLAIN, UTF8, LOGIN-DELAY and IMPLEMENTATION through optional backend
interfaces; classified authentication/system failures, strict TLS configuration
and independent-client, RFC, coverage and fuzz verification.

### v0.2.0 (2026-07-25)

`Shutdown(ctx) error` and immediate `Close()` (breaking); UIDL validation,
pre-authentication command checks, QUIT failure reporting and octet-count API.

### v0.1.2 (2026-07-25)

CRLF normalization, isolated session panics, failed-maildrop guards and exact
multiline framing.

### v0.1.1 (2026-07-23)

Initial tagged release lineage, including v0.1.0.

## Licence

[MIT](LICENSE) © 2026 rest-mail

[RFC 1939]: https://www.rfc-editor.org/rfc/rfc1939
[RFC 2449]: https://www.rfc-editor.org/rfc/rfc2449
[RFC 2595]: https://www.rfc-editor.org/rfc/rfc2595
[RFC 3206]: https://www.rfc-editor.org/rfc/rfc3206
[RFC 6856]: https://www.rfc-editor.org/rfc/rfc6856
[RFC 8314]: https://www.rfc-editor.org/rfc/rfc8314
