# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Note: pre-1.0, breaking changes may ship in a minor release.

## [Unreleased]

## [0.4.0] - 2026-10-26

### Changed (breaking)

- The root package is gone. The module path remains
  `github.com/rest-mail/go-pop3`, while the backend-facing API moves to
  `github.com/rest-mail/go-pop3/pkg/pop3` and the server moves to
  `github.com/rest-mail/go-pop3/pkg/pop3/server`.
- `NewServer` becomes `server.New`; `NewSession` keeps its name in package
  `server`. The protocol and backend method contracts are unchanged.
- Command parsing, redaction, UID checks and message framing live in
  `pkg/pop3/internal/wire`. The black-box RFC suite is now
  `pkg/pop3/rfctest`, and the independent Python client comparison is in
  `pkg/pop3/internal/referencetest`. These test and helper packages are not
  consumer APIs.

Migration: replace imports and qualify server names separately from backend types.

| Before (v0.3) | After (v0.4) |
|---------------|--------------|
| `import "github.com/rest-mail/go-pop3"` | `"github.com/rest-mail/go-pop3/pkg/pop3"` and/or `"github.com/rest-mail/go-pop3/pkg/pop3/server"` |
| `pop3.NewServer(backend, tlsConfig, limiter)` | `server.New(backend, tlsConfig, limiter)` |
| `pop3.Server`, `pop3.Ports`, `pop3.Session`, `pop3.NewSession` | `server.Server`, `server.Ports`, `server.Session`, `server.NewSession` |
| `pop3.Backend`, `pop3.Mailbox`, `pop3.Message`, `pop3.OctetCount`, `pop3.Limiter`, `pop3.NopLimiter` | Same names, imported from `.../pkg/pop3` |
| `pop3.LoginDelayBackend`, `pop3.LoginDelayMailbox`, `pop3.ImplementationBackend`, `pop3.UTF8Backend`, `pop3.SASLBackend` | Same names, imported from `.../pkg/pop3` |
| `pop3.ErrInUse`, `pop3.ErrLoginDelay`, `pop3.ErrSystemTemporary`, `pop3.ErrSystemPermanent`, `pop3.ErrUTF8Required` | Same sentinels, imported from `.../pkg/pop3` |

### Documentation

- Reorganize the README around packages, a compiled quick start, capabilities,
  library and host responsibilities, and deliberate, unplanned and planned gaps.
- Backfill the v0.3.0 release notes that were left under Unreleased, preserving
  their text unchanged.

## [0.3.0] - 2026-10-23

### Changed

- The RFC checklist is no longer labelled partial: RFC 3206, RFC 4616 and
  RFC 8314 now have line-by-line audit records too, so every implemented RFC
  has one. The README badge reads "RFC checklist: 155/204 mapped".

- Empty POP3 command lines now receive `-ERR` and invalidate a pending USER,
  as RFC 1939 §3 requires for syntactically invalid commands; previously they
  were silently ignored.

- PASS failures after good credentials now carry RFC 3206 §4 system codes: a
  maildrop that fails to load replies `-ERR [SYS/TEMP] Failed to load mailbox`
  and a backend that returns no Mailbox replies
  `-ERR [SYS/PERM] Failed to load mailbox` (both were plain `-ERR`). Checked by
  POP3-RESP-CODES-04.

### Added

- `ImplementationBackend` announces the RFC 2449 §6.9 `IMPLEMENTATION`
  capability in CAPA, in both states or only after login. Text outside the
  §9 param grammar or the 512-octet line limit omits the tag. Checked by
  POP3-CAPA-10, and POP3-CAPA-11 now combines it with LOGIN-DELAY, UTF8 and
  STLS in the CAPA grammar matrix.

- `ErrUTF8Required` lets a Mailbox refuse a message that has no form outside
  UTF-8 mode. RETR and TOP then reply `-ERR [UTF8] Message requires UTF-8 mode`
  (RFC 6856 §5) and mark nothing seen, when the Backend is a UTF8Backend and
  the client has not issued UTF8. Checked by POP3-UTF8-10.

- `SASLBackend`, an optional Backend extension that enables the RFC 5034 `AUTH`
  command with SASL `PLAIN` (RFC 4616), by initial response or after an empty
  `+ ` challenge. CAPA lists `SASL PLAIN` only while it is accepted; with a TLS
  config, AUTH is refused before STLS like USER. Credential failures reply
  `-ERR [AUTH]` and count toward the Limiter ban like PASS. Backends without it
  are unchanged. Seven RFC 5034 checklist rows (POP3-SASL-01..07), one
  response-line policy row and
  POP3-CAPA-08 are now mapped to wire tests (110/116 mapped).
- Nine RFC 1939 core command/state checklist rows: six mapped requirements
  covering keyword case permutations, credential bytes, empty commands, fresh
  deletion state and RETR failure recovery, plus three explicit backend lock
  and resource lifecycle boundaries. The inventory remains partial.

- Nine RFC 2449/2595 checklist rows: TOP/UIDL capability contracts, combined
  CAPA configuration grammar, optional extended codes, and STLS with a verified
  client certificate are backed by five new public-Session wire tests. Four
  explicit n/a reasons cover unsupported SASL/IMPLEMENTATION, unavailable host
  retention policy and client capability-cache duties. The partial README badge
  includes these gaps in its denominator (101/108 mapped).

- README coverage-gate and partial RFC checklist badges, checked by tests
  against the CI coverage minimum and the checklist's actual test mappings.

- POP3-LENGTH-02 (`TestResponseFirstLineLimit`) proves RFC 2449 §4: the first
  response line, including the greeting, stays within 512 octets for
  maximum-length commands in both states.
- `LoginDelayBackend` and `LoginDelayMailbox` announce the RFC 2449 §6.5
  `LOGIN-DELAY` CAPA tag: the Backend's minimum in AUTHORIZATION (followed by
  `USER` when the delay varies per user) and the user's own value in
  TRANSACTION. Backends without them are unchanged. Checked by POP3-CAPA-05.

- RFC 6856 `UTF8` support through the optional `UTF8Backend` interface: CAPA
  advertises `UTF8`, the `UTF8` command is accepted in AUTHORIZATION state
  (refused after login, reset by STLS), and `AuthenticateUTF8` receives the
  client's mode so the backend can serve raw UTF-8 headers or downgrade them
  (RFC 6857). A plain `Backend` keeps the old behavior: no `UTF8` in CAPA and
  `-ERR Unknown command` for `UTF8`.

- `ErrSystemTemporary` and `ErrSystemPermanent` let `Authenticate` and `Messages`
  classify login failures as `SYS/TEMP` or `SYS/PERM` (RFC 3206 §4), including
  wrapped errors. Authentication system failures preserve credential-failure
  history without recording a failure or checking for a ban. Private details
  and partial results are discarded. Checked POP3-RESP-CODES-07 covers both
  reporting points, classes, wrapping, repeated failure, retry and QUIT.

- Backends may return `ErrLoginDelay` (including wrapped errors) from
  `Authenticate` after verifying credentials to refuse a login that comes too
  soon after the last one. PASS replies `-ERR [LOGIN-DELAY]` (RFC 2449 §8.1.1),
  consumes the pending USER, stays in AUTHORIZATION and resets
  credential-failure history without recording a failure or checking for a ban.
  USER never carries the code. Checked by POP3-RESP-CODES-06.

- Backends may return `ErrInUse` (including wrapped errors) from `Authenticate`
  or `Messages` after verifying credentials to report an already-open maildrop.
  PASS replies `-ERR [IN-USE]` (RFC 2449 §8.1.2), consumes the pending USER,
  stays in AUTHORIZATION and resets credential-failure history without recording
  a failure or checking for a ban. Checked POP3-RESP-CODES-05 covers repeated
  contention, partial results, successful retry and QUIT. Locking remains the
  backend's responsibility.

- Checked RFC 1939 §3 autologout proof for partial commands after STLS
  (POP3-IDLE-08): a genuine timeout below TLS while an encrypted USER, PASS or
  QUIT without its LF is buffered closes with only close_notify, never
  dispatches the line, authenticates no further and never enters UPDATE.
  Tests only; no runtime change.

- Checked STARTTLS command-injection proof (POP3-STLS-07): plaintext commands
  pipelined behind STLS in one write are dropped at the handshake, so an
  injected USER/PASS never authenticates and no injected command is answered
  under TLS. Tests only; no runtime change.

- CAPA advertises `AUTH-RESP-CODE` (RFC 3206 §6), and the PASS failure that
  earns a limiter ban now replies `-ERR [AUTH] Too many authentication failures`,
  so every credential-caused failure carries the AUTH response code. Checked
  by POP3-RESP-CODES-03.

- Checked RFC 1939 §3 autologout proof after STLS: the encrypted idle
  interval is renewed to at least ten minutes, and a genuine timeout below TLS
  after the handshake, an encrypted USER or two DELE marks closes with only
  close_notify, with no extra authentication and no UPDATE. Tests only; no
  runtime change.

- Checked RFC 8314 §3.4 implicit-TLS closure proofs for TLS 1.2 and 1.3,
  before and after login: QUIT and client close alerts receive a server
  close_notify before TCP termination. The test distinguishes an alert from
  bare TCP EOF. Tests only; no runtime change.

- Deterministic server lifecycle proofs for recovery after an `Accept` error and
  a connection returned by `Accept` after shutdown: no lost limiter slot, no
  greeting or session for the late connection, and bounded server drain. Removes
  the remaining `server.go` coverage exceptions; no runtime change.

- Checked RFC 8314 §3.1 implicit-TLS ("pop3s") proofs through the public
  `ListenAndServe`: TLS before the greeting, nothing sent in the clear to a
  plaintext client, AUTHORIZATION after the greeting, no STLS offered or
  accepted, and USER/PASS without STLS. Also tests `ListenAndServe` on a port
  already in use. Tests only; no runtime change.

- Checked RFC 1939 pre-login timeout proofs for buffered partial USER/PASS
  lines, with and without a trailing CR: no reply, authentication or mailbox
  access before the session closes. Plaintext fixture only; no runtime change.

- A CI-required comparison with Python 3.13's independent `poplib` client for
  selected plaintext POP3 commands, decoded message bytes/octet counts and
  QUIT versus disconnect deletion behavior. Python remains test-only and is
  optional for ordinary local Go test runs; see README for scope and usage.

### Fixed

- **A `POP3TLS` port with no TLS config listened in plaintext.** RFC 8314 §3.1
  says the "pop3s" port begins a TLS handshake immediately, but with a nil
  `tls.Config` `ListenAndServe` opened it as a plain TCP listener. It now
  returns an error before opening any listener. A host that relied on this,
  such as a gateway started without certificate files, now fails to start
  instead of serving port 995 in the clear. Checked by POP3-ITLS-05.

- **A TLS config with no certificate announced STLS that always failed.** RFC
  8314 §4 says not to advertise STARTTLS when it is likely to fail, for example
  with no server certificate. `ListenAndServe` now refuses a `tls.Config` with
  no `Certificates`, `GetCertificate` or `GetConfigForClient`. Checked by
  POP3-ITLS-06.

- **TLS logs did not name the cipher.** RFC 8314 §4 says servers SHOULD log
  TLS cipher information with connection and authentication logs. The STLS,
  login and failed-login logs now carry `tls_version` and `tls_cipher`.
  Checked by POP3-ITLS-11.

- **AUTH PLAIN rejected the largest valid response.** RFC 5034 §4 requires a
  server to handle the largest response its mechanisms generate. PLAIN with
  255-octet authzid, authcid and password (RFC 4616 §2) is 1024 base64
  characters, but the line after the `+ ` challenge was capped at 1024 octets
  including CRLF, so it was refused and the session closed. That line is now
  capped at 1026 octets. Checked by POP3-SASL-08.

- **AUTH PLAIN accepted a bare CR inside a base64 response.** Go's
  `encoding/base64` skips CR and LF, so `AHVz\rZXI...` decoded and could log
  in. RFC 5034 §4 says to reject any character outside the base64 alphabet;
  such a response now gets `-ERR Invalid base64 response`. Checked by
  POP3-SASL-09.

- **`TOP` of a message with an empty header section.** A message that begins with
  an empty line has no header fields (RFC 5322 §2.1), but `TOP` treated its whole
  body as header lines: `TOP n 0` sent every body line plus a stray blank line.
  `TOP` now ends the header section at the first empty line, so it sends that
  line and then at most the requested number of body lines (RFC 1939 §7).
- **Command keywords fold ASCII case only.** RFC 1939 §3 keywords are printable
  ASCII, but the parser upper-cased them with Unicode case mapping: `paſs`
  (U+017F) logged in as `PASS`, `lıst` (U+0131) ran `LIST`, and invalid UTF-8 in a
  keyword became U+FFFD. Only `a`-`z` are upper-cased now, so such a keyword is an
  unknown command.
- **Sizes of a message stored without a final end-of-line.** `RETR` ends the last
  line with CRLF, as every line of a multi-line response must, but `OctetCount`
  did not count that CRLF. `STAT`, `LIST` and the `RETR` "+OK n octets" reply
  were 2 octets short of the message a client decoded. `OctetCount` and `RETR`
  now count it (RFC 1939 §11). Messages that already end in an end-of-line, and
  empty messages, keep their size. A backend that stores `Message.Size` it
  computed earlier with `OctetCount` should recompute it for such messages
  (part of rest-mail-server#374).
- **Malformed credential lines no longer reach the debug log.** Only a
  well-formed `PASS`, `APOP` or `AUTH` line had its argument redacted, so a
  credential sent with the wrong syntax was logged in full: ` PASS secret`,
  `PASS<TAB>secret`, `paſs secret`, `PАSS secret` (Cyrillic А), a password typed
  on a line by itself, or a SASL response sent after an `AUTH` the server does
  not offer. A line whose keyword is not a known command is now logged only as
  `<unknown command redacted>`; known non-credential commands are still logged as
  sent. Which commands the server accepts is unchanged (part of
  rest-mail-server#374).

### Added

- **Fuzz tests** for the parsers that read untrusted input: the command line and
  its log redaction, the UIDL unique-id, `canonicalCRLF`, `OctetCount`, and the
  `RETR`/`TOP` framing. Each checks an RFC 1939 property against an independent
  model, and CI fuzzes every target for 5 seconds (part of rest-mail-server#374).
- **A coverage gate.** CI counts statement coverage across every package and
  fails below 95% (97.4% now); `COVERAGE.md` lists what is left uncovered and
  why. New tests cover a message the store cannot retrieve (`RETR` and `TOP`
  answer `-ERR`), a failed `PASS` that bans the client, a connection the limiter
  refuses at accept time, an empty command line, and `NopLimiter` (part of
  rest-mail-server#374).

## [0.2.0] - 2026-07-25

### Breaking

- **`Server.Shutdown` now takes a `context.Context` and returns an `error`.** Its
  signature changed from `Shutdown()` to `Shutdown(ctx context.Context) error`: it
  closes the listeners and then blocks until every in-flight session finishes, or
  returns `ctx.Err()` if the context is done first — a graceful drain that mirrors
  `net/http.Server.Shutdown`. Callers must now pass a context and handle the
  returned error. For the previous immediate stop, use the new **`Server.Close()`**,
  which force-closes live connections without waiting.

### Added

- **`OctetCount(raw []byte) int`** computes the RFC 1939 §11 octet count of a
  stored message — the number of octets `RETR` transmits, measured on the
  canonical CRLF wire form (a lone LF counts as the two octets `CR LF`), with the
  §3 leading-dot byte-stuffing and the `CRLF.CRLF` terminator excluded as
  transport framing. Backends size messages with it so `STAT`/`LIST` and `RETR`
  agree to the octet.

### Changed

- **`Message.Size` is now defined as the exact `RETR` octet count.** Its
  documentation previously said the size "need not equal the exact length
  `Retrieve` returns," which licensed `STAT`/`LIST` to disagree with what `RETR`
  delivers — a client that pre-allocates or verifies against the listing size
  would then mismatch. RFC 1939 §5 requires the scan listing to carry the "exact
  size of the message in octets," and §11 fixes that count to the CRLF-normalized
  form. `Size` must now be that count (compute it with the new `OctetCount`); the
  engine already advertises and transmits exactly this value from `RETR`, so the
  two now agree by construction. No wire behavior changed; the contract and its
  documentation were tightened.
- **`Server.Shutdown` now takes a `context.Context` and actually drains
  in-flight sessions.** It previously waited only on the accept-loop goroutines —
  which return the moment their listener closes — so it returned while client
  sessions were still being served, contradicting its "drains them gracefully"
  documentation. Each accepted connection is now tracked in the server's
  `sync.WaitGroup`, and `Shutdown(ctx)` closes the listeners and then blocks
  until every session finishes, or until `ctx` is done (returning `ctx.Err()`),
  mirroring `net/http.Server.Shutdown`. A new `Server.Close()` provides the
  immediate hard stop: it force-closes live connections without waiting.

### Fixed

- **`STLS` is now refused after authentication.** `STLS` (STARTTLS for POP3,
  RFC 2595) belongs to the AUTHORIZATION state, but the handler gated it only on
  the current TLS status, so a connection that reached TRANSACTION while still
  cleartext could begin a TLS handshake after `PASS`. It is now answered `-ERR`
  once the session is authenticated, before any negotiation begins.
- **The failed-authentication ban is now enforced at connection accept.** The
  ban was consulted only after a failed `PASS`, so a banned IP could still open a
  connection and spend one fresh authentication attempt on every reconnect. The
  accept loop now checks the ban list up front and drops a banned client before a
  session is created.
- **`UIDL` now validates each unique-id and refuses a malformed or duplicate
  one.** A backend-supplied unique-id was written into the `UIDL` response without
  being checked, so an id containing a space, `CR`, or `LF`, or one outside the
  RFC 1939 §7 grammar (1–70 characters in the range `0x21`–`0x7E`), could split the
  `n uid` pair a client parses or inject protocol lines and desynchronize the
  session. Single-message `UIDL n` now answers `-ERR` for a malformed id, and the
  full-list form pre-scans every message and answers `-ERR` if any id is malformed
  or duplicated before writing the `+OK` header, so a bad id can never corrupt the
  dot-terminated block mid-stream.
- **`NOOP` and `RSET` are now rejected before authentication.** Both are
  TRANSACTION-state commands (RFC 1939 §5), reachable only after a successful
  login, but they were dispatched with no authentication check, so a pre-auth
  client received `+OK` — and `RSET` even reported a message count for a maildrop
  that was not yet open. They now answer `-ERR` in the AUTHORIZATION state and
  continue to work once the session is authenticated.
- **`QUIT` now answers `-ERR` when a maildrop deletion fails.** On `QUIT` the
  server enters the UPDATE state and commits every message marked for deletion
  (RFC 1939 §6). A failed backend deletion was only logged while `QUIT` still
  answered `+OK`, telling the client its deletions were durable — so a client that
  trusts that reply may drop its local copies and silently lose mail that is still
  on the server. Every deletion is still attempted, but `QUIT` now answers `-ERR`
  when any of them fails and `+OK` only when the update fully succeeds.
- **Credentials are now redacted from the per-command debug log.** With debug
  logging enabled the full raw command line was recorded, so `PASS` wrote the
  user's cleartext password to the logs (also capturing mistyped-but-valid
  passwords on failed attempts). The argument of credential-bearing verbs — `PASS`,
  `APOP`, and `AUTH` — is now replaced with `<redacted>` before logging; the
  command keyword is kept for debuggability and all other command lines are logged
  verbatim.
- **No-argument commands now reject a spurious trailing argument with `-ERR`.**
  `STAT`, `RSET`, `NOOP`, `QUIT`, `STLS`, and `CAPA` take no arguments (RFC 1939
  §3, RFC 2595, RFC 2449), but a trailing token was silently dropped and the
  command still succeeded — `STAT junk` answered `+OK`. Such a line is a syntax
  error and is now refused with `-ERR`, leaving session state untouched (a
  mistyped `QUIT` no longer enters the UPDATE phase and commits deletions). The
  bare forms and every argument-taking command are unaffected.
- **Command lines are now length-bounded, closing a pre-auth memory DoS.**
  Command lines were read with an unbounded `bufio.Reader.ReadString('\n')`, so a
  client — before authenticating — could stream an arbitrarily large "line" with
  no CRLF and force the process to buffer all of it, exhausting memory. The reader
  is now capped at a generous per-line limit (RFC 1939 §3 commands are tiny); a
  line that exceeds it is refused with `-ERR` and the connection is closed rather
  than buffered without limit.

## [0.1.2] - 2026-07-25

### Fixed

- **RETR/TOP now normalize bare LF to CRLF so dot-stuffing holds.** Messages
  stored with a bare LF (an LF not preceded by CR) were framed by splitting on
  CRLF only, so a bare LF survived inside a line and its content was never
  dot-stuffed. A line that was exactly `.` reached the wire unescaped as a
  premature multi-line terminator, letting attacker-controllable message content
  desync the session and forge responses. Retrieved bytes are now canonicalized
  to CRLF before framing, so dot-stuffing and the reported octet count operate on
  real line boundaries.
- **A panicking session no longer crashes the whole server.** A panic in any
  command handler or `Backend`/`Mailbox` call was unrecovered and took down the
  per-connection goroutine along with every other concurrent session. Each
  session now recovers, logs the panic with its stack, replies `-ERR`, and closes
  only that one connection.
- **A `nil` mailbox from `Authenticate` no longer causes a nil dereference.** A
  backend returning `(nil, nil)` from `Authenticate` authenticated the user but
  left a nil `Mailbox`, which was then dereferenced (SIGSEGV). `PASS` now rejects
  a nil mailbox with `-ERR` and stays in AUTHORIZATION.
- **Failed maildrop load no longer wedges the session in TRANSACTION.** The
  session was marked authenticated before `Mailbox.Messages()` was called, so a
  load failure left it authenticated with a nil message list: `STAT` answered
  `+OK 0 0` over a phantom empty maildrop and `USER` retries were refused. The
  maildrop is now loaded first and the authenticated state committed only on
  success; on failure the session replies `-ERR` and remains in AUTHORIZATION so
  the client can retry.
- **RETR/TOP no longer emit a spurious blank line before the terminator.**
  Splitting a well-formed message (which ends in CRLF) on CRLF left a trailing
  empty element that was sent as an extra blank line, so the transmitted octets
  exceeded the advertised count and disagreed with LIST/STAT sizes. The trailing
  empty element is now dropped, so the wire octets match the advertised size.

## [0.1.1] - 2026-07-23

- Initial tagged release lineage (see Git history for v0.1.0 and v0.1.1).
