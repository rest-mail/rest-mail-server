# POP3 RFC checklist

This checklist covers every RFC this library implements: RFC 1939 (POP3),
RFC 2449 (CAPA, RESP-CODES, PIPELINING and the other extensions), RFC 2595
(STLS), RFC 3206 (SYS and AUTH response codes), RFC 4616 (SASL PLAIN),
RFC 5034 (AUTH), RFC 6856 (UTF8) and RFC 8314 (implicit TLS). Each RFC was read
line by line on 2026-10-22, and every MUST, SHOULD, MAY and their negations has
a row: either mapped to a wire test that asserts it, or `n/a` with the reason
(a client, host, operator or specification duty, a feature not implemented, or
a named known gap). The audit records near the end say what each pass found. A
mapped row means its named test asserts the stated behavior; it does not prove
the whole referenced section, and this is not a certification.

Requirements below paraphrase the primary texts:
[RFC 1939](https://www.rfc-editor.org/rfc/rfc1939.txt),
[RFC 2449](https://www.rfc-editor.org/rfc/rfc2449.txt),
[RFC 2595](https://www.rfc-editor.org/rfc/rfc2595.txt),
[RFC 3206](https://www.rfc-editor.org/rfc/rfc3206.txt),
[RFC 4616](https://www.rfc-editor.org/rfc/rfc4616.txt),
[RFC 5034](https://www.rfc-editor.org/rfc/rfc5034.txt),
[RFC 6856](https://www.rfc-editor.org/rfc/rfc6856.txt), and
[RFC 8314](https://www.rfc-editor.org/rfc/rfc8314.txt).
`Normative` denotes protocol text without an explicit RFC 2119 keyword.

## RFC 1939: additional core command and state requirements

These public Session/net.Pipe proofs exercise all ASCII case permutations of
core command keywords, exact backend credential arguments (including 40-character
arguments), a fresh session over the same in-memory maildrop, and a transient
Retrieve error with partial bytes. The partial bytes must not start a positive
multiline reply or leak into the following command's response. Empty command
lines are syntax errors, including while USER is pending; they must receive a
negative reply and consume that pending USER just like other invalid commands.
The lock and resource rows explicitly record host integration duties and the
current API gap; `n/a` does not mean those RFC duties have been fulfilled.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-CORE-01 | RFC 1939 §3 | Normative | Accept every ASCII case permutation of USER, PASS, STAT, LIST, RETR, DELE, NOOP, RSET and QUIT keywords, preserving their command semantics and response framing. | TestCoreCommandCasePermutations |
| POP3-CORE-02 | RFC 1939 §3 | MUST | Answer a syntactically invalid empty command with negative status in AUTHORIZATION and TRANSACTION; preserve transaction deletion marks. | TestEmptyCommandRefusal |
| POP3-USER-02 | RFC 1939 §§3, 7 (USER) | Normative | Interpret case insensitivity on the keyword only: deliver the mailbox-identifying argument unchanged to the backend that defines its significance. | TestCredentialArgumentsPreserved |
| POP3-PASS-06 | RFC 1939 §§3, 7 (PASS) | Normative | Deliver the server/mailbox-specific password argument unchanged, including case and embedded spaces. | TestCredentialArgumentsPreserved |
| POP3-AUTH-05 | RFC 1939 §4 | Normative | Enter TRANSACTION with no messages marked deleted, including a fresh session over the same maildrop after an earlier session aborted with deletion marks. | TestFreshSessionDeletionMarks |
| POP3-RETR-03 | RFC 1939 §5 (RETR) | Normative | An unsuccessful RETR sends negative status rather than a positive multiline message; a later RETR may succeed in the same TRANSACTION. | TestRetrievalFailureRecovery |
| POP3-LOCK-01 | RFC 1939 §4 | Normative | Acquire exclusive maildrop access as necessary before reporting successful authentication, preventing modification or removal before UPDATE. | n/a: host/backend integration duty; Session does not acquire storage locks and the Backend/Mailbox interfaces expose no lock operation. ErrInUse refusal is tested separately, not proof of actual exclusive ownership. Planned follow-up: [#84](https://github.com/rest-mail/go-pop3/issues/84). |
| POP3-LOCK-02 | RFC 1939 §4 | Normative | Release any acquired maildrop lock before rejecting maildrop access. | n/a: host/backend integration duty; no lock release callback exists when Authenticate or Messages fails, so the backend must arrange cleanup itself. Planned follow-up: [#84](https://github.com/rest-mail/go-pop3/issues/84). |
| POP3-UPDATE-07 | RFC 1939 §6 | Normative | Release the exclusive-access lock and all maildrop resources, then close the connection after UPDATE. | n/a: Session closes its connection (POP3-UPDATE-03), but Mailbox has no Close or unlock callback; actual storage resource and lock release cannot be asserted by a library wire test. Planned follow-up: [#84](https://github.com/rest-mail/go-pop3/issues/84). |

## RFC 1939: selected AUTHORIZATION and USER/PASS behavior

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-AUTH-01 | RFC 1939 §§3–4 | Normative | Greet positively; refuse transaction commands before authentication. | TestAuthorizationUserPass |
| POP3-AUTH-02 | RFC 1939 §3 | Normative | Accept ASCII case variations of USER and PASS keywords. | TestAuthorizationUserPass |
| POP3-USER-01 | RFC 1939 §7 (USER) | Normative | Require a mailbox argument; USER alone does not authenticate; reject USER in TRANSACTION. | TestAuthorizationUserPass |
| POP3-PASS-01 | RFC 1939 §7 (PASS) | Normative | Require a password and a preceding accepted USER; reject PASS in TRANSACTION. | TestAuthorizationUserPass |
| POP3-PASS-02 | RFC 1939 §§4, 7 (PASS) | Normative | Successful USER/PASS opens maildrop access. | TestAuthorizationUserPass |
| POP3-PASS-03 | RFC 1939 §§4, 7 (USER/PASS) | Normative | Invalid credentials receive a negative reply; permit a new authentication attempt if connected. | TestAuthenticationFailureRetry |
| POP3-AUTH-03 | RFC 1939 §4 | Normative | Maildrop opening failure receives a negative reply and grants no transaction access. | TestMaildropOpenFailure |
| POP3-PASS-04 | RFC 1939 §7 (PASS) | MAY | Treat embedded password spaces as password data. | TestPasswordContainsSpaces |
| POP3-PASS-05 | RFC 1939 §7 (PASS), Appendix A | Normative | Accept PASS only immediately after a successful USER in AUTHORIZATION; intervening commands and empty PASS invalidate pending USER without backend calls, and a fresh USER/PASS pair can retry. | TestPassRequiresImmediateUser |
| POP3-AUTH-04 | RFC 1939 §§3–4, 7 (USER) | Normative | QUIT in AUTHORIZATION replies positively and closes, including after accepted USER. | TestAuthorizationQuit |

## RFC 2449: selected CAPA behavior and capability inventory

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-CAPA-01 | RFC 2449 §5 | Normative | Accept argumentless CAPA in AUTHORIZATION and TRANSACTION; reject an argument. | TestCapaBothStates |
| POP3-CAPA-02 | RFC 2449 §§3–5 | Normative | Positive CAPA has one capability per CRLF line, at most 512 octets per line, then a dot CRLF terminator. | TestCapaBothStates |
| POP3-CAPA-03 | RFC 2449 §5 | MUST | Retain AUTHORIZATION capabilities in TRANSACTION (tested without TLS). | TestCapaBothStates |
| POP3-CAPA-04 | RFC 2449 §6.2 | Normative | Announce parameterless USER in both states for USER/PASS support. | TestCapaBothStates |
| POP3-CAPA-05 | RFC 2449 §6.5 | Normative | When the Backend opts in, announce LOGIN-DELAY with the minimum seconds between logins in both states, followed by USER only in AUTHORIZATION when the value varies per user, and the user's own value in TRANSACTION. | TestCapaLoginDelay, TestCapaLoginDelayRequiresBackend |
| POP3-CAPA-06 | RFC 2449 §6.1 | Normative | Announce parameterless TOP unchanged in both states; the advertised command is usable only in TRANSACTION. | TestCapaTopContract |
| POP3-CAPA-07 | RFC 2449 §6.8 | Normative | Announce parameterless UIDL unchanged in both states; the advertised single and multiline commands are usable only in TRANSACTION. | TestCapaUIDLContract |
| POP3-CAPA-08 | RFC 2449 §6.3 | Normative | If AUTH/SASL is supported, advertise the SASL tag with the supported mechanism list in both states. | TestSASLCapability |
| POP3-CAPA-09 | RFC 2449 §6.7 | SHOULD | Sites allowing indefinite retention announce EXPIRE NEVER; a finite EXPIRE value describes the guaranteed minimum retention and accounts for per-user policy. | n/a: host retention policy is unavailable through Backend/Mailbox, and EXPIRE is not advertised. This is an integration/API gap; neither indefinite retention nor compliance with the site recommendation is proved. Planned follow-up: [#85](https://github.com/rest-mail/go-pop3/issues/85). |
| POP3-CAPA-10 | RFC 2449 §6.9 | Normative | When IMPLEMENTATION is announced, supply identifying tokens in both states or optionally TRANSACTION only. | TestCapaImplementation, TestCapaImplementationRequiresBackend |
| POP3-CAPA-11 | RFC 2449 §§3, 5 | Normative | Emit nonempty capability tags and space-separated nonempty VCHAR parameters with complete CRLF/dot framing, including combined LOGIN-DELAY, UTF8, IMPLEMENTATION (both states or TRANSACTION only) and STLS configurations before TLS, after TLS and after login. | TestCapaConfiguredGrammar |

The configuration matrix combines the optional LOGIN-DELAY and UTF8 backend
interfaces, with and without TLS. It checks the full actual CAPA list before TLS,
after a verified handshake, and after authentication, including parameter changes.
TOP/UIDL capability proofs also execute their advertised commands and check their
state restrictions. An `ImplementationBackend` announces IMPLEMENTATION in
both states or after login only, and text outside the §9 param grammar or the
512-octet line limit omits the tag (POP3-CAPA-10). The n/a entries distinguish
unsupported optional features,
unavailable host retention policy, and duties belonging to clients; they are not
proofs of those features or host/client conformance.

## RFC 1939 §5: selected TRANSACTION requirements

Sizes here use a backend fixture with canonical CRLF messages and correct octet
metadata; this does not certify arbitrary backend metadata or all message formats.
Deletion references below cover LIST, RETR and DELE; selected TOP/UIDL proofs follow in §7.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-TRANS-01 | RFC 1939 §5 | Normative | Require TRANSACTION for STAT, LIST, RETR, DELE, NOOP and RSET. | TestTransactionArguments |
| POP3-TRANS-02 | RFC 1939 §§3, 5 | Normative | Reject arguments to STAT/NOOP/RSET and missing or invalid required message numbers. | TestTransactionArguments |
| POP3-STAT-01 | RFC 1939 §5 (STAT) | Normative | Reply positively with decimal message count and total octets separated by spaces. | TestTransactionStatList, TestTransactionEmptyMaildrop |
| POP3-STAT-02 | RFC 1939 §5 (STAT) | Normative | Exclude deletion marks from count and size. | TestTransactionDeleteReset |
| POP3-LIST-01 | RFC 1939 §§4–5 (LIST) | Normative | List each message using its stable one-based decimal number and exact octet size. | TestTransactionStatList, TestTransactionDeleteReset |
| POP3-LIST-02 | RFC 1939 §§3, 5 (LIST) | Normative | Argumentless LIST sends positive status, scan lines and dot terminator; empty maildrops have no scan lines. | TestTransactionStatList, TestTransactionEmptyMaildrop |
| POP3-LIST-03 | RFC 1939 §5 (LIST) | Normative | LIST with a valid number returns its scan listing; reject nonexistent or deleted messages and omit deleted scan lines. | TestTransactionStatList, TestTransactionArguments, TestTransactionDeleteReset |
| POP3-RETR-01 | RFC 1939 §§3, 5 (RETR) | Normative | Positive RETR sends the selected entire message with CRLF framing, leading-dot stuffing and a dot terminator. | TestTransactionRetrieval |
| POP3-RETR-02 | RFC 1939 §5 (RETR) | Normative | Reject nonexistent and deletion-marked message numbers. | TestTransactionArguments, TestTransactionDeleteReset |
| POP3-DELE-01 | RFC 1939 §5 (DELE) | Normative | Mark a valid message positively; reject nonexistent and already marked messages; defer physical removal. | TestTransactionArguments, TestTransactionDeleteReset |
| POP3-NOOP-01 | RFC 1939 §5 (NOOP) | Normative | Reply positively without altering message visibility or deletion marks. | TestTransactionNoop |
| POP3-RSET-01 | RFC 1939 §5 (RSET) | Normative | Reply positively and clear all deletion marks, restoring listings, counts and retrieval. | TestTransactionDeleteReset |

## RFC 1939 §6: selected UPDATE/QUIT requirements

These proofs observe wire replies, session termination and per-UID backend calls.
Successful fixture deletes model persistence; actual storage durability, exclusive
maildrop ownership, lock acquisition/release and resource cleanup are backend
integration contracts outside this checked inventory. The Mailbox interface has
no multi-message transaction or lock-release operation. On deletion failure the
RFC permits some or none of the marked messages to be removed; these tests prove
both outcomes without claiming atomicity, rollback or a deletion order.
AUTHORIZATION QUIT remains covered by POP3-AUTH-04 above.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-UPDATE-01 | RFC 1939 §§3, 6 | Normative | QUIT takes no arguments; reject an argument without committing or leaving TRANSACTION. | TestUpdateQuit |
| POP3-UPDATE-02 | RFC 1939 §6 | Normative | TRANSACTION QUIT removes marked messages on success; never remove unmarked messages, including on failure or after RSET. | TestUpdateQuit, TestUpdateResetQuit, TestUpdateDeleteFailure |
| POP3-UPDATE-03 | RFC 1939 §6 | Normative | Reply to QUIT with the update status and close the connection after success or failure. | TestUpdateQuit, TestUpdateResetQuit, TestUpdateDeleteFailure |
| POP3-UPDATE-04 | RFC 1939 §6 | Normative | Report successful removal with a positive QUIT reply. | TestUpdateQuit |
| POP3-UPDATE-05 | RFC 1939 §6 | MUST NOT | Remove messages when a session ends without client-issued QUIT (tested with EOF and an incomplete command). | TestUpdateDisconnect |
| POP3-UPDATE-06 | RFC 1939 §6 | Normative | Report removal failure negatively; some or none of the marked messages may have been removed. | TestUpdateDeleteFailure |

## RFC 1939 §7: selected optional TOP/UIDL requirements

TOP and within-session UIDL tests reuse the canonical message corpus. All use
public Session/net.Pipe helpers with opaque backend UIDs distinct from wire
message numbers. UID stability
within one session includes DELE/RSET. Sequential-session tests additionally
prove Session faithfully emits stable backend IDs from a synchronized in-memory
model after committed deletion renumbers a survivor, after a distinct fixture
message is appended, and after EOF without UPDATE. These are fixture proofs, not
production backend persistence, crash recovery, global UID generation/non-reuse
or maildrop lifetime guarantees; those remain integration follow-ups. Format checks
exercise existing fail-safe error handling, not new runtime work. The duplicate-ID
negative case records the library's current rejection policy, not a claim that the
RFC forbids equal hashes for identical message copies.

Sequential sessions use dynamic copied Messages snapshots reflecting successful
Delete calls. STAT checks fixed fixture metadata, not message-body octet accuracy.
Every connection has bounded wire deadlines and explicit QUIT/EOF with a joined
session goroutine before reconnecting; no database or socket listener is used.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-TOP-01 | RFC 1939 §§3, 7 (TOP) | Normative | Require TRANSACTION, a valid message number and a non-negative line count; reject missing, invalid or extra arguments. | TestOptionalArguments |
| POP3-TOP-02 | RFC 1939 §7 (TOP) | Normative | TOP with zero body lines sends headers and one blank separator, with no body lines (including empty-header and headers-only fixtures). | TestOptionalTopFraming |
| POP3-TOP-03 | RFC 1939 §§3, 7 (TOP) | Normative | Send at most the requested body lines, clamping to the available body; use CRLF, leading-dot stuffing and exactly one dot terminator. | TestOptionalTopFraming |
| POP3-TOP-04 | RFC 1939 §§5, 7 (TOP) | Normative | Refuse deletion-marked messages; RSET restores access using the original message number. | TestOptionalDeleteReset |
| POP3-UIDL-01 | RFC 1939 §§3, 7 (UIDL) | Normative | Require TRANSACTION; reject invalid, nonexistent or extra message-number arguments. | TestOptionalArguments |
| POP3-UIDL-02 | RFC 1939 §§3, 7 (UIDL) | Normative | Single and multiline listings contain the exact decimal message number, one space and UID only; terminate lists with dot CRLF, including an empty maildrop. | TestOptionalUIDLListing |
| POP3-UIDL-03 | RFC 1939 §§4–5, 7 (UIDL) | Normative | Retain numbers and UIDs across repeated commands within a session; exclude deletion marks from lists and single replies; RSET restores the original listings. | TestOptionalDeleteReset |
| POP3-UIDL-04 | RFC 1939 §7 (UIDL) | Normative | Emit UIDs of 1–70 printable ASCII characters (0x21–0x7e); reject malformed visible backend UIDs without corrupting subsequent response framing. | TestOptionalUIDLListing, TestOptionalUIDLInvalidBackend |
| POP3-UIDL-05 | RFC 1939 §7 (UIDL) | Normative | In sequential sessions over a stable-ID memory backend, retain a survivor UID when successful QUIT deletion renumbers it from 2 to 1; a later appended fixture has a distinct UID without changing the survivor identity. | TestUIDLSequentialSessionsDeleteAndAppend |
| POP3-UIDL-06 | RFC 1939 §7 (UIDL) | Normative | In sequential sessions over a stable-ID memory backend, retain original UIDs and listings after EOF without UPDATE, including an uncommitted DELE mark. | TestUIDLSequentialSessionsWithoutUpdate |

## RFC 2595 §4: selected POP3 STLS and CAPA transitions

POP3 STLS is specified in RFC 2595 **§4**; §3 specifies IMAP STARTTLS.
These proofs use public Session/net.Pipe with an actual TLS 1.3 handshake,
a temporary self-signed localhost certificate trusted by the client, normal
certificate/hostname verification, bounded client deadlines and joined session
goroutines. TLS 1.3 is the test fixture's choice, not a claimed RFC 2595 mandate.
Re-issued encrypted CAPA verifies the transition, not client cache management.
POP3-STLS-08 additionally configures mandatory, verified TLS client certificates
and observes the server verify the actual peer certificate; even then, no
maildrop command succeeds until a separate USER/PASS exchange.
The two POLICY rows describe existing library choices: configuring TLS refuses
plaintext USER/PASS, and a failed handshake closes the session. They are not
universal RFC mandates. POP3-STLS-07 is also library policy: RFC 2595 does not
spell out buffered-input handling, but the STARTTLS command-injection class
(CVE-2011-0411) is avoided by never reading plaintext octets past the STLS line
under TLS. A temporary control that carried the buffered octets into the TLS
reader (pkg/pop3/server/session.go restored byte-for-byte) authenticated the injected USER/PASS
and failed the test. Nil TLS configuration permits plaintext authentication.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-STLS-01 | RFC 2595 §4; RFC 2449 §5 | Normative | Advertise parameterless STLS through CAPA when available before TLS; omit it and refuse STLS when TLS is not configured. | TestSTLSNoConfig, TestSTLSCredentials |
| POP3-STLS-02 | RFC 2595 §4 | Normative | STLS takes no arguments; refuse extra arguments without starting TLS or losing the plaintext session. | TestSTLSArguments |
| POP3-STLS-03 | RFC 2595 §4 | Normative | Start TLS immediately after the positive STLS reply's CRLF; subsequent POP3 commands use the negotiated TLS layer. | TestSTLSCredentials |
| POP3-STLS-04 | RFC 2595 §4 | Normative | Remain in AUTHORIZATION after STLS; do not grant transaction access until USER/PASS succeeds. | TestSTLSCredentials |
| POP3-STLS-05 | RFC 2595 §4; RFC 2449 §5 | Normative | Re-issued CAPA after TLS omits STLS and retains USER; preserve that encrypted capability list after authentication. | TestSTLSCredentials |
| POP3-STLS-06 | RFC 2595 §4 | Normative | Permit STLS only in AUTHORIZATION; refuse it in TRANSACTION and exercise the permitted refusal when TLS is already active. | TestSTLSStates |
| POP3-STLS-07 | RFC 2595 §4; RFC 2449 §6.6 (library policy) | Normative | Plaintext commands pipelined after STLS in the same read are discarded at the handshake: no injected USER/PASS authenticates or is retained, and no injected command is answered under TLS. | TestSTLSDiscardsPipelinedPlaintext |
| POP3-STLS-08 | RFC 2595 §4 | Normative | Remain in AUTHORIZATION even when a verified client certificate is supplied during STLS; require a separate POP3 login before granting maildrop access. | TestSTLSClientCertificateRequiresPOPLogin |
| POP3-STLS-09 | RFC 2595 §4 | MUST | A client discards cached server capabilities after TLS and SHOULD reissue CAPA. | n/a: this package implements a server, with no POP3 client or capability cache. Encrypted CAPA replies are tested separately; the test client's fresh query does not prove a production client's cache handling. |
| POP3-TLS-POLICY-01 | RFC 2595 §§2.5, 4 (library policy) | Normative | With TLS configured, refuse plaintext USER/PASS without calling backend authentication; accept valid credentials through TLS exactly once. | TestSTLSCredentials |
| POP3-TLS-POLICY-02 | RFC 2595 §2.5 (library policy) | Normative | Close after a malformed TLS record header during STLS negotiation without calling backend authentication. | TestSTLSHandshakeFailure |

## RFC 8314 §3.1: implicit TLS ("pop3s")

These proofs start a Server through the public `ListenAndServe` with only the
`POP3TLS` listener, the way the gateway serves port 995. The test reserves a
free loopback port, so this is the one fixture that opens a socket; the client
is a real TLS 1.3 client that verifies the temporary self-signed certificate.
Client-certificate authentication (RFC 8314 §§4.2, 5.5) is not supported; the
"even if a client certificate was supplied" clause is POP3-ITLS-07. Forced shutdown and error-path closure remain
unproven; a `POP3TLS` port with a nil TLS config is refused (POP3-ITLS-05). Selected normal closure proofs follow below.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-ITLS-01 | RFC 8314 §3.1 | Normative | Begin the TLS handshake immediately on connect; send the greeting and every reply only as TLS application data, never in the clear to a plaintext client. | TestImplicitTLSSession, TestImplicitTLSRejectsPlaintext |
| POP3-ITLS-02 | RFC 8314 §3.1 | MUST | After the +OK greeting, enter AUTHORIZATION: refuse transaction commands until USER/PASS succeeds. | TestImplicitTLSSession |
| POP3-ITLS-03 | RFC 8314 §3.1; RFC 2595 §4; RFC 2449 §5 | Normative | On an implicit-TLS connection, omit STLS from CAPA before and after authentication, refuse STLS as already active, and accept USER/PASS without STLS. | TestImplicitTLSSession |

## RFC 8314 §3.4: selected implicit-TLS connection closure

These proofs run through the public implicit-TLS listener with TLS 1.2 and 1.3,
in AUTHORIZATION and TRANSACTION. A normal QUIT reply is followed by a decrypted
TLS close_notify and then TCP EOF. A client can instead send its close_notify
without QUIT or closing TCP: the server sends its own alert and terminates TCP.
The client transport replaces bare TCP EOF with a distinct error, because Go's
TLS reader otherwise accepts it at a record boundary; TLS EOF alone would not
prove an alert was sent. Certificate and hostname verification stay enabled.
Server cleanup drains sessions with bounded deadlines. No deletion, forced
shutdown, failed-handshake, idle-timeout or STLS closure claim is made here.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-ITLS-04 | RFC 8314 §3.4 | SHOULD | Initiate TLS close alerts before TCP termination (tested after QUIT and in response to a client close alert, before and after authentication, with TLS 1.2 and 1.3). | TestImplicitTLSCloseNotify |

## RFC 2449 §6.6: selected PIPELINING requirements

Each bounded batch is written completely before reading any individual reply,
either after sequential login or across USER/PASS authentication. Public
Session/net.Pipe proofs use in-memory backends; transaction proofs reuse the
canonical CRLF corpus and opaque UIDs distinct from wire numbers. Exact response
lines check order, CRLF, dot stuffing and complete multiline terminators before
subsequent replies. Small batches fit the session's
command-reader buffer, avoiding synchronous net.Pipe write deadlock; this does
not prove arbitrary batch sizes or transport-window handling. Existing bounded
client deadlines and session cleanup apply; no extra goroutines are started.
TLS pipelining, all extension combinations, backend lifetime,
rate policy and atomic batch guarantees are outside this selected inventory.
Exact explanatory reply text below records current library behavior; the RFC
requires the relevant status and ordering, not those particular words.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-PIPELINING-01 | RFC 2449 §6.6 | Normative | Announce parameterless PIPELINING in AUTHORIZATION and TRANSACTION without differing arguments. | TestPipeliningCapabilities |
| POP3-PIPELINING-02 | RFC 2449 §6.6 | MUST | Process each command in turn: a queued NOOP/STAT/LIST 1/UIDL 1 batch returns the corresponding complete single-line replies in order. | TestPipeliningSingleReplies |
| POP3-PIPELINING-03 | RFC 2449 §6.6; RFC 1939 §§3, 5, 7 | MUST | Process queued LIST/TOP/RETR/NOOP in turn, finishing each multiline block and dot terminator before the next response. | TestPipeliningMultilineReplies |
| POP3-PIPELINING-04 | RFC 2449 §6.6; RFC 1939 §3 | MUST | Reply negatively to invalid and unknown queued commands in order; subsequent valid commands retain their replies and session access. | TestPipeliningErrorRecovery |
| POP3-PIPELINING-05 | RFC 2449 §6.6; RFC 1939 §5 | MUST | Process queued DELE, queries and RSET in turn; repeated batches observe deletion then restoration with stable message numbers and UIDs. | TestPipeliningStateOrder |
| POP3-PIPELINING-06 | RFC 2449 §6.6 | MUST | Process a bounded CAPA/STAT/USER/PASS/STAT/CAPA/NOOP batch in turn across successful login, completing each capability block before subsequent replies and loading messages only after accepted credentials. | TestAuthPipeliningSuccess |
| POP3-PIPELINING-07 | RFC 2449 §6.6 | MUST | Process a bounded failed USER/PASS, unauthenticated STAT, and fresh successful USER/PASS/STAT batch in turn with ordered replies and exactly the corresponding backend authentication and enumeration calls. | TestAuthPipeliningFailureRetry |
| POP3-PIPELINING-POLICY-01 | Library policy (pkg/pop3/server/session.go authentication reset/retry) | Normative | Within one queued batch, a failed PASS clears pending USER: refuse PASS without fresh USER without calling Authenticate, retain AUTHORIZATION, and allow a fresh successful USER/PASS. | TestAuthPipeliningFailureRetry |

The authentication batches additionally check CAPA multiline framing and exact
Authenticate arguments/call order, with no Messages before accepted credentials,
no retrieval/mark-seen/delete calls, fresh STAT/NOOP, and QUIT/EOF with joined
session goroutines. They use nil TLS and the no-op limiter. RFC 2449 §6.6 gives
USER/PASS batching as an example; reset/retry details above record existing
library policy, not an additional RFC mandate or strict immediate-PASS claim.

## RFC 2449 §§3, 6.4, 8 and RFC 3206 §§5–6: selected RESP-CODES/AUTH behavior

RFC 2449 §8 permits optional bracketed response codes; advertising RESP-CODES
does not require a code on every -ERR. RFC 3206 §5 defines AUTH for credential
problems, including wrong passwords and unknown accounts. These public
Session/net.Pipe tests pin that code and its wire placement, not merely error
prose. The exact uppercase spelling, explanatory text and spacing after the
closing bracket record current library output, not additional RFC mandates.
The POLICY rows are library regression contracts, represented as `Normative`
for the checker's existing vocabulary; they are not RFC MUST/SHOULD claims.
They pin identical public credential-failure replies, a fresh USER after a
failed PASS, and maildrop-load errors without credential AUTH labeling or
backend detail disclosure. Fixtures use memory, nil TLS and the default no-op
limiter except for the IN-USE proof below; ordinary backend Authenticate errors
are treated as credential rejection. Arbitrary system-error classification is
not covered by this inventory.
RFC 3206 §6 says a server that labels credential failures with AUTH SHOULD
advertise AUTH-RESP-CODE. The library now does: the PASS failure that earns a
limiter ban is credential-caused, so its reply carries AUTH as well.
RFC 3206 §4 defines SYS/TEMP and SYS/PERM for system errors on PASS. After good
credentials, a maildrop that fails to load replies SYS/TEMP (likely transient,
such as a locked or unavailable store) and a backend that returns no Mailbox
replies SYS/PERM (a backend bug that needs intervention). Neither counts against
the limiter. Backend errors can explicitly select the class with
`ErrSystemTemporary` or `ErrSystemPermanent`, directly or wrapped. Unclassified
maildrop-load errors still default to TEMP; unclassified authentication errors
remain credential failures under the backend contract.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-RESP-CODES-01 | RFC 2449 §§6.4, 8 | Normative | Announce parameterless RESP-CODES in AUTHORIZATION and TRANSACTION for extended response-code support. | TestResponseCodeCapabilities |
| POP3-RESP-CODES-02 | RFC 2449 §§3, 8; RFC 3206 §5 | Normative | On coded wrong-password and unknown-account PASS failures, AUTH denotes credential trouble and begins response text in brackets after the status space, with CRLF framing. | TestResponseCodeCredentialFailures |
| POP3-RESP-CODES-03 | RFC 3206 §6 | SHOULD | Every credential-caused PASS failure carries AUTH (wrong password, unknown account, and the failure that earns a limiter ban), so CAPA lists parameterless AUTH-RESP-CODE exactly once in AUTHORIZATION and TRANSACTION. | TestResponseCodeAuthRespCodeCapability |
| POP3-RESP-CODES-04 | RFC 3206 §4 | Normative | On a system failure after good credentials, PASS replies `-ERR [SYS/TEMP]` when the maildrop fails to load and `-ERR [SYS/PERM]` when the backend returns no Mailbox, never AUTH; no limiter failure is recorded and the session stays in AUTHORIZATION. | TestResponseCodeSystemFailures |
| POP3-RESP-CODES-05 | RFC 2449 §8.1.2 | Normative | Backend-signalled maildrop contention after valid credentials returns -ERR [IN-USE] on PASS, for direct or wrapped ErrInUse from Authenticate, AuthenticateUTF8 or Messages; ignore partial results, clear USER, retain AUTHORIZATION without a limiter failure/ban check, and permit fresh login or QUIT. | TestResponseCodeInUse |
| POP3-RESP-CODES-06 | RFC 2449 §8.1.1 | Normative | Backend-signalled login delay after valid credentials returns -ERR [LOGIN-DELAY] on PASS (never on USER), for direct or wrapped ErrLoginDelay from Authenticate; ignore partial results, clear USER, retain AUTHORIZATION without a limiter failure/ban check, and permit fresh login or QUIT. | TestResponseCodeLoginDelay |
| POP3-RESP-CODES-07 | RFC 3206 §4 | Normative | Backend-classified system failures from Authenticate or Messages return SYS/TEMP or SYS/PERM on PASS, directly or wrapped, never AUTH or private details; discard partial results, consume USER, retain AUTHORIZATION, and permit retry or QUIT without UPDATE. Authentication system failures neither record nor reset credential failures nor check bans. | TestResponseCodeBackendSystemFailures |
| POP3-RESP-CODES-08 | RFC 2449 §§3, 6.4, 8 | Normative | Extended response codes are optional even after RESP-CODES is advertised: ordinary positive/negative replies and bracketed credential errors can coexist without changing command framing or recovery. | TestResponseCodesRemainOptional |
| POP3-RESP-POLICY-01 | RFC 3206 §5 (library policy) | Normative | Accept USER identically for known and unknown accounts; use the same generic AUTH failure reply for wrong passwords and unknown accounts. | TestResponseCodeCredentialFailures |
| POP3-RESP-POLICY-02 | RFC 1939 §§4, 7 (library policy) | Normative | After a credential failure, reject PASS without a fresh USER, retain AUTHORIZATION, and permit successful USER/PASS with usable STAT/NOOP. | TestResponseCodeAuthenticationRetry |
| POP3-RESP-POLICY-03 | RFC 1939 §4; RFC 3206 §5 (library policy) | Normative | With valid credentials but maildrop enumeration failure, reply with -ERR [SYS/TEMP] without AUTH or backend details; clear USER, refuse transaction access, and permit another attempt and QUIT. | TestResponseCodeMaildropFailure |

The IN-USE proof uses an explicit backend contract: `ErrInUse` (direct or
wrapped with `%w`) means credentials have already been verified and another
session owns the maildrop. `Authenticate`, `AuthenticateUTF8` and `Messages`
may report it.
The engine resets credential-failure history but neither records a failure nor
checks for a ban. It discards partial mailbox/message results and consumes the
pending USER. Each backend interface runs eight wire cases covering the two
reporting points, direct/wrapped errors, repeated contention, and either
successful retry with the correct maildrop or AUTHORIZATION QUIT without UPDATE. Counters observe every backend
operation and limiter call. Actual exclusive locking/release remains a backend
responsibility; unrelated backend errors and APOP/SASL remain outside this
proof.

The LOGIN-DELAY proof uses the same shape: `ErrLoginDelay` (direct or wrapped)
from `Authenticate` means credentials were verified but the user logged in too
recently. PASS replies `-ERR [LOGIN-DELAY]`; USER always replies `+OK`, as RFC
2449 §8.1.1 recommends, so the code never reveals that an account exists before
its password is checked. Four wire cases cover direct/wrapped errors, repeated
refusal, and either successful retry or AUTHORIZATION QUIT without UPDATE.
Tracking login times and choosing the delay remain the backend's job. A
`LoginDelayBackend` announces the optional `LOGIN-DELAY` CAPA tag (RFC 2449
§6.5, POP3-CAPA-05): its minimum in AUTHORIZATION, with `USER` when the delay
varies per user, and the value from a `LoginDelayMailbox` (or the minimum) in
TRANSACTION. Announcing a delay does not enforce it.

The backend-system proof covers 16 wire cases: TEMP/PERM, Authenticate/Messages,
direct/wrapped errors and retry/QUIT. Authentication infrastructure failures can
happen before credential verification, so they neither charge nor clear the
limiter's credential-failure history and never check its ban list. After
Authenticate succeeds, Messages failures retain the existing successful-login
reset. Partial mailboxes/message lists are discarded. TEMP versus PERM is the
backend's decision: TEMP permits a later retry; PERM calls for intervention.
These sentinels apply to login opening only; transaction errors are outside
this proof.

## RFC 2449 §§3–4: selected command-length support and library policy

CAPA servers MUST support commands up to 255 octets including CRLF. This is
an acceptance requirement, not a server rejection ceiling at 255. The normative
proof sends exact 255-octet ASCII USER and PASS lines, both whole and with
fragmented CRLF, and checks complete credential bytes at Authenticate, successful
authentication and subsequent response framing. This covers selected credential
commands, not every command, capability-specific maximum or parser grammar.

RFC 2449 §4 leaves the first line of a response, including the greeting, at
512 octets including CRLF. POP3-LENGTH-02 sends a maximum-length line for each
keyword in each state, mostly with out-of-range or extra arguments, and checks
that every status line stays within that limit. Status text is fixed by the
library and never echoes the command. Negative control: echoing the line in the
unknown-command error fails the test at 1044 octets.

The POLICY rows describe the existing 1024-byte bounded reader, including LF.
Acceptance beyond 255, non-ASCII credential bytes and bare LF are implementation
controls, not claims of RFC-conforming client syntax. A buffer of 1024 bytes
without LF receives one exact error and connection termination; the server does
not resynchronize. Exact explanatory error text is library behavior. These tests
use only public Session/net.Pipe and memory; runtime limits and parsing are unchanged.
The checker accepts `Normative` as its neutral level for these explicitly labeled
library-policy rows; it does not make the policy an RFC requirement.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-LENGTH-01 | RFC 2449 §§3–4 | MUST | Support 255-octet command lines including CRLF (proved for USER/PASS with complete credentials, including fragmented delivery). | TestCommandLength255Credentials |
| POP3-LENGTH-02 | RFC 2449 §4 | Normative | Keep the first line of every response, including the greeting, within 512 octets including CRLF: maximum-length (255-octet) commands for every keyword in AUTHORIZATION and TRANSACTION, and a 1023-octet unknown command under the reader policy, each receive a bounded status line. | TestResponseFirstLineLimit |
| POP3-LENGTH-POLICY-01 | Library policy (pkg/pop3/server/session.go bounded reader) | Normative | Accept complete 1024-byte USER/PASS lines including LF, preserving credential bytes; count UTF-8 bytes rather than runes. | TestCommandLengthPolicy1024Accepted |
| POP3-LENGTH-POLICY-02 | Library policy (pkg/pop3/server/session.go bounded reader) | Normative | Refuse a 1024-byte buffer without LF with one error then close; do not authenticate or resynchronize a 1025-byte complete line or queued command. | TestCommandLengthPolicyOverflowCloses |

## RFC 1939 §3: selected unknown and unimplemented command refusals

These sequential public Session/net.Pipe proofs exercise two unknown keywords
and currently unimplemented optional APOP/AUTH commands (including mixed case)
in AUTHORIZATION, both at greeting and after USER, and in TRANSACTION after DELE.
Exact -ERR replies and the shared wire reader check uppercase status and CRLF;
following CAPA, STAT and NOOP responses check framing and retained session state.
The error prose and unchanged-state POLICY row record current library behavior,
not additional RFC mandates. Backend counters cover authentication, enumeration,
retrieval, mark-seen and deletion; QUIT/RSET controls observe the same marked UID
or no deletion after reset. Fixtures use only memory, nil TLS and the no-op limiter.
This is not an APOP/SASL implementation, authentication-error classification,
exhaustive command grammar/state inventory or new PIPELINING proof. Existing
argument/state and queued unknown-command proofs remain in their sections above.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-COMMAND-01 | RFC 1939 §3 | MUST | Reply to selected unrecognized commands with uppercase -ERR and CRLF in AUTHORIZATION and TRANSACTION. | TestCommandErrorsAuthorization, TestCommandErrorsTransaction |
| POP3-COMMAND-02 | RFC 1939 §3 | MUST | Reply to currently unimplemented optional APOP/AUTH commands with uppercase -ERR and CRLF in AUTHORIZATION and TRANSACTION. | TestCommandErrorsAuthorization, TestCommandErrorsTransaction |
| POP3-COMMAND-POLICY-01 | Library policy (pkg/pop3/server/session.go default dispatch) | Normative | Refusal keeps the session usable without authentication or backend read/delete side effects, preserving existing deletion marks until explicit RSET or QUIT. | TestCommandErrorsAuthorization, TestCommandErrorsTransaction |

## RFC 1939 §3: optional autologout timer

RFC 1939 §3 says a server MAY have an inactivity autologout timer. This library
implements one, so these rows prove that implementation; they do not claim the
timer is mandatory. Public Session/net.Pipe proofs wrap only the server end in a
recording net.Conn. Each SetDeadline is paired with the monotonic completion time
of the server's latest Write, so the ten-minute bound is exact without a clock
tolerance: the session computes its deadline only after that write returned. A
client send timestamp taken before each command bounds the deadline from the
other side, proving it was renewed for that command rather than carried over.
Exactly one renewal precedes each next command Read.

No test sleeps. Expiry is forced by moving the underlying pipe's read deadline
into the past once the session waits under its renewed deadline after DELE; the
session then sees a genuine `os.ErrDeadlineExceeded` timeout, not EOF. The
ten-minute duration is thus proved from the configured deadline, not from
elapsed wall-clock time. Fixtures use memory, nil TLS and the no-op limiter.
Expiry in AUTHORIZATION covers greeting-idle and pending-USER states
(POP3-IDLE-05), plus buffered partial USER/PASS lines (POP3-IDLE-06), and expiry
after STLS (POP3-IDLE-07), including buffered partial USER/PASS/QUIT lines
after STLS (POP3-IDLE-08). Other partial commands and real socket keepalive
behavior remain outside this selected inventory.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-IDLE-01 | RFC 1939 §3 | MUST | The implemented autologout timer is at least ten minutes: every idle interval's read deadline falls at least ten minutes after the server's final reply write. | TestIdleDeadlineReset |
| POP3-IDLE-02 | RFC 1939 §3 | Normative | Receipt of any command resets the timer: valid and invalid commands in AUTHORIZATION and TRANSACTION each renew the deadline from that command, once, before the next read. | TestIdleDeadlineReset |
| POP3-IDLE-03 | RFC 1939 §3 | Normative | On expiry the session does not enter UPDATE: a real read timeout after two DELE marks closes the connection with no response, no backend Delete and unchanged messages. | TestIdleAutologoutWithoutUpdate |
| POP3-IDLE-04 | RFC 1939 §3, §6 | Normative | A real read timeout while an incomplete command (`QUIT` or `QUIT\r`, no LF) is buffered closes without a response, never dispatches the partial command, does not enter UPDATE and makes no backend Delete after two DELE marks. | TestIdleTimeoutWithBufferedPartialCommand |
| POP3-IDLE-05 | RFC 1939 §3 | Normative | A real read timeout in AUTHORIZATION, both immediately after the greeting and after an accepted USER with no PASS sent, closes without a response and never reaches backend Authenticate (so no mailbox operation). | TestIdlePreLoginTimeout |
| POP3-IDLE-06 | RFC 1939 §3 | Normative | On a timeout with a buffered partial USER or PASS in AUTHORIZATION, close without a response or backend authentication; never dispatch the incomplete command. | TestIdlePreLoginPartialCommandTimeout |
| POP3-IDLE-07 | RFC 1939 §3; RFC 2595 §4 | Normative | After STLS, the encrypted idle interval is renewed to at least ten minutes after the handshake and each encrypted reply; a real timeout below TLS, after the handshake, after an encrypted USER, or in TRANSACTION with two DELE marks, closes with only TLS close_notify, never authenticates beyond the PASS sent and never enters UPDATE. | TestIdleTimeoutAfterSTLS |
| POP3-IDLE-08 | RFC 1939 §3, §6; RFC 2595 §4 | Normative | After STLS, a real timeout below TLS while an encrypted incomplete `USER`, `PASS` or `QUIT` (no terminator, or CR without LF) is buffered closes with only TLS close_notify, never dispatches it, never authenticates beyond the complete PASS sent and never enters UPDATE after two DELE marks. | TestIdleTimeoutAfterSTLSWithBufferedPartialCommand |

## RFC 1939 §10: ADAPTED TRANSACTION example

This is an **ADAPTED TRANSACTION** example of the published two-message,
120/200-octet sequence, not a literal full RFC session or an APOP proof. Section 10
uses APOP, which this library does not implement, and supplies no message bodies.
The test instead authenticates with existing USER/PASS (`alice`/`secret`), then
runs STAT, LIST, RETR 1, DELE 1, RETR 2, DELE 2 and QUIT in one public
Session/net.Pipe connection. The added STAT/LIST after DELE 1 is a visibility and
stable-numbering control, not a command sequence claimed from section 10.

Synthetic messages have Date/From/Subject headers, canonical CRLF, and independently
asserted byte lengths of exactly 120 and 200; backend metadata is separately fixed
to those sizes. Bodies include `.`, `..`, `.x`, a leading-dot word and a blank
line. Entire RETR bytes are checked against explicitly stuffed expectations,
including every CRLF and the final dot terminator. Backend handles are
`example-alpha`/`example-bravo`, distinct from wire numbers. Both messages stay in
memory through DELE and are removed exactly once per UID only at QUIT; EOF and
session-goroutine completion are checked. Exact explanatory reply text records
current library output, not additional normative RFC prose. No real backend
persistence, locking, APOP, socket listener or complete RFC coverage is claimed.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-EXAMPLE-01 | RFC 1939 §10 (adapted transaction example); §§3, 5–6, 11 | Normative | Exercise the published 120/200-octet transaction sequence with adapted setup and synthetic bodies: exact STAT/LIST numbers, complete RETR framing, deferred DELE, stable remaining number and successful per-UID QUIT removal/EOF. | TestRFC1939AdaptedTransactionExample |

## RFC 1939 §7: immediate PASS sequencing

`TestPassRequiresImmediateUser` exercises sequential and bounded pipelined
USER, intervening command, PASS sequences through the public Session. CAPA is
still allowed and its exact multiline reply is drained; unknown commands,
pre-auth STAT, malformed USER, empty PASS, a malformed PASS keyword, rejected
CAPA arguments and unavailable STLS all require a fresh USER before PASS.
Every stale PASS receives -ERR without Authenticate, Messages, Retrieve,
MarkSeen or Delete calls. A fresh immediate USER/PASS pair then succeeds with
exactly one Authenticate and one Messages call, followed by QUIT/EOF and joined
Handle. The existing immediate login and authentication-pipeline proofs remain.
This proves selected invalidation paths, not the full USER-allowed sequence,
APOP/SASL, mailbox locking or transport-window behavior. POP3-CORE-02 now
additionally covers empty-line refusal and pending-USER invalidation; password
spaces remain password data. Exact error prose is library behavior, not an RFC
requirement.

## Validation and boundaries

Run `go run ./internal/checklist pkg/pop3/rfctest` from the module root. The
checker and its generic negative tests are copied unchanged from go-spf commit
`7d291c08d310b9c62afc287e75a534a39150fafb` (`internal/checklist`). The checker
validates IDs and bidirectional named test mappings; it does not interpret RFC
text or test assertions. `pkg/pop3/rfctest` is a deliberately scoped package of
real wire tests using the public Session API, an in-memory backend and net.Pipe.
Every test in that package must map to this checklist. Existing server and helper tests and
fuzz targets remain outside this mapping; they are still run by CI.
`internal/checklist` also tests mutations of this checklist and its annotations:
missing mappings, unknown IDs and duplicate IDs must fail.
The exact current inventory is **204 rows: 155 mapped / 49 n/a, with 99 named wire tests**.
Example negative-control inputs replace
`TestRFC1939AdaptedTransactionExample` in its row with `TestMissingSessionVector`,
or remove the `POP3-EXAMPLE-01` row while retaining the test annotation; both must
exit nonzero. The scoped mutation test also removes its annotation and empties
its row mapping. UIDL-session controls additionally remove each new test, row
and annotation, and empty each new row mapping; every mutation must fail.
Immediate-PASS controls also remove its test, row and annotation and empty its
row mapping; every mutation must fail. Autologout controls do the same for
POP3-IDLE-01, POP3-IDLE-03, POP3-IDLE-04, POP3-IDLE-05, POP3-IDLE-06,
POP3-IDLE-07 and POP3-IDLE-08.
Generic checker source/tests remain pinned and unchanged.

Immediate-PASS regression validation replaces only `pkg/pop3/server/session.go` with baseline
`fe05ed851f2765eb26af5568248892fd2d28fddc`: all 16 sequential/pipelined cases
fail because stale PASS authenticates. Restoring the implementation makes all
16 pass. The checker CLI validates the current inventory, and the generic and
scoped mutation tests reject broken mappings.

Autologout validation temporarily edits only `pkg/pop3/server/session.go`, restoring it
byte-for-byte (SHA-256 checked) after each control: a 9m59s deadline fails both
idle tests on the ten-minute bound; a single deadline set before the command loop
fails on zero renewals; deleting marked messages after the read error fails on
backend Delete calls; and an -ERR before closing fails on the unexpected
response. Restored, both pass. This slice changes no runtime code.

The partial-command proof (POP3-IDLE-04) runs the same harness with `QUIT` and
`QUIT\r` (no LF) sent after two DELE marks. net.Pipe completes a Write only when
the server's Read has taken the bytes, and the next Read entry signal proves the
session is waiting for the missing LF, so the partial line is buffered with no
sleep. The underlying read deadline is then moved into the past: the session must
end its Read with `os.ErrDeadlineExceeded`, the client sees EOF with no response
byte, the backend gets zero Delete calls and the messages are unchanged. Its
temporary control makes `readCommandLine` return the buffered partial line on a
read error: the partial QUIT is dispatched, UPDATE runs and the test fails;
`pkg/pop3/server/session.go` was restored byte-for-byte (SHA-256 checked). Limits: the timeout is
forced, not a real ten-minute wait; only QUIT variants are covered, not other
partial commands; STLS and pre-login partial commands remain untested.

The pre-login proof (POP3-IDLE-05) uses the same harness with a counting backend.
Greeting-idle and pending-USER states are each reached by the next-Read signal
(no sleeps; no PASS is ever sent). The underlying read deadline is then moved into
the past: the session must end its Read with `os.ErrDeadlineExceeded`
(`net.Error.Timeout()` true), the client sees EOF with no response byte, `Handle`
returns, and backend Authenticate (the only route to a mailbox) was called zero
times. A complete USER before the timeout is independent of the partial-QUIT
cases. Temporary controls on `pkg/pop3/server/session.go` (restored byte-for-byte, SHA-256
checked, no runtime file committed): writing `-ERR` on a read error fails both
cases on the unexpected response byte; calling `backend.Authenticate` on a read
error fails both on the Authenticate count. Limits: the timeout is forced, not a
real ten-minute wait; plaintext only, with no TLS/STLS claim (expiry after STLS
remains untested); buffered partial pre-login commands are covered separately below.

The buffered pre-login proof (POP3-IDLE-06) sends incomplete `USER alice` or
`PASS secret` lines, each with either no terminator or CR without LF. PASS cases
first receive a positive reply to a complete USER. The next-Read signal proves
that the partial bytes were buffered before forcing the underlying deadline
into the past. Each case checks a genuine timeout, EOF without a response,
joined Handle completion and zero Authenticate calls before and after timeout.
Temporary negative controls (restored byte-for-byte) dispatch the buffered line
on read error, producing an unexpected reply in all four cases, or call
Authenticate on read error, producing a nonzero backend count in all four cases.
This closes the selected plaintext partial-USER/PASS gap, not arbitrary partial
commands, STLS expiry, or a real ten-minute elapsed-time proof.

The STLS proof (POP3-IDLE-07) records deadlines on the raw `net.Pipe` end below
the `tls.Conn` that STLS installs, which forwards SetDeadline to it. A TLS
session reads several times per record and during the handshake, so each Read
entry reports how many deadlines had been set; the first Read under a new
deadline marks the session waiting for its next command. Three cases run over
TLS 1.3 with a temporary self-signed certificate: idle right after the
handshake, after an encrypted USER, and in TRANSACTION after two DELE marks.
Each deadline is at least ten minutes after both the server's final write
(handshake or encrypted reply) and the client's send time. Forcing the raw
pipe's read deadline into the past yields `os.ErrDeadlineExceeded` below TLS;
the client then reads EOF from close_notify with no POP3 byte, `Handle` returns,
Authenticate ran only for a PASS actually sent, and no Delete happened.
Temporary controls on `pkg/pop3/server/session.go` (restored, SHA-256 checked): skipping the
renewal under TLS fails all three cases waiting for the renewed deadline;
writing `-ERR` on a TLS read error fails all three on the response byte;
running QUIT handling on a TLS read error with a mailbox fails the TRANSACTION
case on its `+OK`. Limits: the timeout is forced; partial commands after STLS
are covered separately below.

The buffered STLS proof (POP3-IDLE-08) reuses that harness for six cases over
TLS 1.3: encrypted `USER alice` right after the handshake, `PASS secret` after an
encrypted USER, and `QUIT` in TRANSACTION after two DELE marks, each with no
terminator and with CR but no LF. The client's raw pipe end counts the bytes it
writes below TLS, and the server's raw end records how many bytes it had
consumed when each Read entered. A Read entered after every written byte was
consumed, with no further deadline renewal, proves the decrypted partial line is
buffered and the session waits for its LF, with no sleep. Forcing the raw read
deadline into the past then yields `os.ErrDeadlineExceeded` below TLS; the
client reads EOF from close_notify with no POP3 byte, `Handle` returns,
Authenticate ran only for a complete PASS, and no Delete happened. Temporary
control on `pkg/pop3/server/session.go` (restored with git checkout): returning the buffered
partial line on a read error dispatches it, and all six cases fail on the `+`
response byte. Limits: the timeout is forced; only USER, PASS and QUIT partials
are covered.

Follow-ups under [rest-mail/rest-mail-server#374](https://github.com/rest-mail/rest-mail-server/issues/374)
include the remaining AUTHORIZATION inventory (including full USER command
sequencing and mailbox locking/backend contracts), remaining UPDATE integration
contracts, remaining RFC 1939 §3 autologout cases (other partial commands and
transport variants), remaining TOP/UIDL vectors and production cross-session UID persistence,
crash recovery and maildrop lifetime/global UID generation/non-reuse,
remaining TLS/STLS vectors and configuration variants, AUTH/APOP/SASL,
remaining PIPELINING vectors (including other AUTHORIZATION sequences, TLS
and transport-window handling), remaining RESP-CODES semantics (including
hierarchical/unknown-code client handling, production maildrop-lock
integration and transaction-command system codes), the full RFC 3206 credential-error
contract for AUTH/APOP/SASL, other extension requirements and CAPA configuration variations,
remaining RFC 2449 command-length/grammar and capability-specific maximum proofs,
complete RFC vectors and differential testing,
overall library coverage ≥95% toward 100%, coverage rationale and badges, and
adoption of the shared reusable workflow. Keep library/server coverage separate
from checker/tool coverage. Existing parser fuzzing, octet-count and log-redaction
work is prior work, not newly claimed here. This partial proof does not close
#374 or the all-principals acceptance work in #376.

## RFC 5034 and RFC 4616: AUTH with SASL PLAIN

AUTH is opt-in: only a Backend that implements `SASLBackend` gets it, and the
only mechanism is PLAIN. For any other Backend, AUTH stays an unknown command
(POP3-COMMAND-01) and CAPA has no SASL line. These Session/net.Pipe tests use
memory backends; the TLS rows use the STLS fixture's temporary certificate.
The reply text after the status indicator is the library's current output, not
an RFC mandate. The library offers no other mechanism, so RFC 5034 §4's rules for
server challenges with data, security layers and the success data of
`+OK` do not apply to PLAIN and have no rows here.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-SASL-01 | RFC 5034 §3; RFC 2449 §6.3 | Normative | List `SASL PLAIN` in CAPA in both states only while AUTH PLAIN is accepted: never for a Backend without SASL, and with a TLS config only after STLS. | TestSASLCapability |
| POP3-SASL-02 | RFC 5034 §4 | Normative | `AUTH PLAIN <initial-response>` authenticates in one step; `AUTH PLAIN` with no initial response gets an empty `+ ` challenge and reads the response on the next line. Keyword and mechanism name are case-insensitive. | TestSASLPlainExchange |
| POP3-SASL-03 | RFC 5034 §4; RFC 4616 §2 | Normative | Decode `[authzid] NUL authcid NUL passwd`, pass all three to the Backend, and enter TRANSACTION on `+OK`; AUTH is refused once authenticated. | TestSASLPlainExchange |
| POP3-SASL-04 | RFC 5034 §4 | MUST | A `*` response cancels the exchange with `-ERR`; a response that does not base64-decode rejects the command. Neither reaches the Backend, and the session stays in AUTHORIZATION. | TestSASLPlainRejections |
| POP3-SASL-05 | RFC 5034 §4; RFC 4616 §2 | Normative | Refuse an unknown or missing mechanism, an empty initial response (`=`), and a PLAIN message without exactly three fields, with an empty authcid or password, or that is not UTF-8, without calling the Backend; a fresh AUTH can then succeed. | TestSASLPlainRejections |
| POP3-SASL-06 | RFC 3206 §5; RFC 5034 §4 | Normative | A credential failure, including an authzid the Backend refuses, replies `-ERR [AUTH]`, is recorded with the Limiter like a failed PASS, and the failure that earns a ban replies `-ERR [AUTH] Too many authentication failures` and closes the session. | TestSASLPlainCredentialFailures |
| POP3-SASL-07 | RFC 5034 §6; RFC 4616 §5; RFC 2595 §2.2 | Normative | With a TLS config, refuse AUTH before STLS like USER (`-ERR TLS required`, no Backend call, with or without initial response) so PLAIN never carries a password in the clear; after STLS it succeeds. | TestSASLPlainRequiresTLS |
| POP3-SASL-POLICY-01 | Library policy (pkg/pop3/server/session.go AUTH response line) | Normative | The response line after the `+ ` challenge is capped at the largest PLAIN response, 1026 octets with CRLF (POP3-SASL-08): an unterminated over-long one replies `-ERR Command line too long` and closes, and a disconnect at the challenge ends the session; neither calls the Backend. | TestSASLPlainResponseLine |

## RFC 6856: UTF8 and the remaining optional authentication commands

UTF8 is opt-in: only a Backend that implements `UTF8Backend` announces it and
accepts the command. The engine records the mode and reports it to
`AuthenticateUTF8`; it never rewrites message content, so the content duties
below fall to the host. These Session/net.Pipe tests use a memory backend that
records each reported mode; the TLS row uses the STLS fixture's temporary
certificate. The n/a rows marked "known gap" are unmet duties, not met ones.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-UTF8-01 | RFC 6856 §2; RFC 2449 §5 | Normative | Announce the `UTF8` tag in both states only for a UTF8Backend, without the USER argument (the engine does not apply SASLprep to USER/PASS); a Backend without the extension never announces it. | TestUTF8CapabilityBothStates |
| POP3-UTF8-02 | RFC 6856 §§2, 2.1 | Normative | The UTF8 command takes no parameters and is valid only in AUTHORIZATION: `UTF8 <arg>` and UTF8 after login get `-ERR` and leave the mode unchanged; without UTF8Backend it is an unknown command in both states. | TestUTF8CommandStates |
| POP3-UTF8-03 | RFC 6856 §2.1 | Normative | A successful UTF8 enables UTF-8 mode for the session, and every following login reports it to the backend (utf8=true), including after a failed PASS; a session that never issued UTF8, or whose UTF8 was refused, reports utf8=false. | TestUTF8CommandStates |
| POP3-UTF8-04 | RFC 6856 §2.1; RFC 2595 §4 | MAY | Clients must not issue STLS after UTF8 and servers MAY refuse it. This library accepts STLS but discards UTF-8 mode with it: the login after TLS reports utf8=false unless the client issues UTF8 again over TLS. | TestUTF8ModeResetByTLS |
| POP3-UTF8-05 | RFC 6856 §§2.1, 4 | MUST | Outside UTF-8 mode, never send non-ASCII strings as is: send a surrogate message or fail the RETR/TOP with `-ERR`. | n/a: host duty. Message bytes come unchanged from Mailbox.Retrieve; the engine reports the mode to AuthenticateUTF8 (POP3-UTF8-03) so the backend can choose the form. |
| POP3-UTF8-06 | RFC 6856 §2.1 | SHOULD | The LIST size of a message matches the octets RETR sends, not counting byte-stuffing. | n/a: host duty. LIST reports the Message.Size the Mailbox supplies for the form it will serve. |
| POP3-UTF8-07 | RFC 6856 §2.2 | SHOULD | A server announcing `UTF8 USER` applies SASLprep to USER/PASS arguments and rejects prohibited characters. | n/a: the USER argument is not announced (POP3-UTF8-01), so this conditional duty does not apply. |
| POP3-UTF8-08 | RFC 6856 §2.2 | MUST | Reject UTF-8 usernames or passwords that are not well-formed UTF-8 (RFC 3629). | n/a: known gap. USER/PASS arguments reach the Backend unchecked. Rejecting them in the engine would also refuse 8-bit legacy credentials, which is an owner decision. AUTH PLAIN already rejects non-UTF-8 (POP3-SASL-05). Planned follow-up: [#86](https://github.com/rest-mail/go-pop3/issues/86). |
| POP3-UTF8-09 | RFC 6856 §3 | Normative | The optional LANG capability and command select the language of human-readable response text. | n/a: LANG is not implemented or announced; the command is unknown (POP3-COMMAND-01). |
| POP3-UTF8-10 | RFC 6856 §5 | Normative | A LIST/TOP/RETR refused because the message needs UTF-8 mode carries the `[UTF8]` response code. A Retrieve returning ErrUTF8Required (direct or wrapped) makes RETR and TOP reply `-ERR [UTF8]` and mark nothing seen, when the Backend announces UTF8 and the session is not in UTF-8 mode; otherwise, and for other errors, the reply stays plain. LIST is never refused. | TestUTF8ResponseCode |
| POP3-APOP-01 | RFC 1939 §7 (APOP); RFC 6856 §2.2 | Normative | The optional APOP command authenticates with an MD5 digest of the greeting timestamp and a shared secret; with UTF-8 credentials the digest uses SASLprep. | n/a: APOP is not implemented and the greeting carries no timestamp; APOP gets `-ERR` in both states (POP3-COMMAND-02). |

Negative control: with the `s.utf8 = false` reset after STLS removed from
`pkg/pop3/server/session.go`, TestUTF8ModeResetByTLS fails because the login after TLS reports
utf8=true.
With `retrieveFailed` in `pkg/pop3/server/session.go` always sending the plain reply,
TestUTF8ResponseCode fails its direct and wrapped cases.

Every RFC this library implements (1939, 2449, 2595, 3206, 4616, 5034, 6856
and 8314) has had a line-by-line pass; the dated audit records follow.

## RFC 1939: line-by-line audit additions

**Audit record (2026-10-22).** RFC 1939 was read line by line, §§1–13, for
every requirement: the upper-case MUST/MAY keywords and the lower-case
"must", "should", "required", "may NOT", "never" and "In no case" that the RFC
uses in their place (it predates RFC 2119). Each one was matched to a row above
or added below. A lower-case word is recorded at the level of the
matching upper-case keyword. Already covered: §3 unrecognized/invalid commands
(POP3-COMMAND-01/02, POP3-CORE-02), the §3 autologout timer (POP3-IDLE-*), the
§4 greeting, lock and open-failure rules (POP3-AUTH-01/03, POP3-LOCK-*), the §5
drop/scan listing formats and deletion marks (POP3-STAT-*, POP3-LIST-*,
POP3-RETR-*, POP3-DELE-01), §6 UPDATE (POP3-UPDATE-*), §7 TOP/UIDL/USER/PASS
(POP3-TOP-*, POP3-UIDL-*, POP3-USER-*, POP3-PASS-*), APOP (POP3-APOP-01) and the
§10 example (POP3-EXAMPLE-01). §3's command syntax (printable ASCII, single
SPACE separators, 40-octet arguments) and 512-octet responses carry no
requirement keyword; the response limit is asserted on every line the wire
helper reads, and argument length is POP3-LENGTH-*. Client-only duties (§3
multi-line parsing, §7 "the client must first issue USER") are not server
rows. The rows below are what the pass found missing. RFC 1939 has no
requirement left without a row; that does not make the other RFCs complete.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-STATUS-01 | RFC 1939 §3 | MUST | Send `+OK` and `-ERR` in upper case on every reply: greeting, positive and negative replies in both states, multi-line first lines and QUIT. | TestRFC1939StatusIndicatorCase |
| POP3-STATUS-02 | RFC 1939 §5 (STAT, LIST) | SHOULD | Minimal implementations end the drop listing and each scan listing with CRLF right after the size: `+OK 2 59`, `+OK 2 17`, `1 42`. | TestRFC1939StatusIndicatorCase |
| POP3-STATE-01 | RFC 1939 §3 | MUST | Reply negatively to a command issued in the wrong state: every TRANSACTION command (with and without arguments) at the greeting and after an accepted USER, and USER/PASS after login. No backend call; login, marks and the later QUIT update are unaffected. | TestRFC1939WrongStateMatrix |
| POP3-CORE-03 | RFC 1939 §7 | MUST | Support the minimal commands (USER, PASS, QUIT, STAT, LIST, RETR, DELE, NOOP, RSET) in every implementation. | TestCoreCommandCasePermutations |
| POP3-MECH-01 | RFC 1939 §4 | MUST | Support at least one authentication mechanism: USER/PASS works with every Backend, with no extension interface. | TestRFC1939UserNegativeThenRetry |
| POP3-USER-03 | RFC 1939 §§7, 13 (USER) | MAY | Reply `+OK` to USER even if no such mailbox exists. USER does not consult the backend, so its reply is the same for known and unknown names and gives attackers no clue; the failure comes at PASS. | TestRFC1939UserNegativeThenRetry |
| POP3-USER-04 | RFC 1939 §7 (USER) | Normative | After `-ERR` to USER the client may start a new authentication or QUIT: a later USER/PASS logs in, and QUIT replies `+OK` and closes with no backend call. | TestRFC1939UserNegativeThenRetry |
| POP3-OCTET-01 | RFC 1939 §11 | MUST NOT | Count each stored end-of-line as two octets (CRLF) and never count byte-stuffed leading dots twice. | n/a: host duty. LIST/STAT report the Message.Size the Mailbox supplies; the exported OctetCount helper does the §11 count and is pinned by the pkg/pop3/server/octetsize_test.go, outside this package's scope. |
| POP3-UIDL-07 | RFC 1939 §7 (UIDL) | SHOULD | Never reuse a unique-id in a maildrop while the entity using it exists. | n/a: host duty. UIDs come unchanged from the Backend; the engine keeps no UID history (persistence within the engine is POP3-UIDL-05/06). |
| POP3-UIDL-08 | RFC 1939 §7 (UIDL) | SHOULD | Clients handle two identical messages that share a hash-derived unique-id. | n/a: client duty. The server passes duplicate Backend UIDs through unchanged. |
| POP3-APOP-02 | RFC 1939 §7 (APOP) | MUST | The APOP greeting timestamp differs on every greeting; take care with the shared secret. | n/a: APOP is not implemented and the greeting has no timestamp (POP3-APOP-01). |
| POP3-APOP-03 | RFC 1939 §13 | SHOULD | A server with both PASS and APOP allows only one of them per mailbox. | n/a: APOP is not implemented. |
| POP3-SITE-01 | RFC 1939 §8 | SHOULD | Operators tell users about quotas and retention policy; clients must not rely on site deletion; download-and-delete sites never delete without QUIT. | n/a: operator and client duties. The engine part, no removal without QUIT, is POP3-UPDATE-05. |
| POP3-SEC-01 | RFC 1939 §13 | Normative | PASS, RETR and TOP send passwords and mail in the clear. | n/a: no requirement. Encryption is RFC 2595 STLS (POP3-STLS-*) and RFC 8314 implicit TLS (POP3-ITLS-*). |

## RFC 2449: line-by-line audit additions

**Audit record (2026-10-22).** RFC 2449 was read line by line, §§1–10, for
every RFC 2119 keyword (MUST, MUST NOT, SHOULD, MAY; it uses no SHALL or
RECOMMENDED). Each one was matched to a row above or added below. Already
covered: the §4 255-octet command length and 512-octet first line
(POP3-LENGTH-01/02), §5 CAPA in both states and its grammar (POP3-CAPA-01/02/03/11),
§6.1–6.9 per-capability text (POP3-CAPA-04 to 10, POP3-SASL-01), §6.6
PIPELINING processing in turn (POP3-PIPELINING-02 to 07), and §8 response codes
(POP3-RESP-CODES-*). The §3 grammar's APOP timestamp ("MUST conform to RFC-822
msg-id") is POP3-APOP-02. The rows below are what the pass found missing. No
new wire test was needed: the two mapped rows are asserted by existing tests,
which now name them.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-CAPA-12 | RFC 2449 §5 | MUST | Announce every AUTHORIZATION capability in TRANSACTION too, in every configuration: with and without STLS, LOGIN-DELAY, UTF8 and SASL. Only LOGIN-DELAY's argument may change after login. | TestCapaConfiguredGrammar, TestSASLCapability |
| POP3-LDELAY-03 | RFC 2449 §6.5 | SHOULD | When the delay varies per user, append `USER` to LOGIN-DELAY in AUTHORIZATION and announce the user's own value in TRANSACTION. | TestCapaLoginDelay |
| POP3-LDELAY-01 | RFC 2449 §6.5 | SHOULD | A server that advertises LOGIN-DELAY enforces it. | n/a: host duty. The Backend refuses an early login with ErrLoginDelay; the engine's reply is POP3-RESP-CODES-06. |
| POP3-LDELAY-02 | RFC 2449 §6.5 | MUST | When the delay varies per user, announce in AUTHORIZATION the largest value that could apply to any user. | n/a: host duty. The value comes from LoginDelayBackend.LoginDelay and is announced unchanged (POP3-CAPA-05). |
| POP3-LENGTH-03 | RFC 2449 §4; RFC 5034 §4 | MUST | Support the largest command length that any supported capability specifies. Re-checked against RFC 5034 §4 on 2026-10-22: AUTH with an initial response keeps the 255-octet command limit (POP3-LENGTH-01), but a response line after the `+ ` challenge may be as long as the mechanism needs, so the largest PLAIN response (1026 octets with CRLF) is accepted. | TestCommandLength255Credentials, TestRFC5034LargestPlainResponse |
| POP3-CAPA-13 | RFC 2449 §5 | MUST | A capability description documents the states it is announced in, where its commands are valid, and whether its argument can change after login. | n/a: duty on capability specifications, not on servers. |
| POP3-CAPA-14 | RFC 2449 §5 | SHOULD | A capability tag for a command is identical to the command keyword. | n/a: duty on capability specifications. The tags this library announces (TOP, UIDL, USER, STLS, UTF8) already match their keywords. |
| POP3-EXPIRE-01 | RFC 2449 §6.7 | MUST | When EXPIRE varies per user, announce in AUTHORIZATION the smallest value that could apply, append `USER`, and announce the user's own value in TRANSACTION. | n/a: EXPIRE is not announced (POP3-CAPA-09). Planned follow-up: [#85](https://github.com/rest-mail/go-pop3/issues/85). |
| POP3-EXPIRE-02 | RFC 2449 §6.7 | SHOULD | A site with an automatic deletion policy announces it with EXPIRE; with EXPIRE 0 clients MAY assume an implicit DELE for each downloaded message. | n/a: host retention policy has no Backend API, and EXPIRE is not announced (POP3-CAPA-09). Planned follow-up: [#85](https://github.com/rest-mail/go-pop3/issues/85). |
| POP3-CLIENT-01 | RFC 2449 §§5, 6.5, 6.6, 6.9, 7, 8 | MUST | Client duties: reissue CAPA after an integrity layer, use LOGIN-DELAY for check intervals, track pipelined commands and stay within the TCP window, never change behavior on IMPLEMENTATION, never require an extension for basic function, and ignore unknown response-code detail. | n/a: client duties. This library is a server only. |
| POP3-IANA-01 | RFC 2449 §9 | MUST | New capabilities and response codes are defined in an RFC with the listed details; new capability names never begin with `X`. | n/a: registration duty. The library announces only registered capabilities and codes (exact lists in POP3-CAPA-11 and POP3-RESP-CODES-*). |

## RFC 2595: line-by-line audit additions

**Audit record (2026-10-22).** RFC 2595 was read line by line, §§1–10 and
Appendix A, for every RFC 2119 keyword (REQUIRED, MUST, MUST NOT, SHOULD, MAY).
Each one was matched to a row above or added below. Already covered: §4 STLS
arguments, state, the handshake after the `+OK` CRLF, staying in AUTHORIZATION
with client certificates, the permitted `-ERR` when TLS is active, and
different capabilities after STLS (POP3-STLS-01 to 08); the §4 client cache
duty (POP3-STLS-09). §3 (IMAP) and §5 (ACAP) do not apply to POP3. §6 PLAIN is
obsoleted by RFC 4616 and is recorded once below; RFC 8314 updates the §2.1
cipher mandate. The rows below are what the pass found missing. No code bug
was found in RFC 2595 itself.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-STLS-10 | RFC 2595 §2.1 | REQUIRED | Implement the TLS_DHE_DSS_WITH_3DES_EDE_CBC_SHA cipher suite. | n/a: obsolete. Cipher suites come from the host's `tls.Config` and Go's crypto/tls, which has no DSS suites; RFC 8314 updates RFC 2595 and its §4 sets the current TLS requirements. |
| POP3-STLS-11 | RFC 2595 §2.2 | SHOULD | Offer a privacy mode that refuses authentication until encryption is active: with a TLS config, USER and AUTH PLAIN are refused before STLS with no Backend call, and succeed after it. TLS cannot be turned off once started, so the "terminate if deactivated" case cannot arise. | TestSTLSCredentials, TestSASLPlainRequiresTLS |
| POP3-STLS-12 | RFC 2595 §2.2 | SHOULD | Offer a backward-compatible mode needing only the base protocol's mechanisms: with no TLS config, plaintext USER/PASS logs in. | TestAuthorizationUserPass |
| POP3-STLS-13 | RFC 2595 §2.3 | MUST | Be configurable to refuse every clear-text login command and mechanism unless encryption is active: a TLS config refuses USER (so PASS has nothing to complete) and AUTH PLAIN with or without an initial response. APOP is not implemented. | TestSTLSCredentials, TestSASLPlainRequiresTLS |
| POP3-STLS-14 | RFC 2595 §2.3 | SHOULD | A server that allows clear-text logins can refuse them for the whole server and per user. | n/a: known gap. The whole-server switch is the TLS config (POP3-STLS-13). Per user needs the Backend to know whether the session is encrypted, which `Authenticate` is not told; adding that is an API decision for the owner. Planned follow-up: [#87](https://github.com/rest-mail/go-pop3/issues/87). |
| POP3-STLS-15 | RFC 2595 §2.5 | MUST | The server checks the TLS negotiation result: a failed negotiation closes without authenticating. Which versions, suites and client certificates are acceptable is host policy in the `tls.Config`. | TestSTLSHandshakeFailure |
| POP3-STLS-16 | RFC 2595 §4 | MUST | A server with STLS also implements the RFC 2449 extension mechanism, so STLS is announced through CAPA in AUTHORIZATION. | TestCapaBothStates, TestSTLSCredentials |
| POP3-STLS-17 | RFC 2595 §4 | MAY | AUTH EXTERNAL authenticates with TLS client credentials. | n/a: EXTERNAL is not implemented and RFC 2595 does not require it; a client certificate grants nothing on its own (POP3-STLS-08). |
| POP3-STLS-18 | RFC 2595 §6; RFC 4616 §2 | MUST | PLAIN is not announced or used without a strong encryption layer unless backward compatibility needs it, and each PLAIN field is accepted up to 255 octets. With a TLS config SASL PLAIN appears only after STLS; a server built without TLS is the backward-compatible case. | TestSASLCapability, TestSASLPlainRequiresTLS, TestRFC5034LargestPlainResponse |
| POP3-STLS-19 | RFC 2595 §§2.2, 2.4, 2.5, 4, 7 | MUST | Client duties: check the server identity against the hostname, never use an insecurely looked-up name, warn or stop on mismatch, send nothing after STLS until TLS is up, and discard cached capabilities. | n/a: client duties. This library is a server only; the server side of pipelined commands after STLS is POP3-STLS-07. |

## RFC 5034: line-by-line audit additions

**Audit record (2026-10-22).** RFC 5034 was read line by line, §§1–9, for every
RFC 2119 keyword. Each one was matched to a row above or added below. Already
covered: the §3 SASL capability (POP3-SASL-01, POP3-CAPA-08), §4 one-step and
challenge exchanges (POP3-SASL-02/03), the `*` cancel and undecodable response
(POP3-SASL-04), unknown mechanisms (POP3-SASL-05), credential failures
(POP3-SASL-06) and refusing PLAIN before STLS (POP3-SASL-07). The pass found
**two bugs, now fixed**: the response line after the `+ ` challenge was capped
at 1024 octets, so the largest PLAIN response RFC 4616 requires (1024 base64
characters plus CRLF) was refused (POP3-SASL-08); and Go's `encoding/base64`
skips CR and LF, so a response with a bare CR inside it was decoded instead of
rejected (POP3-SASL-09). Negative control: with `pkg/pop3/server/session.go` from before the
fix, the largest-response case times out and both bare-CR cases authenticate.
POP3-LENGTH-03 was re-checked against §4's line-length text and is now mapped.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-SASL-08 | RFC 5034 §4; RFC 4616 §2 | MUST | Handle the largest response a supported mechanism generates, whatever the command-line limit: PLAIN with 255-octet authzid, authcid and password (1024 base64 characters) after the `+ ` challenge reaches the Backend intact and logs in; one octet more replies `-ERR Command line too long` and closes with no Backend call. | TestRFC5034LargestPlainResponse |
| POP3-SASL-09 | RFC 5034 §4 | MUST | Reject, not ignore, any character outside the base64 alphabet and any pad before the end: `=AAA`, `AAA=BBBB`, missing padding, a space, a bare CR (in the challenge response and the initial response) and the URL-safe alphabet all reply `-ERR Invalid base64 response` with no Backend call, and a fresh AUTH succeeds. | TestRFC5034Base64Strictness |
| POP3-SASL-10 | RFC 5034 §3 | MUST | Keep announcing SASL after a successful AUTH, though no further AUTH is allowed. | TestSASLCapability |
| POP3-SASL-11 | RFC 5034 §4 | MUST NOT | The empty challenge is exactly `+ ` with no other data. | TestSASLPlainExchange |
| POP3-SASL-12 | RFC 5034 §4 | MUST | Reject an unsupported mechanism, a cancelled exchange and a failed authentication with `-ERR`, and behave as if AUTH had not been issued: the session stays in AUTHORIZATION and a fresh AUTH logs in. | TestSASLPlainRejections, TestSASLPlainCredentialFailures |
| POP3-SASL-13 | RFC 5034 §4 | MUST | Reject an initial response sent with a mechanism where the server speaks first. | n/a: the only mechanism, PLAIN, has the client speak first, so an initial response is always valid. |
| POP3-SASL-14 | RFC 5034 §§4, 7 | SHOULD | Prepare the authorization identity with SASLprep, and fail the authentication if preparation fails or gives an empty string. | n/a: by design, credential preparation and authorization policy belong to the Backend. The engine checks PLAIN UTF-8 (POP3-SASL-05) and passes authzid unchanged; the host must implement the RFC 5034 preparation recommendation itself. The engine has no SASLprep tables. |
| POP3-SASL-15 | RFC 5034 §4 | MUST | When a SASL security layer takes effect, discard earlier client knowledge and apply TLS after the SASL encoding. | n/a: PLAIN negotiates no security layer. |
| POP3-SASL-16 | RFC 5034 §4 | MUST | Implement PLAIN over TLS. | TestSASLPlainRequiresTLS |
| POP3-SASL-17 | RFC 5034 §4 | MUST | Offer a configuration that neither announces nor permits a plaintext password mechanism before STLS; it SHOULD be the default. Any TLS config gives this behaviour, with no opt-out. | TestSASLCapability, TestSASLPlainRequiresTLS |
| POP3-SASL-18 | RFC 5034 §4 | SHOULD | Implement further mechanisms that send no plaintext password, such as GSSAPI. | n/a: not implemented; PLAIN is the only mechanism. |
| POP3-SASL-19 | RFC 5034 §5 | MUST | Accept the AUTH keyword and mechanism name in any case. | TestSASLPlainExchange |
| POP3-SASL-20 | RFC 5034 §§3, 4 | MUST | Client duties: no initial response without the SASL capability, no initial response that makes AUTH exceed 255 octets, `=` for an empty initial response, `*` for an undecodable challenge, discard capabilities after a security layer, verify the server certificate before PLAIN. | n/a: client duties. This library is a server only. |

## RFC 6856: line-by-line audit additions

**Audit record (2026-10-22).** RFC 6856 was read line by line, §§1–8, for every
RFC 2119 keyword. Each one was matched to a row above or added below. Already
covered: the §2 capability (POP3-UTF8-01), §2.1 command and mode
(POP3-UTF8-02/03), STLS after UTF8 (POP3-UTF8-04), non-ASCII outside UTF-8
mode (POP3-UTF8-05), LIST sizes (POP3-UTF8-06), §2.2 SASLprep and RFC 3629
checks (POP3-UTF8-07/08), §3 LANG (POP3-UTF8-09), §§4–5 the `[UTF8]` code
(POP3-UTF8-10) and APOP (POP3-APOP-01). UTF-8 in AUTH is governed by RFC 5034
(POP3-SASL-05). No code bug was found; POP3-UTF8-08 and POP3-UTF8-10 stay
known gaps.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-UTF8-11 | RFC 6856 §2.1 | MAY | The server may answer the UTF8 command with `-ERR`: without a UTF8Backend it does, in both states, and UTF8 mode stays off. | TestUTF8CommandStates |
| POP3-UTF8-12 | RFC 6856 §2.1 | SHOULD | In UTF-8 mode, convert a maildrop stored in another charset to UTF-8 before sending it; binary content-transfer-encoding stays forbidden. | n/a: host duty. Message bytes come unchanged from Mailbox.Retrieve, and the Backend knows the mode (POP3-UTF8-03). |
| POP3-UTF8-13 | RFC 6856 §§2.1, 2.2 | MUST | Client duties: no STLS after UTF8, and no UTF-8 credentials unless the UTF8 capability has the USER argument. | n/a: client duties. The server side of STLS after UTF8 is POP3-UTF8-04. |

## RFC 3206: line-by-line audit additions

**Audit record (2026-10-22).** RFC 3206 was read line by line, §§1–9, for
every RFC 2119 keyword (it uses SHOULD and RECOMMENDED only) and for the
normative definitions of SYS and AUTH. Each one was matched to a row above or
added below. Already covered: §4 SYS/TEMP and SYS/PERM, only with `-ERR`
(POP3-RESP-CODES-04/07, POP3-RESP-POLICY-03); §5 AUTH on credential failures
of PASS and AUTH (POP3-RESP-CODES-02, POP3-SASL-06); the §5 SHOULDs to support
CAPA and announce AUTH-RESP-CODE, and the §6 capability with no arguments in
both states (POP3-RESP-CODES-03). §3 restates the RFC 2449 IN-USE and
LOGIN-DELAY codes (POP3-RESP-CODES-05/06). §7 is IANA registration done by the
RFC itself. No code bug was found.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-RESP-CODES-09 | RFC 3206 §§5, 8 | RECOMMENDED | Never send the AUTH code to USER, which would reveal that the user exists: USER replies `+OK` to known and unknown names, and its refusals (no argument, TLS required, already authenticated) carry no code. | TestRFC3206NoAuthCodeOnUser |
| POP3-RESP-CODES-10 | RFC 3206 §§5, 6 | Normative | AUTH-RESP-CODE promises that only AUTH-coded errors are credential problems: AUTH is on every credential-caused PASS and AUTH failure, and never on system, USER or TLS-required refusals. | TestResponseCodeAuthRespCodeCapability, TestSASLPlainCredentialFailures, TestResponseCodeBackendSystemFailures, TestRFC3206NoAuthCodeOnUser |
| POP3-RESP-CODES-11 | RFC 3206 §§3–5 | Normative | Client duties: compare response codes case-insensitively, and ask for new credentials only on AUTH when AUTH-RESP-CODE is announced. | n/a: client duties. This library is a server only. |

## RFC 4616: line-by-line audit additions

**Audit record (2026-10-22).** RFC 4616 was read line by line, §§1–6, for every
RFC 2119 keyword (MUST, MUST NOT, SHALL, SHOULD, SHOULD NOT, RECOMMENDED,
MAY). Each one was matched to a row above or added below. Already covered: the
§2 message format and an empty authcid or password (POP3-SASL-03/05), 255-octet
ASCII fields (POP3-SASL-08, POP3-STLS-18), refusing PLAIN before STLS
(POP3-SASL-07). §3 pseudo-code and §4 IMAP examples are not normative. The
rows below are what the pass found missing. New wire tests prove the 255-octet
limit counts octets of multi-byte UTF-8, and that each field outside the ABNF
is refused. No code bug was found: `utf8.Valid` already rejects overlong forms,
surrogates and code points above U+10FFFF.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-PLAIN-01 | RFC 4616 §2 | MUST | Accept authzid, authcid and passwd up to and including 255 octets, counted in octets: fields of 2-, 3- and 4-octet UTF-8 characters filled to 255 octets reach the Backend intact. | TestRFC4616MultibyteFieldLimits, TestRFC5034LargestPlainResponse |
| POP3-PLAIN-02 | RFC 4616 §2 | SHALL | Fields are UTF-8 (1*SAFE): an overlong form, surrogate, code point above U+10FFFF, stray continuation, truncated sequence or Latin-1 octet in any field replies `-ERR Invalid PLAIN response` with no Backend call, and a fresh AUTH succeeds. | TestRFC4616RejectsMalformedUTF8 |
| POP3-PLAIN-03 | RFC 4616 §2 | MUST NOT | NUL never appears inside a field: a password with an extra NUL is a fourth field and is refused with no Backend call. | TestRFC4616RejectsMalformedUTF8 |
| POP3-PLAIN-04 | RFC 4616 §2 | RECOMMENDED | Prepare the presented authcid and password, preferably with SASLprep, before verification. | n/a: host duty. The engine passes the fields unchanged to `AuthenticatePlain`, which verifies them; §2 lets the server choose another preparation or none. |
| POP3-PLAIN-05 | RFC 4616 §2 | MUST | When a hash of the expected string is stored, prepare the string before hashing it. | n/a: host duty. The Backend owns the credential store. |
| POP3-PLAIN-06 | RFC 4616 §2 | SHALL | Verification fails when preparation fails or gives an empty string: the engine refuses an empty authcid or password with no Backend call; failures of the Backend's own preparation are its credential failures (POP3-SASL-06). | TestSASLPlainRejections |
| POP3-PLAIN-07 | RFC 4616 §2 | Normative | With no authzid, derive the authorization identity from the prepared authcid. | n/a: host duty. An absent authzid reaches `AuthenticatePlain` as the empty string (POP3-SASL-03), and the Backend derives the identity. |
| POP3-PLAIN-08 | RFC 4616 §2 | MAY | Use the credentials to initialize another authentication database. | n/a: host duty. |
| POP3-PLAIN-09 | RFC 4616 §§1, 5 | SHOULD | By default, advertise and use PLAIN only with adequate data security: with a TLS config, SASL PLAIN is neither announced nor accepted before STLS. A server built without TLS is RFC 2595's backward-compatible case (POP3-STLS-12, POP3-STLS-18). | TestSASLCapability, TestSASLPlainRequiresTLS |
| POP3-PLAIN-10 | RFC 4616 §1 | MUST | A protocol specification that allows PLAIN mandates a strong data security service. | n/a: duty on specifications. RFC 5034 §4 does, and the server side is POP3-SASL-16. |

## RFC 8314: line-by-line audit additions

**Audit record (2026-10-22).** RFC 8314 was read line by line, §§1–8 and
Appendix A, for every RFC 2119 keyword. Each one was matched to a row above or
added below. Already covered: §3.1 handshake first, greeting and AUTHORIZATION
(POP3-ITLS-01 to 03) and §3.4 close alerts (POP3-ITLS-04). §§3.2–3.3 (IMAP,
submission) and §7 port registrations do not apply to a POP3 library. The pass
found **two bugs, now fixed** in `Server.ListenAndServe`: a `POP3TLS` port with
a nil TLS config was opened in plaintext, against §3.1's immediate handshake
(POP3-ITLS-05); and a TLS config with no certificate source announced STLS that
could only fail, against §4 (POP3-ITLS-06). Both now return an error before any
listener opens. Negative control: with `pkg/pop3/server/server.go` from before the fix, both
new tests fail. The pass also added the TLS version and cipher suite to the
STLS and login logs (POP3-ITLS-11). MSP, operator and MUA duties are listed as n/a rows.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| POP3-ITLS-05 | RFC 8314 §3.1 | Normative | The "pop3s" port begins a TLS handshake immediately, so it never listens in plaintext: a `POP3TLS` port with a nil TLS config makes ListenAndServe fail, and neither that port nor a requested plaintext port is opened. | TestRFC8314ImplicitTLSRequiresConfig |
| POP3-ITLS-06 | RFC 8314 §4 | SHOULD NOT | Do not advertise STLS when it is likely to fail, such as with no server certificate: a TLS config with no Certificates, GetCertificate or GetConfigForClient makes ListenAndServe fail on either port; each source alone is accepted. | TestRFC8314TLSConfigNeedsCertificate |
| POP3-ITLS-07 | RFC 8314 §§3.1, 4.2 | MUST | Enter AUTHORIZATION after the greeting even if a client certificate was supplied on implicit TLS: transaction commands are refused, no SASL (so no EXTERNAL) is offered, AUTH EXTERNAL is unknown, and USER/PASS logs in. | TestRFC8314ClientCertificateImplicitTLS |
| POP3-ITLS-08 | RFC 8314 §4 | MUST | Support TLS access for POP: implicit TLS and, for existing clients, STLS (both SHOULD instances). | TestImplicitTLSSession, TestSTLSCredentials |
| POP3-ITLS-09 | RFC 8314 §4 | MUST | Support TLS 1.2 or later: implicit-TLS sessions complete with TLS 1.2 and TLS 1.3. | TestImplicitTLSCloseNotify |
| POP3-ITLS-10 | RFC 8314 §§4, 4.1 | SHOULD | Use RFC 7525 cipher suites, retire SSL and TLS 1.0, and require TLS 1.1 or later for new users (RECOMMENDED). | n/a: host policy in the `tls.Config`, used unchanged. Go's crypto/tls has no SSL, and its server default minimum is TLS 1.2. |
| POP3-ITLS-11 | RFC 8314 §4 | SHOULD | Log TLS cipher information with connection and authentication logs: the STLS log and the successful and failed login logs carry `tls_version` and `tls_cipher` on STLS and implicit TLS, and a plaintext login log carries neither. The implicit-TLS "new connection" log comes before the handshake, so it has none. | TestRFC8314LogsTLSCipher |
| POP3-ITLS-12 | RFC 8314 §4.1 | MUST NOT | Once clear-text authentication is refused, never show over clear text whether credentials were valid, with the same denial for valid and invalid users: USER replies `-ERR TLS required` for any name and AUTH PLAIN is refused with no Backend call, so no password is checked. | TestRFC3206NoAuthCodeOnUser, TestSASLPlainRequiresTLS |
| POP3-ITLS-13 | RFC 8314 §4.1 | SHOULD | Users who sent passwords in clear text change them when moving to TLS. | n/a: operator duty. |
| POP3-ITLS-14 | RFC 8314 §4.2 | MUST NOT | Request a client certificate only when some certificates are accepted as sufficient for authentication. | n/a: host duty. `ClientAuth` comes from the host's `tls.Config`; the engine never changes it. |
| POP3-ITLS-15 | RFC 8314 §4.2 | MUST | A server that accepts a client certificate as sufficient enables SASL EXTERNAL. | n/a: a certificate is never sufficient (POP3-ITLS-07, POP3-STLS-08), so this conditional duty does not apply. |
| POP3-ITLS-16 | RFC 8314 §§4, 4.3–4.6 | MUST | MSP duties: advertise SRV records preferring implicit TLS, deprecate clear-text servers, maintain valid certificates, sign DNS with DNSSEC, publish TLSA only when signed, record the cipher in submission Received fields. | n/a: operator duties, outside a POP3 library. |
| POP3-ITLS-17 | RFC 8314 §§3.1, 3.4, 5 | MUST | MUA duties: validate certificates per RFC 7817, enforce a minimum confidentiality level, implement TLS 1.2, use SRV records safely, offer client certificates only on implicit TLS, implement EXTERNAL; the client MAY close TCP after its close alert. | n/a: client duties. This library is a server only. |
