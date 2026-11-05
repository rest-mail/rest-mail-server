# IMAP RFC checklist — RFC 3501 inventory, partial conformance proofs

This is a complete **RFC 3501 requirement inventory**, with the section and
grammar-disposition indexes below and explicit unproved, unsupported,
host-backend, client-only and optional-feature boundaries. It is **not** a full
conformance claim, a complete collection of wire proofs or a coverage badge.
RFC 9051 has its own section index and gap issues below, with IMAP4rev2
support still tracked in #718; other extension inventories retain their
stated scopes. A mapped row means its named
wire test in `pkg/imap/rfctest` asserts the stated behavior through the public
`server.Session`; it does not prove the entire referenced section or every
alternative of a grammar production. The engine advertises IMAP4rev1 only.

Requirements paraphrase [RFC 3501](https://www.rfc-editor.org/rfc/rfc3501.txt)
and, in the separately labelled tables, the cited extension/revision RFCs.
`Normative` denotes specified protocol behavior without an RFC2119 keyword.
The Tests cell is either an actual public-session proof or `n/a: <reason>`;
the latter includes real server requirements that remain unproved or unmet,
not only literal non-applicability. No percentage of implemented requirements
can be inferred from that overloaded historical status vocabulary.
CI runs `go run ./internal/checklist pkg/imap/rfctest`, rejecting missing tests,
unknown items, inconsistent annotations and duplicate IDs. The section/grammar
index guard detects structural omissions, not semantic correctness of prose.
The narrower [core grammar crosswalk](RFC-GRAMMAR.md) remains useful component
evidence and does not override the residual requirements below.


## RFC 3501: complete section index

Audit source: RFC Editor RFC3501 text, retrieved 2026-11-03, SHA-256
`4437974dde85859f2391f32215295e7b20ce157c3bd2adbe7e5b443e990f15cf`. The source is reference material, not vendored runtime input.
Every numbered section and appendix is listed once, including headings without
independent requirements. References ending in `-*` mean all rows in that family,
not that any one test proves the family. Read each row's actual Tests cell.

| Section | Title | Requirement rows or explicit exclusion |
|---------|-------|-----------------------------------------|
| 1 | How to Read This Document | n/a: Reading guide; substantive conventions are indexed at 1.2. |
| 1.1 | Organization of This Document | n/a: Document organization; no additional wire behavior. |
| 1.2 | Conventions Used in This Document | IMAP-SYNTAX-05; IMAP-INV-TEXT-01; IMAP-INV-ABNF-22; IMAP-INV-NAME-01 |
| 1.3 | Special Notes to Implementors | n/a: Implementor reading recommendations and historical context; no additional server requirement beyond the RFC2822 interpretation used by FETCH/ENVELOPE rows. |
| 2 | Protocol Overview | n/a: Overview heading; see every subsection below. |
| 2.1 | Link Level | IMAP-INV-LINK-01 |
| 2.2 | Commands and Responses | IMAP-GREET-01; IMAP-TAG-01; IMAP-INV-ABNF-11 |
| 2.2.1 | Client Protocol Sender and Server Protocol Receiver | IMAP-INV-CLIENT-01; IMAP-TAG-01; IMAP-RESP-17; IMAP-INV-ABNF-09 |
| 2.2.2 | Server Protocol Sender and Client Protocol Receiver | IMAP-RESP-01; IMAP-RESP-CLIENT-01; IMAP-INV-SYNTAX-01; IMAP-INV-ABNF-09; IMAP-INV-ABNF-11 |
| 2.3 | Message Attributes | n/a: Attribute overview; each attribute is indexed below. |
| 2.3.1 | Message Numbers | IMAP-INV-SEQ-01; IMAP-INV-UID-01 |
| 2.3.1.1 | Unique Identifier (UID) Message Attribute | IMAP-INV-UID-*; IMAP-SELECT-03; IMAP-SELECT-04; IMAP-SELECT-09; IMAP-STATUS-08 |
| 2.3.1.2 | Message Sequence Number Message Attribute | IMAP-INV-SEQ-01; IMAP-EXPUNGE-02 |
| 2.3.2 | Flags Message Attribute | IMAP-INV-FLAGS-*; IMAP-SELECT-08; IMAP-STORE-*; IMAP-EXPUNGE-07; IMAP-APPEND-19 |
| 2.3.3 | Internal Date Message Attribute | IMAP-INV-DATE-01; IMAP-FETCH-07; IMAP-APPEND-20; IMAP-COPY-12 |
| 2.3.4 | [RFC-2822] Size Message Attribute | IMAP-INV-SIZE-01; IMAP-FETCH-07 |
| 2.3.5 | Envelope Structure Message Attribute | IMAP-ENV-* |
| 2.3.6 | Body Structure Message Attribute | IMAP-BS-*; IMAP-MSG-* |
| 2.4 | Message Texts | IMAP-FETCH-*; IMAP-MSG-* |
| 3 | State and Flow Diagram | IMAP-GREET-01; IMAP-MBOX-AUTH-01; IMAP-INV-STATE-01; IMAP-INV-ABNF-09 |
| 3.1 | Not Authenticated State | IMAP-GREET-01; IMAP-LOGIN-01; IMAP-AUTHENTICATE-* |
| 3.2 | Authenticated State | IMAP-SELECT-06; IMAP-FETCH-13; IMAP-SEARCH-09; IMAP-CLOSE-* |
| 3.3 | Selected State | IMAP-SELECT-*; IMAP-EXAMINE-* |
| 3.4 | Logout State | IMAP-LOGOUT-01; IMAP-STATE-01; IMAP-RESP-13; IMAP-RESP-14; IMAP-INV-CLIENT-04 |
| 4 | Data Formats | IMAP-INV-ABNF-* |
| 4.1 | Atom | IMAP-INV-ABNF-02; IMAP-SYNTAX-09 |
| 4.2 | Number | IMAP-INV-ABNF-13 |
| 4.3 | String | IMAP-LITERAL-*; IMAP-APPEND-03; IMAP-SYNTAX-04; IMAP-INV-ABNF-08; IMAP-INV-ABNF-19; IMAP-INV-CLIENT-01 |
| 4.3.1 | 8-bit and Binary Strings | IMAP-INV-TEXT-01; IMAP-INV-TEXT-02; IMAP-INV-TEXT-03; IMAP-RESP-16; IMAP-APPEND-31 |
| 4.4 | Parenthesized List | IMAP-INV-ABNF-18; IMAP-INV-ABNF-19 |
| 4.5 | NIL | IMAP-NIL-01; IMAP-ENV-*; IMAP-INV-ABNF-19 |
| 5 | Operational Considerations | n/a: Operational heading; each rule is indexed below. |
| 5.1 | Mailbox Naming | IMAP-MUTF7-*; IMAP-SELECT-12; IMAP-INV-CLIENT-05; IMAP-INV-ABNF-17 |
| 5.1.1 | Mailbox Hierarchy Naming | IMAP-INV-ABNF-17; IMAP-LIST-01; IMAP-LIST-04; IMAP-INV-NAME-01 |
| 5.1.2 | Mailbox Namespace Naming Convention | IMAP-INV-LIST-04 |
| 5.1.3 | Mailbox International Naming Convention | IMAP-MUTF7-*; IMAP-INV-CLIENT-05 |
| 5.2 | Mailbox Size and Message Status Updates | IMAP-UPDATE-01; IMAP-INV-UPDATE-01; IMAP-RESP-CLIENT-01 |
| 5.3 | Response when no Command in Progress | IMAP-UPDATE-03; IMAP-RESP-12 |
| 5.4 | Autologout Timer | IMAP-AUTOLOGOUT-01 |
| 5.5 | Multiple Commands in Progress | IMAP-UPDATE-04; IMAP-INV-CLIENT-01; IMAP-INV-CLIENT-02; IMAP-RESP-12 |
| 6 | Client Commands | IMAP-INV-STATE-01; IMAP-INV-STATE-02; IMAP-INV-ABNF-09; IMAP-INV-ABNF-10 |
| 6.1 | Client Commands - Any State | IMAP-CAP-*; IMAP-NOOP-01; IMAP-LOGOUT-01; IMAP-SYNTAX-03 |
| 6.1.1 | CAPABILITY Command | IMAP-CAP-*; IMAP-INV-CAP-01; IMAP-CLIENT-01 |
| 6.1.2 | NOOP Command | IMAP-NOOP-01; IMAP-UPDATE-01; IMAP-AUTOLOGOUT-01 |
| 6.1.3 | LOGOUT Command | IMAP-LOGOUT-01; IMAP-INV-CLIENT-04 |
| 6.2 | Client Commands - Not Authenticated State | IMAP-AUTH-STATE-01; IMAP-ANON-01; IMAP-STARTTLS-*; IMAP-AUTHENTICATE-*; IMAP-LOGIN-* |
| 6.2.1 | STARTTLS Command | IMAP-STARTTLS-*; IMAP-CLIENT-01 |
| 6.2.2 | AUTHENTICATE Command | IMAP-AUTHENTICATE-*; IMAP-INV-SASL-01; IMAP-INV-ABNF-03; IMAP-INV-SEC-07 |
| 6.2.3 | LOGIN Command | IMAP-LOGIN-*; IMAP-SYNTAX-04; IMAP-CLIENT-01; IMAP-INV-SEC-07 |
| 6.3 | Client Commands - Authenticated State | IMAP-MBOX-AUTH-01; IMAP-INV-ABNF-09 |
| 6.3.1 | SELECT Command | IMAP-SELECT-*; IMAP-INV-STATE-01 |
| 6.3.2 | EXAMINE Command | IMAP-EXAMINE-*; IMAP-SELECT-* |
| 6.3.3 | CREATE Command | IMAP-CREATE-*; IMAP-INV-CREATE-* |
| 6.3.4 | DELETE Command | IMAP-DELETE-*; IMAP-INV-DELETE-01 |
| 6.3.5 | RENAME Command | IMAP-RENAME-*; IMAP-INV-RENAME-01 |
| 6.3.6 | SUBSCRIBE Command | IMAP-SUB-* |
| 6.3.7 | UNSUBSCRIBE Command | IMAP-SUB-* |
| 6.3.8 | LIST Command | IMAP-LIST-*; IMAP-INV-LIST-*; IMAP-INV-CLIENT-06 |
| 6.3.9 | LSUB Command | IMAP-LSUB-*; IMAP-SUB-02; IMAP-INV-LSUB-01 |
| 6.3.10 | STATUS Command | IMAP-STATUS-*; IMAP-CLIENT-01 |
| 6.3.11 | APPEND Command | IMAP-APPEND-*; IMAP-CLIENT-01 |
| 6.4 | Client Commands - Selected State | IMAP-INV-ABNF-09; IMAP-FETCH-13; IMAP-SEARCH-09; IMAP-STORE-03; IMAP-EXPUNGE-* |
| 6.4.1 | CHECK Command | IMAP-CHECK-*; IMAP-UPDATE-02; IMAP-CLIENT-01 |
| 6.4.2 | CLOSE Command | IMAP-CLOSE-*; IMAP-SELECT-07 |
| 6.4.3 | EXPUNGE Command | IMAP-EXPUNGE-* |
| 6.4.4 | SEARCH Command | IMAP-SEARCH-*; IMAP-INV-SEARCH-01 |
| 6.4.5 | FETCH Command | IMAP-FETCH-*; IMAP-ENV-*; IMAP-BS-*; IMAP-MSG-*; IMAP-INV-UID-05 |
| 6.4.6 | STORE Command | IMAP-STORE-*; IMAP-INV-FLAGS-*; IMAP-INV-ABNF-16 |
| 6.4.7 | COPY Command | IMAP-COPY-* |
| 6.4.8 | UID Command | IMAP-FETCH-09; IMAP-STORE-04; IMAP-COPY-*; IMAP-SEARCH-04; IMAP-SEARCH-05; IMAP-INV-UID-06; IMAP-INV-ABNF-21 |
| 6.5 | Client Commands - Experimental/Expansion | IMAP-XCMD-*; IMAP-INV-XCMD-01 |
| 6.5.1 | X<atom> Command | IMAP-XCMD-*; IMAP-INV-XCMD-01 |
| 7 | Server Responses | IMAP-RESP-*; IMAP-INV-UPDATE-01; IMAP-INV-ABNF-11 |
| 7.1 | Server Responses - Status Responses | IMAP-RESP-*; IMAP-INV-ABNF-20 |
| 7.1.1 | OK Response | IMAP-GREET-01; IMAP-RESP-02; IMAP-INV-ABNF-11 |
| 7.1.2 | NO Response | IMAP-RESP-10; IMAP-INV-RESP-01 |
| 7.1.3 | BAD Response | IMAP-BAD-01; IMAP-RESP-03; IMAP-INV-STATE-01 |
| 7.1.4 | PREAUTH Response | IMAP-RESP-11 |
| 7.1.5 | BYE Response | IMAP-GREET-02; IMAP-LOGOUT-01; IMAP-STATE-01; IMAP-RESP-13; IMAP-RESP-14; IMAP-INV-CLIENT-04 |
| 7.2 | Server Responses - Server and Mailbox Status | IMAP-INV-ABNF-18 |
| 7.2.1 | CAPABILITY Response | IMAP-CAP-*; IMAP-RESP-02; IMAP-INV-CAP-01; IMAP-INV-ABNF-07 |
| 7.2.2 | LIST Response | IMAP-LIST-*; IMAP-INV-LIST-*; IMAP-INV-ABNF-17; IMAP-INV-LIST-05 |
| 7.2.3 | LSUB Response | IMAP-LSUB-*; IMAP-INV-LSUB-01; IMAP-INV-ABNF-17 |
| 7.2.4 | STATUS Response | IMAP-STATUS-*; IMAP-INV-ABNF-18 |
| 7.2.5 | SEARCH Response | IMAP-SEARCH-01; IMAP-SEARCH-04; IMAP-INV-ABNF-18 |
| 7.2.6 | FLAGS Response | IMAP-SELECT-01; IMAP-SELECT-13; IMAP-INV-FLAGS-*; IMAP-RESP-CLIENT-01 |
| 7.3 | Server Responses - Mailbox Size | IMAP-INV-ABNF-18 |
| 7.3.1 | EXISTS Response | IMAP-SELECT-01; IMAP-UPDATE-01; IMAP-RESP-CLIENT-01 |
| 7.3.2 | RECENT Response | IMAP-RESP-15; IMAP-SELECT-08; IMAP-RESP-CLIENT-01 |
| 7.4 | Server Responses - Message Status | IMAP-INV-ABNF-18 |
| 7.4.1 | EXPUNGE Response | IMAP-EXPUNGE-02; IMAP-RESP-12; IMAP-RESP-CLIENT-01 |
| 7.4.2 | FETCH Response | IMAP-FETCH-*; IMAP-ENV-*; IMAP-BS-*; IMAP-MSG-*; IMAP-RESP-16; IMAP-INV-CLIENT-07; IMAP-INV-UID-05; IMAP-INV-ABNF-* |
| 7.5 | Server Responses - Command Continuation Request | IMAP-RESP-17; IMAP-LITERAL-*; IMAP-AUTHENTICATE-02; IMAP-INV-CLIENT-01 |
| 8 | Sample IMAP4rev1 connection | n/a: Illustrative transcript, not an additional requirement. Its component behaviors are indexed above; this audit does not claim a verbatim end-to-end replay of the entire sample. |
| 9 | Formal Syntax | IMAP-INV-CLIENT-08; IMAP-SYNTAX-*; IMAP-INV-ABNF-*; see the exhaustive production dispositions below. |
| 10 | Author's Note | n/a: Historical supersession note; no additional server behavior. |
| 11 | Security Considerations | IMAP-STARTTLS-*; IMAP-INV-SEC-* |
| 11.1 | STARTTLS Security Considerations | IMAP-INV-SEC-01; IMAP-INV-SEC-02; IMAP-INV-SEC-03; IMAP-INV-CLIENT-03 |
| 11.2 | Other Security Considerations | IMAP-INV-SEC-04; IMAP-INV-SEC-05; IMAP-INV-SEC-06; IMAP-INV-SEC-07; IMAP-AUTHENTICATE-09; IMAP-LOGIN-02 |
| 12 | IANA Considerations | n/a: IANA/specification-editor registration procedure, not a Session operation; capability naming duties are IMAP-INV-CAP-01 and IMAP-INV-ABNF-07. |
| A | Normative and informative references | n/a: Bibliography; referenced RFCs are dependencies of definitions, not an assertion that every requirement of every referenced RFC is inventoried here. |
| B | Changes from RFC 2060 | n/a: Historical change list. Reconciled against current prose/grammar above; superseded intermediate changes (for example NEWNAME) are not current requirements. |
| C | Key Word Index | n/a: Keyword locator index; duplicates requirements in the indexed sections and adds none. |

### RFC 3501 section 9: every production disposition

This is a requirement inventory, not just a production-name census: each
residual row states the relevant value/alternative restrictions and its reason
for remaining unproved. The existing component proofs are retained, but do not
upgrade a residual boundary into a mapped requirement. The pinned 148-production
source index is also used by `TestRFC3501Inventory`; no RFC9051 production is
added or reclassified by this audit.

| Productions | Requirement boundary | Related component rows |
|-------------|----------------------|------------------------|
| `address`, `addr-adl`, `addr-host`, `addr-mailbox`, `addr-name` | IMAP-INV-ABNF-01 | IMAP-ENV-* |
| `astring`, `ASTRING-CHAR`, `atom`, `ATOM-CHAR`, `atom-specials` | IMAP-INV-ABNF-02 | IMAP-SYNTAX-* |
| `authenticate`, `auth-type`, `base64`, `base64-char`, `base64-terminal` | IMAP-INV-ABNF-03 | IMAP-AUTHENTICATE-* |
| `body`, `body-type-1part`, `body-type-basic`, `body-type-mpart`, `body-type-msg`, `body-type-text`, `media-basic`, `media-message`, `media-subtype`, `media-text` | IMAP-INV-ABNF-04 | IMAP-BS-*; IMAP-MSG-* |
| `body-extension`, `body-ext-1part`, `body-ext-mpart` | IMAP-INV-ABNF-05 | IMAP-BS-* |
| `body-fields`, `body-fld-desc`, `body-fld-dsp`, `body-fld-enc`, `body-fld-id`, `body-fld-lang`, `body-fld-loc`, `body-fld-lines`, `body-fld-md5`, `body-fld-octets`, `body-fld-param` | IMAP-INV-ABNF-06 | IMAP-BS-* |
| `capability`, `capability-data` | IMAP-INV-ABNF-07 | IMAP-CAP-* |
| `CHAR8`, `literal` | IMAP-INV-ABNF-08 | IMAP-LITERAL-*; IMAP-APPEND-* |
| `command`, `command-any`, `command-auth`, `command-nonauth`, `command-select` | IMAP-INV-ABNF-09 | IMAP-SYNTAX-*; IMAP-MBOX-AUTH-01 |
| `append`, `copy`, `create`, `delete`, `examine`, `fetch`, `list`, `login`, `lsub`, `rename`, `search`, `select`, `status`, `store`, `store-att-flags`, `subscribe`, `uid`, `unsubscribe`, `userid`, `password`, `x-command` | IMAP-INV-ABNF-10 | IMAP-SYNTAX-*; IMAP-SELECT-*; IMAP-SEARCH-*; IMAP-STORE-*; IMAP-FETCH-*; IMAP-APPEND-* |
| `continue-req`, `greeting`, `response`, `response-data`, `response-done`, `response-fatal`, `response-tagged`, `resp-cond-auth`, `resp-cond-bye`, `resp-cond-state` | IMAP-INV-ABNF-11 | IMAP-GREET-*; IMAP-RESP-*; IMAP-TAG-01 |
| `date`, `date-day`, `date-day-fixed`, `date-month`, `date-text`, `date-year`, `date-time`, `time`, `zone` | IMAP-INV-ABNF-12 | IMAP-SEARCH-22; IMAP-SEARCH-23; IMAP-APPEND-29; IMAP-FETCH-07 |
| `digit-nz`, `number`, `nz-number`, `uniqueid` | IMAP-INV-ABNF-13 | IMAP-SYNTAX-08; IMAP-FETCH-18; IMAP-STATUS-08 |
| `envelope`, `env-bcc`, `env-cc`, `env-date`, `env-from`, `env-in-reply-to`, `env-message-id`, `env-reply-to`, `env-sender`, `env-subject`, `env-to` | IMAP-INV-ABNF-14 | IMAP-ENV-* |
| `fetch-att`, `section`, `section-msgtext`, `section-part`, `section-spec`, `section-text`, `header-fld-name`, `header-list` | IMAP-INV-ABNF-15 | IMAP-FETCH-*; IMAP-MSG-* |
| `flag`, `flag-extension`, `flag-fetch`, `flag-keyword`, `flag-list`, `flag-perm` | IMAP-INV-ABNF-16 | IMAP-STORE-05; IMAP-STORE-11; IMAP-APPEND-19 |
| `list-mailbox`, `list-char`, `list-wildcards`, `mailbox`, `mailbox-list`, `mbx-list-flags`, `mbx-list-oflag`, `mbx-list-sflag` | IMAP-INV-ABNF-17 | IMAP-MUTF7-*; IMAP-LIST-*; IMAP-LSUB-* |
| `mailbox-data`, `message-data`, `msg-att`, `msg-att-dynamic`, `msg-att-static`, `status-att`, `status-att-list` | IMAP-INV-ABNF-18 | IMAP-FETCH-*; IMAP-STATUS-*; IMAP-RESP-* |
| `nil`, `nstring`, `string`, `quoted`, `QUOTED-CHAR`, `quoted-specials` | IMAP-INV-ABNF-19 | IMAP-NIL-01; IMAP-SYNTAX-04; IMAP-ENV-*; IMAP-BS-* |
| `resp-specials`, `resp-text`, `resp-text-code`, `text`, `TEXT-CHAR`, `tag` | IMAP-INV-ABNF-20 | IMAP-SYNTAX-01; IMAP-SYNTAX-02; IMAP-RESP-* |
| `search-key`, `seq-number`, `seq-range`, `sequence-set` | IMAP-INV-ABNF-21 | IMAP-SEARCH-*; IMAP-SYNTAX-08; IMAP-FETCH-17 |

## RFC 3501: connection, any-state commands and LOGIN

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-GREET-01 | RFC 3501 §§3.1, 7.1.1 | Normative | Greet with an untagged OK, which starts the not authenticated state; refuse SELECT before authentication. | TestGreeting |
| IMAP-GREET-02 | RFC 3501 §§7.1.5, 9 | Normative | A connection the server is unwilling to accept (the Limiter refuses it) is greeted with an untagged BYE, then closed, rather than dropped without a greeting. | TestGreetingByeOnRefusal |
| IMAP-CAP-01 | RFC 3501 §6.1.1 | MUST | Send a single untagged CAPABILITY response listing IMAP4rev1 before the tagged OK, in every state. | TestCapability |
| IMAP-CAP-02 | RFC 3501 §6.1.1 | MUST | Implement the STARTTLS, LOGINDISABLED and AUTH=PLAIN capabilities: AUTH=PLAIN is advertised and AUTHENTICATE PLAIN works, and with TLS configured a cleartext session advertises STARTTLS and LOGINDISABLED. | TestAuthenticateCommand, TestStartTLSExample |
| IMAP-NOOP-01 | RFC 3501 §6.1.2 | Normative | NOOP always succeeds, in any state. | TestNoop |
| IMAP-LOGOUT-01 | RFC 3501 §6.1.3 | MUST | Send an untagged BYE before the tagged OK to LOGOUT, then close the connection. | TestLogout |
| IMAP-LOGIN-01 | RFC 3501 §6.2.3 | Normative | LOGIN with valid credentials returns OK and enters the authenticated state; invalid credentials return NO and leave the state unchanged. | TestLogin |
| IMAP-LOGIN-02 | RFC 3501 §6.2.3 | MUST | Implement a configuration that advertises LOGINDISABLED and refuses LOGIN until TLS protects the session; with TLS configured that is the cleartext behavior. | TestStartTLSExample, TestStartTLSCleartextCredentialsRefused |
| IMAP-AUTH-STATE-01 | RFC 3501 §§6, 6.2 | Normative | Once authenticated, a session cannot re-enter the not authenticated state or switch user: LOGIN and AUTHENTICATE get BAD with no continuation and no credential check, and the selected mailbox is kept. | TestNoReauthentication |
| IMAP-ANON-01 | RFC 3501 §6.2 | MAY | Allow access to certain mailboxes without authentication, e.g. SASL ANONYMOUS or LOGIN as "anonymous". | n/a: optional (MAY) and not offered; every mailbox needs LOGIN or AUTHENTICATE against the Backend. |
| IMAP-AUTHENTICATE-01 | RFC 3501 §6.2.2 | MUST | Implement AUTHENTICATE with the PLAIN mechanism, named case-insensitively; success returns tagged OK and enters the authenticated state. | TestAuthenticateCommand |
| IMAP-AUTHENTICATE-02 | RFC 3501 §6.2.2 | Normative | Each server challenge is a `+` continuation followed by a base64 string (empty for PLAIN), and each client response is one base64 line. | TestAuthenticateCommand |
| IMAP-AUTHENTICATE-03 | RFC 3501 §6.2.2 | MUST | A client response of a single `*` cancels the exchange: tagged BAD, and the session stays not authenticated. | TestAuthenticateCommand |
| IMAP-AUTHENTICATE-04 | RFC 3501 §6.2.2 | SHOULD | An unsupported mechanism gets tagged NO without a continuation. | TestAuthenticateCommand |
| IMAP-AUTHENTICATE-05 | RFC 3501 §§6, 6.2.2 | Normative | A missing mechanism is an invalid argument and gets BAD; rejected credentials get NO; neither changes state, and a later AUTHENTICATE can still succeed. | TestAuthenticateCommand |
| IMAP-AUTHENTICATE-06 | RFC 3501 §6.2.2; RFC 4616 §2 | Normative | The authorization identity names the user whose privileges are requested. Acting as another user is not supported, so an authzid naming a different user gets NO; an empty authzid or one equal to the authentication identity is accepted. | TestAuthenticateCommand |
| IMAP-AUTHENTICATE-07 | RFC 3501 §6.2.2; RFC 4959 §3 | Normative | RFC 3501 AUTHENTICATE carries no SASL initial response; the server advertises SASL-IR, which adds one (IMAP-SASLIR-01..04). | TestSASLIR |
| IMAP-AUTHENTICATE-08 | RFC 3501 §6.2.2 | SHOULD | List the supported mechanisms in CAPABILITY (AUTH=PLAIN); the tagged OK of a successful AUTHENTICATE carries a CAPABILITY response code (MAY). | TestAuthenticateCommand |
| IMAP-AUTHENTICATE-09 | RFC 3501 §6.2.2 | MUST | Implement a configuration that permits no plaintext password mechanism until STARTTLS: with TLS configured, cleartext AUTHENTICATE is refused before any continuation (sites SHOULD NOT run without such protection). | TestStartTLSCleartextCredentialsRefused |
| IMAP-AUTHENTICATE-10 | RFC 3501 §6.2.2 | SHOULD | Implement additional SASL mechanisms that do not use plaintext passwords, such as GSSAPI or DIGEST-MD5. | n/a: not supported yet; only PLAIN is offered, and Backend.Authenticate takes a user name and password, so a challenge-response mechanism needs a backend design. Tracked in rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1670. |
| IMAP-AUTHENTICATE-11 | RFC 3501 §6.2.2 | Normative | A negotiated SASL security layer takes effect right after the CRLF of the tagged OK. | n/a: PLAIN negotiates no security layer and no other mechanism is offered; see IMAP-AUTHENTICATE-10. |
| IMAP-AUTHENTICATE-12 | RFC 3501 §6.2.2 | Normative | The SASL service name is "imap". | n/a: PLAIN uses no service name; it matters only for mechanisms such as GSSAPI, see IMAP-AUTHENTICATE-10. |
| IMAP-CLIENT-01 | RFC 3501 §§6.1.1, 6.2.1, 6.2.3, 6.3.1, 6.3.10, 6.3.11, 6.4.1 | MUST | Client-side duties: require no capability beyond IMAP4rev1 unless advertised; send nothing after STARTTLS until TLS completes, then discard cached capabilities and re-issue CAPABILITY (SHOULD); never send LOGIN when LOGINDISABLED is advertised and use LOGIN only as a last resort; default missing SELECT data; avoid STATUS on the selected mailbox (SHOULD NOT) and never use it to check for new mail (MUST NOT); APPEND RFC 2822 messages (SHOULD); poll for new mail with NOOP, not CHECK (SHOULD). | n/a: client obligations; this library is the server side. Its server-side counterparts are IMAP-STARTTLS-06 (cleartext after STARTTLS never runs), IMAP-STARTTLS-03, IMAP-STARTTLS-08 and IMAP-STATUS-04. |
| IMAP-TAG-01 | RFC 3501 §§2.2.1, 2.2.2 | Normative | The tagged completion result carries the tag of the command it completes. | TestTaggedCompletion |
| IMAP-BAD-01 | RFC 3501 §7.1.3 | Normative | An unknown command receives a tagged BAD. | TestTaggedCompletion |

## RFC 3501 / RFC 2595: STARTTLS — partial negotiation slice

`TestStartTLSExample` follows the RFC 3501 §6.2.1 example transcript over a real
TLS handshake on the public `Session`, configured with a TLS config. Implicit TLS
(port 993) is set only by `Server` and is not covered here.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-STARTTLS-01 | RFC 3501 §§6.1.1, 6.2.3, 7.2.1; RFC 2595 §3.2 | MUST | With TLS configured, a cleartext session advertises STARTTLS and LOGINDISABLED and withholds AUTH=PLAIN; LOGIN gets tagged NO even with valid credentials. AUTHENTICATE is likewise refused before any continuation, and no credentials reach the backend. | TestStartTLSExample |
| IMAP-STARTTLS-02 | RFC 3501 §6.2.1 | Normative | STARTTLS returns tagged OK and TLS negotiation begins immediately after its CRLF, with no further octets in cleartext. | TestStartTLSExample |
| IMAP-STARTTLS-03 | RFC 3501 §6.2.1 | MAY | After STARTTLS the server advertises different capabilities: STARTTLS and LOGINDISABLED disappear, AUTH=PLAIN appears and LOGIN succeeds. | TestStartTLSExample |
| IMAP-STARTTLS-04 | RFC 3501 §6.2.1; RFC 2595 §3.1 | Normative | The session remains not authenticated after TLS negotiation: SELECT is refused and no credentials are checked until LOGIN. | TestStartTLSNonAuthenticated |
| IMAP-STARTTLS-05 | RFC 3501 §§6.2.1, 9; RFC 2595 §3.1 | Normative | STARTTLS is refused with BAD when TLS is not configured, when arguments are supplied or when TLS is already active, including in the authenticated state; the session stays usable. | TestStartTLSRefusals |
| IMAP-STARTTLS-06 | RFC 3501 §6.2.1 | Normative | Cleartext pipelined behind STARTTLS is discarded and never runs inside the TLS layer (CVE-2011-0411 class). | TestStartTLSDiscardsPipelinedPlaintext |
| IMAP-STARTTLS-07 | RFC 3501 §6.2.1 | Normative | A failed TLS negotiation ends the session; cleartext sent after the STARTTLS OK never runs as commands. | TestStartTLSFailedNegotiation |
| IMAP-STARTTLS-08 | RFC 3501 §6.2.3 | SHOULD NOT | When TLS is configured, do not permit LOGIN or AUTHENTICATE before TLS is active: every credential form, including literals and SASL initial responses, gets NO [PRIVACYREQUIRED] with no continuation and no backend credential check. A session built without TLS is a development deployment outside this row. | TestStartTLSCleartextCredentialsRefused |
| IMAP-STARTTLS-09 | RFC 2595 §3.1; RFC 3501 §6.2.1 | MAY | Authenticate with SASL EXTERNAL from TLS client credentials. | n/a: optional (MAY) and not offered. AUTH=EXTERNAL is never advertised and AUTHENTICATE EXTERNAL gets NO, before and after TLS; the session has no client-certificate identity source. Tracked in rest-mail/rest-mail-server#374. |

## RFC 3501: SELECT/EXAMINE — partial selection-state slice

The examples in §§6.3.1/6.3.2 supply the counts, first-unseen sequence numbers
and UID metadata in `TestSelectionExamples`. Flag lists vary with the store.
These fixtures deliberately align the current unread-based RECENT approximation
with the examples; they do not prove independent Recent tracking.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-SELECT-01 | RFC 3501 §6.3.1 | MUST | Send FLAGS and the mailbox's EXISTS count before successful tagged completion of SELECT/EXAMINE. | TestSelectionExamples |
| IMAP-SELECT-02 | RFC 3501 §§6.3.1, 7.1 | Normative | UNSEEN, if present, identifies the first unseen message by sequence number, not the unread count; omit it for an empty or fully seen mailbox. | TestSelectionExamples, TestSelectionUnseen |
| IMAP-SELECT-03 | RFC 3501 §6.3.1 | MUST | Send UIDNEXT before successful SELECT/EXAMINE completion; the examples report a value greater than all existing UIDs. | TestSelectionExamples |
| IMAP-SELECT-04 | RFC 3501 §6.3.1 | MUST | Send the backend's UIDVALIDITY before successful SELECT/EXAMINE completion when UIDPlusMailbox supplies it. | TestSelectionExamples |
| IMAP-SELECT-05 | RFC 3501 §6.3.1 | SHOULD | Identify a writable SELECT with a READ-WRITE response code and send its PERMANENTFLAGS before completion. | TestSelectionExamples |
| IMAP-SELECT-06 | RFC 3501 §§6.3.1, 6.3.2 | Normative | A backend selection failure returns NO and leaves no prior mailbox selected; a subsequent valid selection can recover. | TestSelectionFailureDropsOldMailbox |
| IMAP-SELECT-07 | RFC 3501 §§6.3.1, 6.4.2 | Normative | Selecting another mailbox replaces the old selection without expunging it; old folder-scoped deletion marks must not affect the new mailbox. | TestSelectionSwitchDoesNotExpunge |
| IMAP-SELECT-08 | RFC 3501 §§2.3.2, 6.3.1, 6.3.2 | MUST | Report independent Recent state and preserve it on EXAMINE. | n/a: not supported yet; Message has no Recent field and the engine approximates Recent as unseen. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-SELECT-09 | RFC 3501 §§2.3.1.1, 6.3.1 | MUST | UIDNEXT must track past UID assignments even after messages are expunged; UIDVALIDITY must reflect persistent mailbox identity. | n/a: not supported yet for the base Mailbox interface; UIDNEXT is derived from current messages and UIDVALIDITY defaults to 1 without UIDPlusMailbox. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-SELECT-10 | RFC 3501 §6.3.1 | MUST | SELECT must return READ-ONLY when access permits reading but forbids modification. | n/a: not supported yet; Mailbox exposes no per-folder write permission. Host can refuse Messages with an error but cannot select read-only through SELECT. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-SELECT-11 | RFC 3501 §§6.3.1, 6.3.2, 9 | Normative | SELECT/EXAMINE require exactly one mailbox astring: missing, malformed, extra or improperly separated arguments receive BAD without metadata reads or writes, and BAD leaves any prior selection, its read-only mode and its deletion marks unchanged (RFC 3501 §6); a later valid selection recovers without inheriting those marks. | TestSelectionInvalidArguments, TestSelectionInvalidLiteralArguments, TestSelectionRefusalKeepsSelection |
| IMAP-SELECT-12 | RFC 3501 §§4.3, 5.1, 6.3.1, 6.3.2, 9 | Normative | Decode bare, quoted and counted-literal mailbox names without trimming significant spaces, preserve escaped quoted-specials and quoted wildcard characters, canonicalize only INBOX, and preserve command framing for synchronizing and LITERAL+ names. An explicit empty string reaches the backend unchanged rather than selecting INBOX. Names are modified UTF-7, decoded before they reach the backend (IMAP-MUTF7-02). | TestSelectionMailboxAstrings, TestSelectionLiteralMailbox |
| IMAP-SELECT-13 | RFC 3501 §§6.3.1, 6.3.2 | MUST | Before tagged OK to SELECT or EXAMINE, send every REQUIRED response: FLAGS, EXISTS, RECENT, and OK PERMANENTFLAGS, UIDNEXT and UIDVALIDITY, with OK UNSEEN when an unseen message exists. The RECENT value itself is IMAP-SELECT-08. | TestSelectionExamples |
| IMAP-EXAMINE-01 | RFC 3501 §6.3.2 | MUST | Successful EXAMINE starts tagged OK text with READ-ONLY and reports no permanent flags. | TestSelectionExamples |
| IMAP-EXAMINE-02 | RFC 3501 §6.3.2 | Normative | EXAMINE forbids permanent writes through STORE, EXPUNGE and MOVE (including UID variants) and CLOSE; a later SELECT restores writable access. | TestExaminePreventsPermanentWrites |
| IMAP-EXAMINE-03 | RFC 3501 §6.3.2 | Normative | Non-peek FETCH and UID FETCH under EXAMINE return the body without persisting or locally setting Seen. | TestExamineBodyFetchDoesNotSetSeen |

The argument rows check complete astring tokens after literal materialization.
Raw quoted and literal octets share that representation, so strict wire-form
validation (including raw quoted 8-bit octets), permanent keyword support, UID exhaustion, backend error/fallback behavior and
concurrent sessions remain unaudited. These rows do not claim full
§§6.3.1/6.3.2 conformance.

## RFC 3501: STATUS — partial metadata and argument slice

`TestStatusExample` uses §6.3.10's A042 request, 231 messages and UIDNEXT
44292. The engine quotes the mailbox and returns items in request order, both
permitted by the response grammar. Other fixtures distinguish unseen counts
from selected-folder metadata and use mailbox-specific UIDVALIDITY values.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-STATUS-01 | RFC 3501 §§6.3.10, 7.2.4 | Normative | Return one untagged STATUS with the queried mailbox and requested MESSAGES/UNSEEN/UIDNEXT items before tagged OK, including the RFC example and empty mailboxes; item names are case-insensitive and no unrequested items are added. | TestStatusExample, TestStatusRequestedItems |
| IMAP-STATUS-02 | RFC 3501 §§5.1, 6.3.10, 7.2.4 | Normative | STATUS canonicalizes any-case INBOX and returns the queried mailbox's UIDVALIDITY when UIDPlusMailbox supplies it, rather than selected-folder values. | TestStatusRequestedItems |
| IMAP-STATUS-03 | RFC 3501 §§6.3.10, 9 | Normative | Reject malformed mailbox astrings, missing/unknown/quoted/non-ASCII items, non-SP or doubled/leading/trailing separators, unbalanced/nested lists and trailing arguments with BAD before metadata/content reads or flag changes; keep the session reusable. | TestStatusInvalidArguments |
| IMAP-STATUS-04 | RFC 3501 §6.3.10 | Normative | Successful and failed STATUS leave the selected mailbox, cached flags and SELECT/EXAMINE access mode unchanged; in the authenticated state STATUS does not select a mailbox or read message content. | TestStatusInvalidArguments, TestStatusPreservesSelection |
| IMAP-STATUS-05 | RFC 3501 §§3, 6.3.10 | Normative | Refuse STATUS before authentication without metadata access; backend lookup failure returns NO without an untagged STATUS, and a later valid query succeeds. | TestStatusPreservesSelection |
| IMAP-STATUS-06 | RFC 3501 §§4.3, 6.3.10, 7.2.4, 9 | Normative | Decode quoted and counted-literal mailbox astrings, echo the name in modified UTF-7 with escaped quotes/backslashes, treat quoted wildcard characters as part of a name, and reject NUL without dispatching literal payload as commands. | TestStatusMailboxStrings |
| IMAP-STATUS-07 | RFC 3501 §§2.3.2, 6.3.10 | Normative | RECENT reports independent Recent state and STATUS preserves it. | n/a: not supported yet; Message has no Recent field and STATUS approximates RECENT as unseen, as in IMAP-SELECT-08. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-STATUS-08 | RFC 3501 §§2.3.1.1, 6.3.10 | MUST | UIDNEXT tracks past UID assignments and UIDVALIDITY reflects persistent mailbox identity. | n/a: not supported yet for the base Mailbox interface; UIDNEXT is current maximum plus one and UIDVALIDITY defaults to 1 without UIDPlusMailbox, as in IMAP-SELECT-09. UID exhaustion and UIDValidity backend-error fallback remain unaudited. Tracked in rest-mail/rest-mail-server#374. |

This is not a complete STATUS or mailbox-name audit. Literal arguments are
materialized as quoted strings before parsing, so raw quoted 8-bit/control
octets cannot be distinguished from literal contents here; strict wire-form
validation, concurrent updates and UID exhaustion remain outside this slice. RECENT syntax remains accepted without a
claim that its current unread approximation conforms.

## RFC 3501: STORE and UID STORE — partial flag-update slice

`TestStoreExample` replays the §6.4.6 `A003 STORE 2:4 +FLAGS (\Deleted)`
example. Flag lists are sets; this engine orders system flags as \Seen,
\Answered, \Flagged, \Draft, then \Deleted.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-STORE-01 | RFC 3501 §6.4.6 | Normative | Without .SILENT, answer each changed message with an untagged FETCH carrying its full resulting flag list before the tagged OK. | TestStoreExample |
| IMAP-STORE-02 | RFC 3501 §6.4.6 | Normative | FLAGS replaces the flag list, +FLAGS adds to it and -FLAGS removes from it; system flags persist through the backend and \Deleted stays visible to later FETCH. | TestStoreExample, TestStoreLeavesRecentAlone |
| IMAP-STORE-03 | RFC 3501 §6.4.6 | SHOULD | With .SILENT, apply the change but send no untagged FETCH, for STORE and UID STORE. | TestStoreSilent |
| IMAP-STORE-04 | RFC 3501 §6.4.8 | MUST | An untagged FETCH caused by UID STORE includes the UID data item. | TestUIDStoreReportsUID |
| IMAP-STORE-05 | RFC 3501 §6.4.6 | Normative | A refused backend write answers NO (§6.4.6 "store error") and leaves the session's flags for that message unchanged, for STORE and UID STORE. | TestStoreBackendFailure |
| IMAP-STORE-06 | RFC 3501 §§2.3.2, 6.4.6, 7.1 | Normative | PERMANENTFLAGS lists `\*` only if keywords a client creates are stored and reported back by FETCH FLAGS. Keywords persist through the backend and `\*` permits their creation. | TestKeywordsAdvertisedAndStored |
| IMAP-STORE-07 | RFC 3501 §§6.4.6, 6.4.8, 9 | Normative | STORE and UID STORE reject malformed sequence sets with BAD before backend writes, session flag changes or FETCH responses, including .SILENT and empty mailboxes; the selection remains writable and reusable. | TestStoreInvalidSequenceSet |
| IMAP-STORE-08 | RFC 3501 §§6.4.6, 6.4.8, 9 | Normative | Accept reversed ranges, star, lists and unsigned-32-bit sequence-set bounds; resolve sparse or nonexistent UIDs by mailbox membership and preserve normal/silent flag updates and UID replies. | TestStoreValidSequenceSet |
| IMAP-STORE-09 | RFC 3501 §§6.4.6, 9 | Normative | STORE and UID STORE reject a malformed flag operand with BAD before backend writes, session flag changes or FETCH responses: unbalanced, nested or repeated parentheses, extra or non-SP separators, quoted strings, and flags that are not an atom with an optional leading `\` (atom-specials, `\*`, non-ASCII or control octets). | TestStoreInvalidFlagList |
| IMAP-STORE-10 | RFC 3501 §§6.4.6, 9 | Normative | Accept an empty flag-list, bare SP-separated flags, case-insensitive system flags, and keywords or flag-extensions using any ATOM-CHAR, for every action with and without .SILENT. | TestStoreValidFlagList |
| IMAP-STORE-11 | RFC 3501 §§2.3.2, 6.4.6 | Normative | FLAGS replaces the flags other than \Recent. The §9 flag production excludes \Recent, so naming it in any case in a FLAGS, +FLAGS or -FLAGS list, parenthesized or bare, for STORE or UID STORE, gets BAD and writes no listed flag; a keyword spelled Recent stays an ordinary flag, and a STORE never answers with \Recent. Read-only refusal still precedes this check under EXAMINE. | TestStoreLeavesRecentAlone, TestStoreRefusesRecent |
| IMAP-STORE-12 | RFC 3501 §6.4.6 | SHOULD | .SILENT suppresses only the STORE's own answer: a flag change from another source the session observes (when an idling session re-reads its mailbox) is still reported with an untagged FETCH carrying the message's flags. | TestStoreSilentStillReportsOtherSourcesChanges |

A NO from a multi-message set reports the first refused write; earlier
messages in the set keep their applied changes, which RFC 3501 permits.
Out-of-range message sequence numbers and overlapping UID-set semantics
remain unaudited. Another source's flag changes are observed while a
session idles (IMAP-STORE-12); between commands the session does not
re-read its mailbox.

## RFC 3501: SEARCH and UID SEARCH — partial criteria slice

`TestSearchExample` replays §6.4.4's A282 and A283 examples, with a mailbox
containing exactly the three expected matches and near misses for each A282
predicate. It does not replay A284's UTF-8 literal. Calendar-date tests supply
`Message.Date` as the internal date, including positive/negative timezone
offsets that cross UTC midnight. Hosts must supply that metadata correctly.
Size searches similarly trust the host's nonnegative `Message.Size`, the full
RFC822.SIZE octet count, including headers. Synthetic boundary fixtures and a
UTF-8/quoted-printable message distinguish strict octet comparisons from decoded
body or character counts; size searches never fetch message content. HEADER,
CC, BCC and SENT* keys read the raw header block from the backend; HEADER, CC
and BCC values are matched after RFC 2047 encoded-word decoding (IMAP-SEARCH-10).

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-SEARCH-01 | RFC 3501 §§6.4.4, 7.2.5 | MUST | Successful SEARCH sends one untagged SEARCH listing matching sequence numbers before tagged OK; no matches yields an empty SEARCH response. | TestSearchExample, TestSearchRequiresSelection |
| IMAP-SEARCH-02 | RFC 3501 §6.4.4 | Normative | Multiple keys and parenthesized groups intersect their matches; OR takes the union of two operands and NOT complements its operand. | TestSearchExample, TestSearchBooleanKeys |
| IMAP-SEARCH-03 | RFC 3501 §6.4.4 | Normative | ALL matches every message; SEEN/UNSEEN and FLAGGED filter flags; FROM and SUBJECT match case-insensitive substrings in metadata. | TestSearchExample, TestSearchBooleanKeys |
| IMAP-SEARCH-04 | RFC 3501 §§6.4.8, 7.2.5 | MUST | UID SEARCH applies the same criteria but returns UIDs rather than sequence numbers, including sparse UID assignments. | TestSearchBooleanKeys, TestSearchSequenceAndUIDKeys |
| IMAP-SEARCH-05 | RFC 3501 §§6.4.4, 6.4.8 | Normative | Bare sequence-set keys select sequence numbers even inside UID SEARCH; UID keys select UIDs. Reversed ranges, star and nonexistent UIDs respect the selected mailbox. | TestSearchSequenceAndUIDKeys |
| IMAP-SEARCH-06 | RFC 3501 §6.4.4 | Normative | BEFORE, ON and SINCE compare internal calendar dates, disregarding time and timezone; BEFORE excludes and SINCE includes the specified day. | TestSearchCalendarDates |
| IMAP-SEARCH-07 | RFC 3501 §6.4.4 | MUST | Accept US-ASCII; supported UTF-8 matches UTF-8 subject metadata. Unsupported CHARSET produces tagged NO with BADCHARSET and supported alternatives, without a SEARCH result. | TestSearchCharsets |
| IMAP-SEARCH-08 | RFC 3501 §§6.4.4, 9 | Normative | Require at least one key, including inside groups; reject unknown keys, missing operands, invalid dates and unbalanced groups with tagged BAD and no partial SEARCH result. | TestSearchInvalidCriteria |
| IMAP-SEARCH-09 | RFC 3501 §§3, 6.4 | Normative | SEARCH and UID SEARCH require a selected mailbox; authenticating alone does not permit searching. | TestSearchRequiresSelection |
| IMAP-SEARCH-10 | RFC 3501 §6.4.4 | MUST | Decode MIME transfer encodings and encoded headers to the selected character set before searching: BODY/TEXT remove base64 and quoted-printable encodings in every MIME leaf, including encapsulated messages; TEXT, HEADER and SUBJECT decode RFC 2047 encoded words; UTF-8, US-ASCII and ISO-8859-1 content is compared as UTF-8. Malformed encodings, parts in other charsets and encoded words in other charsets are compared as stored, and the encoded words around such a word still decode. | TestSearchDecodesMIME, TestSearchDecodesEveryWordItCan |
| IMAP-SEARCH-11 | RFC 3501 §6.4.4 | Normative | KEYWORD and UNKEYWORD take flag-keyword atoms and test presence/absence of that keyword; without keywords the former matches nothing and the latter everything; a missing or non-atom operand is BAD. | TestSearchKeywordKeys, TestKeywordsAdvertisedAndStored |
| IMAP-SEARCH-12 | RFC 3501 §6.4.4 | Normative | RECENT, NEW and OLD search independent Recent state. | n/a: not supported yet; RECENT approximates unseen, and NEW and OLD follow it (IMAP-SEARCH-27). Message has no Recent field. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-SEARCH-13 | RFC 3501 §§6.4.4, 6.4.8, 9 | Normative | Reject malformed bare sequence-set keys and UID set operands with BAD and no partial SEARCH result or content evaluation, including inside NOT, OR and groups and in empty mailboxes; keep the session reusable. | TestSearchInvalidSequenceSet |
| IMAP-SEARCH-14 | RFC 3501 §§6.4.4, 6.4.8, 9 | Normative | Valid reversed, star, overlapping and unsigned-32-bit boundary sets retain sequence/UID membership semantics under SEARCH and UID SEARCH, with each match returned once and no content reads or flag writes. | TestSearchValidSequenceSet |
| IMAP-SEARCH-15 | RFC 3501 §§6.4.4, 6.4.8 | Normative | ANSWERED/UNANSWERED and DRAFT/UNDRAFT match the presence/absence of their respective system flag under SEARCH and UID SEARCH, including Boolean/group combinations and empty/read-only mailboxes, without content reads or flag changes. | TestSearchAnsweredDraftKeys |
| IMAP-SEARCH-16 | RFC 3501 §§6.4.4, 6.4.6, 6.4.8 | Normative | Flag searches use current selected-session Answered/Draft values after successful STORE and UID STORE addition, removal and replacement; a refused backend update leaves search membership unchanged. | TestSearchAnsweredDraftStore |
| IMAP-SEARCH-17 | RFC 3501 §§6.4.4, 6.4.8, 9 | Normative | LARGER and SMALLER strictly compare full RFC822.SIZE octets with an unsigned 32-bit number, accepting zero and leading zeroes; SEARCH and UID SEARCH retain Boolean/group/set semantics without content reads or flag changes. | TestSearchSizeKeys, TestSearchSizeOctets |
| IMAP-SEARCH-18 | RFC 3501 §§6.4.4, 6.4.8, 9 | Normative | Missing, signed, quoted, literal, nondecimal and overflowing size operands produce BAD before results or content evaluation, even inside Boolean/groups or empty mailboxes; the session remains reusable. | TestSearchInvalidSize |
| IMAP-SEARCH-19 | RFC 3501 §6.4.4 | Normative | HEADER, CC and BCC match a case-insensitive substring of the unfolded text after the colon of the named header fields (Cc/Bcc for CC/BCC), never the body; field names compare case-insensitively and an empty string matches any message with the field; a message whose content is unavailable matches none. | TestSearchHeaderKeys, TestSearchHeaderUnavailable |
| IMAP-SEARCH-20 | RFC 3501 §6.4.4 | Normative | SENTBEFORE, SENTON and SENTSINCE compare the Date: header's calendar date as written, disregarding time and timezone and ignoring the internal date; a message with no parseable Date: header matches none. | TestSearchSentDates, TestSearchHeaderUnavailable |
| IMAP-SEARCH-21 | RFC 3501 §§6.4.4, 9 | Normative | Missing or empty HEADER field names, missing HEADER/CC/BCC strings and missing or invalid SENT* dates produce BAD before results or content reads, even inside Boolean/groups or empty mailboxes; the session remains reusable. | TestSearchInvalidHeaderKeys |
| IMAP-SEARCH-22 | RFC 3501 §§6.4.4, 6.4.8, 9 | Normative | All six internal/sent-date keys accept bare or DQUOTE-enclosed dates with one/two day digits, a case-insensitive month and four year digits; calendar comparisons retain SELECT/EXAMINE and sequence/UID semantics. | TestSearchDateGrammar |
| IMAP-SEARCH-23 | RFC 3501 §§6.4.4, 6.4.8, 9 | Normative | Reject date-token padding, controls, non-ASCII digits/months, quoted escapes, malformed widths and invalid calendar dates with BAD before results, content reads or flag changes, including Boolean/groups and empty mailboxes; keep command framing and selection reusable. | TestSearchInvalidDateGrammar |
| IMAP-SEARCH-24 | RFC 3501 §§4.3, 6.4.4, 6.4.8, 9 | Normative | Quoted SEARCH operands decode escaped DQUOTE and backslash without ending the token early, including Boolean/groups; synchronizing and LITERAL+ string operands preserve quotes, backslashes, parentheses, CRLF, UTF-8 and empty values without flag writes. | TestSearchQuotedStrings, TestSearchQuotedLiterals |
| IMAP-SEARCH-25 | RFC 3501 §§4.3, 6.4.4, 6.4.8, 9 | Normative | Unterminated quotes, invalid escapes, embedded quotes in bare tokens, NUL and text adjoining a closing quote receive BAD before results or content evaluation, including Boolean/groups and empty/read-only mailboxes; selection and command framing remain reusable. | TestSearchInvalidQuotes |
| IMAP-SEARCH-26 | RFC 3501 §§6.4.4, 7.4.2 | Normative | FROM matches a case-insensitive substring of the envelope's FROM field: the decoded display name as well as the address, so a string may span "Name <address>"; SEARCH and UID SEARCH read only metadata. | TestSearchFromMatchesTheEnvelopeMailbox |
| IMAP-SEARCH-27 | RFC 3501 §6.4.4 | Normative | NEW (functionally "(RECENT UNSEEN)") and OLD ("NOT RECENT") are operand-free, case-insensitive keys that answer exactly what their equivalents answer under SEARCH and UID SEARCH, alone, in Boolean/groups and with sets, in SELECT/EXAMINE and empty mailboxes, without content reads or flag changes. | TestSearchNewAndOld |
| IMAP-SEARCH-28 | RFC 3501 §6.4.4 | Normative | DELETED and UNDELETED follow the \Deleted flag STORE sets and clears in the session; UNFLAGGED is the complement of FLAGGED; TO matches a case-insensitive substring of the envelope's TO field after encoded-word decoding; SEARCH and UID SEARCH read only metadata. | TestSearchDeletedUnflaggedAndTo |
| IMAP-SEARCH-29 | RFC 3501 §§4.3, 6.4.4, 9 | Normative | Match supported charset names with ASCII case rules only; unknown quoted or literal names return one static NO BADCHARSET line without echoing control or 8-bit octets and leave pipelined commands usable. | TestCoreCharsetValuesStayASCII |
| IMAP-SEARCH-30 | RFC 3501 §§6.4.4, 7.1.3, 9 | Normative | Invalid SEARCH dates and unknown criteria return a single static BAD line without echoing literal octets, injecting responses or reading/changing messages; a following command still runs. | TestCoreSearchRefusalsKeepOneLine |

This is not a full search grammar or conformance audit. Date tokens and
quoted-token framing/escapes are checked above, each with a grammar-based fuzz
target: FuzzSearchDate compares date tokens against an independent RFC grammar
and Gregorian calendar oracle, and FuzzSearchQuotes covers quoted-token framing;
CI runs each for 30 seconds. The shared literal reader materializes literals as
quoted strings, so this slice preserves CR/LF and 8-bit literal bytes and does
not distinguish them from illegal bytes sent directly in a wire quoted-string.
Literal date operands, full string character validation, bare astring grammar
and separators between keys remain unaudited. Out-of-range message sequence
number rejection is also outside this slice. Backend Fetch errors, BODY/TEXT
matching beyond the empty-result example, malformed charset data, concurrency,
EXPUNGE suppression and the RFC's UID SEARCH example remain for later slices. No coverage gate or independent implementation
comparison is added here.

## RFC 3501: FETCH and UID FETCH — partial data/flag slice

The wire reader consumes literal octet counts independently of CRLF and tagged
text in the message. `TestFetchHeaderExample` uses §6.4.5's A654 command and
three concrete header payloads; `TestUIDFetchExample` reproduces §6.4.8's A999
UID/sequence-number example. This does not inventory all MIME or grammar rules.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-FETCH-01 | RFC 3501 §§4.3, 6.4.5, 7.4.2 | Normative | BODY[] returns the full raw message and BODY[HEADER]/BODY[TEXT] return the header including its blank line / body excluding the header, framed by exact literal octet counts. | TestFetchSections |
| IMAP-FETCH-02 | RFC 3501 §6.4.5 | Normative | Non-peek BODY sections implicitly set Seen in a writable selection and report changed flags; BODY.PEEK leaves flags unchanged and answers with the BODY item name. | TestFetchSections |
| IMAP-FETCH-03 | RFC 3501 §6.4.5 | Normative | RFC822 and RFC822.TEXT behave like BODY[] and BODY[TEXT]; RFC822.HEADER behaves like BODY.PEEK[HEADER], retaining their RFC822 response labels. | TestFetchSections |
| IMAP-FETCH-04 | RFC 3501 §§6.4.5, 7.4.2 | Normative | Partial text fetches slice zero-based octets, truncate at EOF and return empty beyond it; response labels contain only the origin, including zero, while nonpartial responses have no origin. | TestFetchPartialOctets |
| IMAP-FETCH-05 | RFC 3501 §6.4.5 | Normative | HEADER.FIELDS includes and HEADER.FIELDS.NOT excludes exactly matching case-insensitive field names, preserving folded continuations and the delimiting blank line. | TestFetchHeaderExample |
| IMAP-FETCH-06 | RFC 3501 §6.4.5 | Normative | Apply a partial range after header-field subsetting. | TestFetchHeaderExample |
| IMAP-FETCH-07 | RFC 3501 §§2.3.3, 2.3.4, 6.4.5 | Normative | INTERNALDATE and RFC822.SIZE report stored internal-date and octet-size metadata; metadata and peek items do not set Seen. | TestFetchMetadataAndMacros |
| IMAP-FETCH-08 | RFC 3501 §6.4.5 | Normative | FAST expands to FLAGS/INTERNALDATE/RFC822.SIZE; ALL adds ENVELOPE and FULL additionally adds non-extensible BODY. | TestFetchMetadataAndMacros |
| IMAP-FETCH-09 | RFC 3501 §6.4.8 | MUST | UID FETCH responses use sequence-number prefixes and include UID even if not requested; sparse ranges select existing UIDs and nonexistent UIDs cause no error or data response. | TestUIDFetchExample |
| IMAP-FETCH-10 | RFC 3501 §§6.4.5, 6.4.8 | Normative | Failure to load requested message data yields tagged NO, without a partial data response or Seen write for the failed message; earlier completed message responses remain valid. | TestFetchBackendFailure, TestFetchPartialFailure |
| IMAP-FETCH-11 | RFC 3501 §§6.4.5, 6.4.8 | Normative | Refused implicit Seen writes yield NO without advertising or caching the failed change; successful writes precede the response, and already Seen messages need no redundant write. | TestFetchSeenFailure, TestFetchPartialFailure |
| IMAP-FETCH-12 | RFC 3501 §§6.3.2, 6.4.5 | Normative | A non-peek body response after EXAMINE reports unchanged flags rather than a phantom Seen change. | TestFetchExamineEcho |
| IMAP-FETCH-13 | RFC 3501 §§3, 6.4 | Normative | FETCH and UID FETCH require a selected mailbox, including after UNSELECT. | TestFetchRequiresSelection |
| IMAP-FETCH-14 | RFC 3501 §6.4.5 | MUST | Numeric MIME part paths retrieve the designated part, with nested MESSAGE/RFC822 and MIME section rules; malformed section-specs receive BAD. | TestFetchNumericSections, TestFetchNumericSectionSingle, TestFetchInvalidSection |
| IMAP-FETCH-15 | RFC 3501 §§6.4.5, 9 | Normative | Invalid or unknown data items, malformed section/partial syntax and illegal macro combinations receive BAD. | TestFetchComposedGrammar, TestFetchComposedGrammarRefusals |
| IMAP-FETCH-16 | RFC 3501 §§6.4.5, 6.4.8, 9 | Normative | FETCH and UID FETCH reject malformed sequence sets with BAD before loading message data, changing Seen or sending FETCH responses, including in empty mailboxes; the connection and selection remain usable. | TestFetchInvalidSequenceSet |
| IMAP-FETCH-17 | RFC 3501 §§6.4.5, 6.4.8, 9 | Normative | FETCH and UID FETCH accept reversed ranges, star and nonzero unsigned-32-bit bounds; sparse/nonexistent UIDs select only existing messages, with sequence-number response prefixes and UID attributes. | TestFetchValidSequenceSet |
| IMAP-FETCH-18 | RFC 3501 §§6.4.5, 9 | Normative | BODY and BODY.PEEK partial requests require unsigned 32-bit number.nz-number operands; invalid suffixes receive BAD before content reads, Seen writes or responses, even for empty/nonmatching results. | TestFetchInvalidPartial |
| IMAP-FETCH-19 | RFC 3501 §§6.4.5, 7.4.2, 9 | Normative | Valid partial bounds, including leading-zero origins and uint32 maxima, return truncated octets and origin-only labels with ordinary PEEK/EXAMINE Seen semantics. | TestFetchPartialBounds |
| IMAP-FETCH-20 | RFC 3501 §§6.4.5, 6.4.8, 9 | Normative | Require one known data item, a nonempty SP-separated parenthesized item list, or a standalone macro; reject unknown names, illegal macro combinations and malformed outer framing before reads, Seen writes or responses, including empty/nonmatching results; preserve session usability. | TestFetchInvalidItems |
| IMAP-FETCH-21 | RFC 3501 §§6.4.5, 6.4.8, 9 | Normative | Standalone case-insensitive FAST, ALL and FULL produce their explicit-list results; ordinary singleton and list forms preserve exact literals, metadata and Seen semantics for FETCH and UID FETCH in SELECT and EXAMINE. | TestFetchItemForms |
| IMAP-FETCH-22 | RFC 3501 §§6.4.5, 6.4.8, 9 | Normative | HEADER.FIELDS and HEADER.FIELDS.NOT require one SP and a nonempty list of valid astrings with exact SP separators; malformed forms receive BAD before content reads, Seen writes or responses, including empty/nonmatching sets; preserve command stream and selection. | TestFetchInvalidHeaderLists |
| IMAP-FETCH-23 | RFC 3501 §§4.3, 6.4.5, 7.4.2, 9 | Normative | Decode quoted and literal header names, preserve their exact values for case-insensitive include/exclude matching, emit valid astring labels and exact folded-header subsets, apply partials after selection and retain PEEK/EXAMINE Seen semantics. | TestFetchHeaderAstrings, TestFetchHeaderLiteralNames |
| IMAP-FETCH-24 | RFC 3501 §6.4.5 | Normative | The blank line is included in every header fetch except for a message with no body and no blank line: HEADER, RFC822.HEADER, HEADER.FIELDS and HEADER.FIELDS.NOT answer such a message without one and TEXT with nothing; a message starting with the blank line has that line alone as its header and everything after it as its text. | TestFetchHeaderOnlyMessage |

This slice does not prove full ENVELOPE/BODYSTRUCTURE semantics, static attribute
immutability across sessions, absent-part NIL handling, sequence-set overlap
behavior, rejection of out-of-range message sequence numbers, UID exhaustion or
concurrent EXPUNGE suppression. Hosts must provide correct raw message bytes, octet size, internal
date and persistent UID/flag metadata.
Backend data-load failure now refuses ENVELOPE instead of falling back to partial
Message metadata. A failure stops a multi-message command without undoing earlier
successful messages. Network write errors remain best-effort; an accepted Seen
write can outlive a lost connection. Coverage gates, fuzzing, independent
comparison and a release remain separate work under the parent issue.

## RFC 3501: EXPUNGE and CLOSE — partial removal slice

`TestExpungeExample` uses §6.4.3's marked positions 3, 4, 7 and 11. The engine
sends descending sequence numbers 11, 7, 4, 3, which §7.4.1 permits, rather
than the example's ascending-removal responses 3, 3, 5, 8. Tests also check the
remaining backend UIDs and renumbered FETCH replies. `TestCloseExample` uses
the §6.4.2 A341 command/completion, with both marked and unmarked messages.
`TestCloseBackendFailure` refuses deletions during CLOSE: the engine answers NO,
stays selected, reports the removals that succeeded as EXPUNGE and keeps the
failed marks, so a retried CLOSE completes.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-EXPUNGE-01 | RFC 3501 §6.4.3 | Normative | Remove all marked messages, leave unmarked messages intact and send one EXPUNGE per successful removal before tagged OK; no matches succeeds without removals. | TestExpungeExample, TestExpungeNoMatches |
| IMAP-EXPUNGE-02 | RFC 3501 §7.4.1 | MUST | Each EXPUNGE reports the removed message's current sequence number, immediately reducing later sequence numbers; surviving messages retain their UIDs. | TestExpungeExample, TestUIDExpungeSubset |
| IMAP-EXPUNGE-03 | RFC 3501 §§6.4.3, 7.1.2 | Normative | A refused deletion returns NO; report successful removals, keep failed messages and their deletion marks, and allow retry without another STORE. | TestExpungeBackendFailure |
| IMAP-EXPUNGE-04 | RFC 3501 §§3, 6.4, 7.1.3 | Normative | EXPUNGE outside the selected state receives BAD and makes no changes. | TestExpungeRequiresSelection |
| IMAP-EXPUNGE-05 | RFC 3501 §§6.4.3, 9 | Normative | EXPUNGE takes no arguments; reject extra arguments with BAD before deletion. | TestExpungeAndCloseArguments |
| IMAP-EXPUNGE-06 | RFC 3501 §6.3.2 | Normative | EXPUNGE cannot permanently remove messages from a mailbox selected read-only by EXAMINE. | TestReadOnlyCloseAndExpunge |
| IMAP-CHECK-01 | RFC 3501 §6.4.1 | Normative | CHECK is valid only in the selected state, under SELECT or EXAMINE; with no housekeeping it is equivalent to NOOP, returns OK and keeps the selection. Before selection it gets BAD. | TestCheckCommand |
| IMAP-CHECK-02 | RFC 3501 §§6.4.1, 9 | Normative | CHECK takes no arguments; an argument gets BAD. | TestCheckCommand |
| IMAP-CLOSE-01 | RFC 3501 §§3, 6.4, 7.1.3 | Normative | CLOSE outside the selected state receives BAD. | TestExpungeRequiresSelection |
| IMAP-CLOSE-02 | RFC 3501 §§6.4.2, 9 | Normative | CLOSE takes no arguments; invalid arguments receive BAD without deletion or deselection. | TestExpungeAndCloseArguments |
| IMAP-CLOSE-03 | RFC 3501 §6.4.2 | Normative | Successful writable CLOSE removes marked messages without untagged EXPUNGE, enters authenticated state and clears old selection marks before reselection. | TestCloseExample |
| IMAP-CLOSE-04 | RFC 3501 §6.4.2 | Normative | CLOSE after EXAMINE succeeds silently and deselects without removing messages. | TestReadOnlyCloseAndExpunge |
| IMAP-CLOSE-05 | RFC 3501 §6.4.2 | Normative | LOGOUT implicitly closes a selected mailbox without expunging marked messages. | TestLogoutDoesNotExpunge |
| IMAP-CLOSE-06 | RFC 3501 §6.4.2 | Normative | Writable CLOSE removes every marked message before reporting successful completion. | TestCloseBackendFailure |
| IMAP-EXPUNGE-07 | RFC 3501 §§2.3.2, 6.4.3 | Normative | Deleted is persistent mailbox state: a second session sees the stored mark, can clear it, and the first session observes that change on NOOP with a persistent backend. | TestRFC3501DeletedSurvivesSessions |

On EXPUNGE/UID EXPUNGE backend errors, this engine continues trying other
marked messages and returns NO after reporting successful removals. This is
not an atomic multi-message operation. Hosts must make Delete's success/error
accurately reflect whether the message was permanently removed. Disconnected
clients may miss responses after successful backend deletion.

## RFC 4315: UID EXPUNGE — partial UIDPLUS slice

Requirements paraphrase [RFC 4315](https://www.rfc-editor.org/rfc/rfc4315.txt).
`TestUIDExpungeSubset` uses §2.1's `A003 UID EXPUNGE 3000:3002` command with
those three UIDs at positions 3–5; descending responses 5, 4, 3 remove the same
messages as the example's repeated sequence 3. Other UIDPLUS commands and
response codes are not covered by this slice.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-UIDEXPUNGE-01 | RFC 4315 §2.1 | Normative | Remove only messages both marked Deleted and in the specified UID set; other messages and deletion marks are unaffected, and nonexistent UIDs cause no removals. | TestUIDExpungeSubset |
| IMAP-UIDEXPUNGE-02 | RFC 4315 §2.1 | Normative | Backend expunge failure returns NO; successful removals are reported and failed messages remain marked for retry. | TestExpungeBackendFailure |
| IMAP-UIDEXPUNGE-03 | RFC 4315 §2.1; RFC 3501 §6.3.2 | Normative | UID EXPUNGE cannot remove messages from a mailbox selected read-only by EXAMINE. | TestReadOnlyCloseAndExpunge |
| IMAP-UIDEXPUNGE-04 | RFC 4315 §§1, 2.1 | Normative | Without UIDPLUS support or the required UID set, UID EXPUNGE receives BAD without changing deletion marks or messages. | TestUIDExpungeCapabilityAndMissingSet |
| IMAP-UIDEXPUNGE-05 | RFC 4315 §§2.1, 4; RFC 3501 §9 | Normative | Invalid UID sequence sets (zero, leading zeros, values above 2^32-1, empty items or range bounds, extra separators or trailing arguments) receive BAD before deletion; valid reversed, `*` and maximum-UID sets are still accepted. | TestUIDExpungeInvalidSet |

## RFC 3501: APPEND — partial message-literal slice

`TestAppendExample` sends the §6.3.11 message as exactly 310 octets, using its
A003 tag, destination and Seen flag. The remaining tests check the engine's
callback inputs and wire framing; they do not establish durable storage or
rollback by arbitrary backends. Draft/incomplete message headers are accepted.
The RFC 7888 rows cover the `{n+}` form; the engine advertises LITERAL-
(IMAP-LITMINUS-01..04), not LITERAL+.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-APPEND-01 | RFC 3501 §6.3.11 | Normative | Pass the RFC example's literal unchanged to the destination mailbox and return tagged OK after backend success. | TestAppendExample |
| IMAP-APPEND-02 | RFC 3501 §§4.3, 6.3.11 | Normative | Send a continuation before receiving synchronizing APPEND message octets. | TestAppendExample |
| IMAP-APPEND-03 | RFC 3501 §§4.3, 6.3.11 | Normative | Count message octets exactly, including zero-length and 8-bit content; keep command-looking text inside the literal and preserve the next command. | TestAppendLiteralOctets |
| IMAP-APPEND-04 | RFC 3501 §§4.3, 6.3.11 | Normative | Honor a quoted mailbox name and supply no requested flags when the optional flag list is absent. | TestAppendLiteralOctets |
| IMAP-APPEND-05 | RFC 3501 §6.3.11 | SHOULD | Pass explicitly requested Seen, Answered, Flagged and Draft flags to the backend. | TestAppendFlagsAndDate |
| IMAP-APPEND-06 | RFC 3501 §§5.1, 6.3.11 | SHOULD | With DateAppender, pass the explicit INTERNALDATE including its timezone; fold mixed-case INBOX to the canonical mailbox. | TestAppendFlagsAndDate |
| IMAP-APPEND-07 | RFC 3501 §6.3.11 | SHOULD | Notify a selected destination with EXISTS before completion and refresh sequence-number resolution; do not notify an unrelated selection. | TestAppendSelectedMailbox |
| IMAP-APPEND-08 | RFC 3501 §§3.3, 6.3.2, 6.3.11 | Normative | APPEND remains available in selected state, including after EXAMINE: read-only selection does not prohibit appending to a mailbox. | TestAppendSelectedMailbox |
| IMAP-APPEND-09 | RFC 3501 §6.3.11 | Normative | Backend append failure produces NO without a success notification; a later retry can succeed. | TestAppendBackendFailure, TestAppendNonSynchronizingBackendFailure |
| IMAP-APPEND-10 | RFC 3501 §§6.3.11, 9 | Normative | Invalid terminal literal declarations and extra arguments receive BAD without inviting message bytes or calling the backend. | TestAppendInvalidArguments |
| IMAP-APPEND-11 | RFC 3501 §§4.3, 6.3.11 | Normative | Refuse unsupported flags and oversized synchronizing message literals before sending a continuation, preserving command synchronization. | TestAppendRefusedBeforeContinuation |
| IMAP-APPEND-12 | RFC 7888 §3 | Normative | For an accepted non-synchronizing APPEND message literal, send no continuation and preserve a following pipelined command. | TestAppendNonSynchronizing, TestAppendNonSynchronizingBackendFailure |
| IMAP-APPEND-13 | RFC 3501 §§2.2, 6.3.11, 9 | Normative | Only a CRLF after the counted message completes APPEND; reject malformed termination without backend delivery and close the ambiguous stream. | TestAppendMalformedTerminator |
| IMAP-APPEND-14 | RFC 3501 §§2.2, 6.3.11, 9 | Normative | Disconnect before all message bytes and the command terminator arrive cannot deliver an incomplete APPEND to the backend. | TestAppendDisconnectBeforeCompletion |
| IMAP-APPEND-15 | RFC 3501 §2.3.2 | Normative | Treat supported system flag names case-insensitively on APPEND. | TestAppendFlagsCaseInsensitive |
| IMAP-APPEND-16 | RFC 3501 §§3.1, 6.3 | Normative | Refuse APPEND before authentication without inviting message bytes; permit it after successful LOGIN. | TestAppendRequiresAuthentication |
| IMAP-APPEND-17 | RFC 3501 §6.3.11 | MUST | Restore the mailbox after an unsuccessful append with no partially stored message. | n/a: the backend must append atomically; Mailbox.Append offers no rollback interface and the engine cannot undo a backend that mutates before returning an error. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-APPEND-18 | RFC 3501 §6.3.11 | MUST | Reject a nonexistent destination without implicitly creating it and supply TRYCREATE unless creation is known to be impossible. | TestTryCreateMissingDestination, TestTryCreatePlainAppendFailure |
| IMAP-APPEND-19 | RFC 3501 §6.3.11 | Normative | Set Recent on the new message and preserve requested Deleted and keyword flags. | n/a: not fully supported or wire-proved as a combined requirement; Recent is still approximated from unread messages. FlagUpdate now has Deleted and Keywords and APPEND dispatches them, so the earlier claim that those fields do not exist is obsolete. Durable storage remains the backend's duty. |
| IMAP-APPEND-20 | RFC 3501 §6.3.11 | SHOULD | Honor explicit INTERNALDATE and use current time by default for every backend. | n/a: explicit dates reach only DateAppender; other backends drop them, and default timestamps are assigned by the host rather than the engine. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-APPEND-21 | RFC 3501 §§4.3, 6.3.11 | Normative | Accept a literal mailbox-name astring before the message literal. | TestAppendLiteralMailbox |
| IMAP-APPEND-22 | RFC 7888 §§3–4 | MUST | For a syntactically valid non-synchronizing literal marker, early APPEND refusal sends BYE and ends the session without dispatching unread octets; backend refusal after consuming the complete command preserves synchronization. | TestAppendRejectedNonSynchronizing, TestAppendNonSynchronizingBackendFailure |
| IMAP-APPEND-23 | RFC 3501 §§4.3, 9 | Normative | Enforce the complete APPEND grammar, including all astring and literal forms. | TestAppendGrammarCombinations, TestAppendQuotedMailboxOctets |
| IMAP-APPEND-24 | RFC 3501 §§6.3.11, 9 | Normative | Answer BAD before a continuation, without calling the backend, for a malformed flag-list: unbalanced or nested parentheses, a second list, a missing SP after the list, doubled, leading, trailing or tab separators, and flags that are not an atom with an optional leading `\`. | TestAppendInvalidFlagList, TestAppendRejectedNonSynchronizing |
| IMAP-APPEND-25 | RFC 3501 §§6.3.11, 9 | Normative | Accept an empty flag-list and single-SP-separated supported flags; a well-formed list naming a flag the store cannot keep stays NO, not BAD. | TestAppendValidFlagList |
| IMAP-APPEND-26 | RFC 3501 §§6.3.11, 9 | Normative | Separate mailbox, flag-list, date-time and message literal by exactly one SP: answer BAD before a continuation, without calling the backend, for a missing, doubled, leading or tab separator, and accept every single-SP combination. | TestAppendInvalidSeparators, TestAppendSingleSeparators, TestAppendRejectedNonSynchronizing |
| IMAP-APPEND-27 | RFC 3501 §§4.3, 9 | Normative | Reject NUL in a counted APPEND message literal with BAD, without calling the backend; after consuming its terminator, keep the next command framed for both synchronizing and non-synchronizing literals. | TestAppendRejectsNULMessageOctet |
| IMAP-APPEND-28 | RFC 3501 §9 | Normative | Decode quoted APPEND mailbox names with escaped DQUOTE or backslash, and reject any other escape, NUL or 8-bit octet before a message-literal continuation. | TestAppendQuotedMailboxGrammar |
| IMAP-APPEND-29 | RFC 3501 §§6.3.11, 9 | Normative | Accept fixed-width, DQUOTE-delimited date-times with space-padded or two-digit days and preserve explicit dates at DateAppender, including Go's zero time; reject malformed fields, escapes and invalid calendar/time/zone values before continuation or backend writes, preserving synchronizing recovery and non-synchronizing BYE refusal. | TestAppendInvalidDateTime, TestAppendValidDateTime |
| IMAP-APPEND-30 | RFC 3501 §§4.3, 9 | Normative | Unquoted APPEND mailbox names contain only ASTRING-CHAR octets (including `]`); reject controls, non-ASCII octets, wildcards and quoted-specials before a continuation or backend call (a lone `&` is ASTRING-CHAR but not modified UTF-7, so NO: IMAP-MUTF7-03), preserving synchronizing recovery and closing on unread non-synchronizing literals. | TestAppendUnquotedMailboxGrammar, TestAppendRejectedNonSynchronizing |
| IMAP-APPEND-31 | RFC 3501 §6.3.11 | MUST | 8-bit APPEND data is preserved: the counted octets reach the backend unchanged, with no conversion. Storing them faithfully is the backend's duty. | TestAppendLiteralOctets |

Early APPEND refusals with a valid `{n+}` marker choose RFC 7888 §4's
BYE-and-close policy instead of draining potentially large payloads. This
includes malformed preceding arguments and unauthenticated commands. Invalid
markers outside the unsigned 32-bit literal grammar are not a supported
non-synchronizing form. An overlong initial APPEND line ending in `+}` is
covered by IMAP-LITERAL-08. LITERAL- is advertised, not LITERAL+; a `{n+}`
message over 4096 octets is BAD with TOOBIG (IMAP-LITMINUS-04).

## RFC 3501 / RFC 7888: command-argument literals — partial refusal slice

These rows cover the existing non-APPEND literal materialization path and its
resource limits. LITERAL- itself is the IMAP-LITMINUS rows below.
RFC 7888 §§3–4 permit BYE-and-close instead of consuming refused in-flight
literal data. Once a continuation line exceeds the line cap, its suffix and
any next literal marker are lost; this path closes conservatively as well.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-LITERAL-01 | RFC 3501 §4.3 | Normative | Refuse oversized synchronizing argument literals before requesting their octets, including aggregate limits across multiple literals; preserve the next command. | TestCommandLiteralSafeRefusal |
| IMAP-LITERAL-02 | RFC 7888 §§3–4 | MUST | Refusing an individual or aggregate oversized non-synchronizing argument literal sends BYE and ends the session without dispatching unread payload. | TestCommandLiteralRefusalCloses |
| IMAP-LITERAL-03 | RFC 7888 §§3–4 | MUST | Refusing an oversized assembled command with another non-synchronizing argument literal sends BYE and ends the session without dispatching that literal's unread payload. | TestCommandLiteralRefusalCloses |
| IMAP-LITERAL-04 | RFC 7888 §3 | MUST | Reject an overlong literal-continuation line with BYE and end the session because a discarded trailing marker may leave in-flight literal octets. | TestCommandLiteralRefusalCloses |
| IMAP-LITERAL-05 | RFC 3501 §§2.2.1, 4.3 | Normative | Refusing oversized assembled arguments preserves the next command if no literal remains unread or the next literal is synchronizing and no continuation has been sent. | TestCommandLiteralSafeRefusal |
| IMAP-LITERAL-06 | RFC 3501 §4.3; RFC 7888 §3 | Normative | Count accepted LOGIN argument octets, support empty literals, send continuations only for synchronizing forms, and preserve a pipelined next command. | TestCommandLiteralAccepted |
| IMAP-LITERAL-07 | RFC 3501 §§2.2.1, 4.3 | Normative | A read error before all argument octets or the continuation line arrive ends the session without dispatching the incomplete command or unread suffix. | TestCommandLiteralReadFailureEnds |
| IMAP-LITERAL-08 | RFC 7888 §§3–4 | MUST | An overlong initial command line ending in `+}` (a possible non-synchronizing marker) is refused with BAD, then BYE, and the session ends without dispatching in-flight octets, however the marker and terminator split across reads. | TestCommandLineOverflowLiteralCloses |
| IMAP-LITERAL-09 | RFC 3501 §§2.2.1, 4.3 | Normative | An overlong initial command line with no marker, or a synchronizing marker whose continuation was never sent, is refused with BAD and the next command still runs. | TestCommandLineOverflowSafeRefusal |
| IMAP-LITERAL-10 | RFC 3501 §§6.2.2, 9; RFC 4959 | Normative | An overlong AUTHENTICATE response is refused with tagged BAD before any credential check and the next command still runs, whatever the line ends in: a base64 response is not a literal position, so a trailing `{n}` or `{n+}` leaves nothing in flight. | TestAuthenticateResponseOverflow |
| IMAP-LITERAL-11 | RFC 2177 §3 | Normative | An overlong line while idling is ignored without ending IDLE, whatever it ends in: only DONE is legal input there, so no literal can be pending. A later command-shaped line is still not run, DONE ends IDLE, and the next command runs. | TestIdleLineOverflow |

The initial overlong line is discarded in constant memory, so only its last
octets are kept: any line ending in `+}` closes, even when the discarded text
would not parse as a valid marker. Malformed markers, complete quoting/separator
and CHAR8 validation, binary literals and
other extension interactions remain unaudited or unsupported. Overlong
AUTHENTICATE responses and IDLE lines keep the session because neither position
admits a literal (IMAP-LITERAL-10/11); a client that sends literal octets there
anyway is outside the grammar, the same as for a short line.
The accepted non-synchronizing form remains unadvertised compatibility behavior.

## RFC 3501 / RFC 4315: COPY and UID COPY — partial dispatch slice

`TestCopyExample` replays RFC 3501 §6.4.7's A003 command. The source UIDs and
assigned destination UIDs in `TestCopyUIDExample` reproduce RFC 4315 §3's
A004/A005 examples; comma lists are equivalent to ranges in COPYUID. The backend
fixture records dispatch and refuses selected messages. It does not prove a real
store preserves content/metadata or atomically copies a whole set.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-COPY-01 | RFC 3501 §6.4.7 | Normative | COPY passes each selected sequence number's UID and the destination to the backend, then returns tagged OK without command-specific untagged responses. Base backends omit COPYUID. | TestCopyExample |
| IMAP-COPY-02 | RFC 3501 §6.4.7 | Normative | COPY leaves source membership and source flags unchanged in the selected session. | TestCopyExample |
| IMAP-COPY-03 | RFC 4315 §3 | SHOULD | Successful COPY/UID COPY with destination UID metadata returns tagged OK with COPYUID containing destination UIDVALIDITY and corresponding source/destination UIDs in copy order. | TestCopyUIDExample, TestCopyUIDMapping |
| IMAP-COPY-04 | RFC 3501 §6.4.8; RFC 4315 §3 | Normative | UID COPY uses UIDs rather than sequence numbers and ignores nonexistent UIDs; an empty match succeeds without copying or reporting COPYUID. | TestCopyUIDExample, TestCopySets |
| IMAP-COPY-05 | RFC 3501 §§2.3.1.1, 6.4.8 | Normative | Reversed ranges denote the same members; a UID range ending in star includes the last message even if its other endpoint exceeds the greatest UID. | TestCopySets |
| IMAP-COPY-06 | RFC 3501 §9; RFC 4315 §3 | Normative | Overlapping sequence-set members identify each message once; UID COPY must not duplicate backend calls or COPYUID members. | TestCopySets, TestCopyUIDMapping |
| IMAP-COPY-07 | RFC 3501 §§6.4.7, 6.4.8 | Normative | A backend copy failure returns tagged NO without COPYUID and stops further dispatch; the session accepts a later retry. This does not establish rollback of earlier copies. | TestCopyBackendFailure |
| IMAP-COPY-08 | RFC 3501 §§5.1, 6.3.2, 6.4.7 | Normative | COPY/UID COPY may copy from an EXAMINE selection; quoted destination names retain spaces and INBOX is case-insensitive. | TestCopyReadOnlyAndNames |
| IMAP-COPY-09 | RFC 3501 §§3, 6.4 | Normative | COPY/UID COPY require a selected mailbox and dispatch no backend copy before selection. | TestCopyRequiresSelection |
| IMAP-COPY-10 | RFC 4315 §3 | Normative | Omit COPYUID when the destination UIDVALIDITY is unavailable rather than reporting guessed metadata; the completed copy still succeeds. | TestCopyUIDValidityFallback |
| IMAP-COPY-11 | RFC 3501 §6.4.7 | MUST | An unsuccessful COPY restores the destination to its state before the attempt. | n/a: not supported yet; per-message Copy/CopyUID cannot roll back earlier successful copies or a failed backend mutation. A batch transaction/rollback backend API is needed. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-COPY-12 | RFC 3501 §6.4.7 | SHOULD | Preserve flags and internal date and set Recent in the destination copy. | n/a: backend storage responsibility, not verified by these dispatch fixtures; Deleted/Keywords now have persistent model fields, but Copy must preserve them and the date, and actual Recent state is not represented. |
| IMAP-COPY-13 | RFC 3501 §6.4.7 | MUST | Refuse nonexistent destinations without auto-creation and prefix NO with TRYCREATE unless creation is known impossible. | TestTryCreateMissingDestination |
| IMAP-COPY-14 | RFC 3501 §§6.4.7, 9 | Normative | Reject invalid sequence sets and mailbox argument syntax with BAD. | TestCopyInvalidArguments |
| IMAP-COPY-15 | RFC 4315 §§3, 5 | SHOULD NOT | Suppress COPYUID for a destination the user cannot SELECT/EXAMINE and for UIDNOTSTICKY stores. | n/a: not supported yet; backend API exposes no destination readability or UIDNOTSTICKY marker. Hosts can suppress metadata by returning zero/error UIDValidity, but that policy is not independently checked here. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-COPY-16 | RFC 4315 §§3, 4 | Normative | COPYUID contains only valid nonzero destination UIDs, with no extraneous or repeated UIDs. | TestCopyUIDInvalidBackendUIDs |

This slice stops at the first backend failure and suppresses COPYUID on NO;
earlier destination writes can remain and a retry may duplicate them. This is an
explicit RFC rollback gap, not atomic COPY conformance. Concurrent updates,
copy-to-self notifications and the remaining UIDPLUS requirements remain for
later slices. No batch backend interface or public capability changes are made.

## RFC 6851: MOVE and UID MOVE — partial dispatch slice

`TestMoveUIDExample` follows the shape of RFC 6851's UID MOVE example:
COPYUID in an untagged OK, then one EXPUNGE per moved message, then the tagged
OK. Fixtures record `Mailbox.Move` / `Mover.MoveUID` dispatch and refuse selected
messages. They do not prove a real store moves each message atomically.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-MOVE-01 | RFC 6851 §3.1 | Normative | MOVE passes each selected message's UID and the destination to the backend, sends an untagged EXPUNGE for each moved message and returns tagged OK; moved messages leave the selected view. Base backends omit COPYUID. | TestMoveExample |
| IMAP-MOVE-02 | RFC 6851 §§3.3, 4; RFC 4315 §3 | SHOULD | With UIDPLUS and destination UID metadata, send COPYUID in an untagged OK before any EXPUNGE, with corresponding source/destination UIDs in move order. | TestMoveUIDExample, TestMoveUIDMapping |
| IMAP-MOVE-03 | RFC 6851 §3.2; RFC 3501 §§2.3.1.1, 6.4.8 | Normative | UID MOVE uses UIDs and ignores nonexistent UIDs; reversed ranges and star denote the same members; an empty match succeeds without EXPUNGE or COPYUID. | TestMoveUIDExample, TestMoveSets |
| IMAP-MOVE-04 | RFC 3501 §9; RFC 6851 §3.3 | Normative | Overlapping sequence-set members identify each message once: one backend move, one EXPUNGE and one COPYUID member per message, and no unrelated message leaves the view. | TestMoveSets, TestMoveUIDMapping |
| IMAP-MOVE-05 | RFC 6851 §3.3 | MUST | A backend move failure stops dispatch and returns tagged NO; messages already moved are still reported (COPYUID when available, then EXPUNGE) and unmoved messages stay in the source view; the session accepts a later retry. | TestMoveBackendFailure |
| IMAP-MOVE-06 | RFC 6851 §3.1; RFC 3501 §6.3.2 | Normative | MOVE/UID MOVE expunge the source, so they are refused with NO on an EXAMINE selection without dispatching a backend move. | TestMoveReadOnly |
| IMAP-MOVE-07 | RFC 6851 §3.1; RFC 3501 §§3, 6.4 | Normative | MOVE/UID MOVE require a selected mailbox and dispatch no backend move before selection. | TestMoveRequiresSelection |
| IMAP-MOVE-08 | RFC 3501 §5.1; RFC 6851 §3.1 | Normative | Quoted destination names retain spaces and INBOX is case-insensitive. | TestMoveNames |
| IMAP-MOVE-09 | RFC 4315 §3; RFC 6851 §4 | Normative | Omit COPYUID when destination UIDVALIDITY is unavailable or the backend has no atomic Mover; the completed move still succeeds. | TestMoveUIDValidityFallback |
| IMAP-MOVE-10 | RFC 6851 §3.3 | SHOULD | Each message is either moved or unaffected, never left in both mailboxes; no message is lost. | n/a: backend responsibility; the base `Mailbox.Move` fallback and `Mover.MoveUID` are trusted per message and not verified by these dispatch fixtures. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-MOVE-11 | RFC 6851 §3.3; RFC 3501 §6.4.7 | MUST | Refuse nonexistent destinations without auto-creation and prefix NO with TRYCREATE unless creation is known impossible. | TestTryCreateMissingDestination |
| IMAP-MOVE-12 | RFC 6851 §3.1; RFC 3501 §9 | Normative | Reject invalid sequence sets and mailbox argument syntax with BAD. | TestMoveInvalidArguments |
| IMAP-MOVE-13 | RFC 6851 §4 | Normative | With QRESYNC enabled, report moved messages with VANISHED instead of EXPUNGE; other sessions selecting the source learn of the removal. | n/a: the QRESYNC half does not apply: QRESYNC is neither advertised nor enabled, so VANISHED is never due. Telling other sessions on the source mailbox about moved messages needs cross-session notification, which the engine does not have. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-MOVE-14 | RFC 4315 §§3, 4 | Normative | COPYUID contains only valid nonzero destination UIDs, with no extraneous or repeated UIDs. | TestMoveUIDInvalidBackendUIDs |

A refused move stops at the failing message and reports only what the backend
actually moved. No rollback is attempted: RFC 6851 permits partial moves as long
as every message ends up in exactly one mailbox, which is the backend's job.

## RFC 3501: BODY and BODYSTRUCTURE — partial encoded-part slice

The two §7.4.2 example structures are reproduced using synthetic messages with
exactly the RFC's octet/line dimensions. The RFC provides structures, not raw
messages; these are not borrowed message test vectors. Exact public-Session
responses check both FETCH and UID FETCH under SELECT and EXAMINE. Encoded body
fixtures specify their own octet and physical-line counts independently of the
MIME parser; quoted-printable soft breaks, invalid escapes, empty bodies, final
CRLF and nested binary attachments guard against accidental decoding.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-BS-01 | RFC 3501 §7.4.2 | Normative | Reproduce the simple text and two-part text/base64 example structures, with the stated octet and line counts. | TestBodyStructureRFCExample, TestBodyStructureRFCMultipartExample |
| IMAP-BS-02 | RFC 3501 §7.4.2 | Normative | Basic single-part fields appear in order: type, subtype, parameters, ID, description, transfer encoding and encoded octet size. | TestBodyStructureRFCExample, TestBodyStructureTransferEncoding, TestBodyStructureNestedEncoding, TestBodyStructureRFCMultipartExample |
| IMAP-BS-03 | RFC 3501 §7.4.2 | Normative | TEXT adds a line count measured in its content transfer encoding, including soft-break lines rather than the decoded text's lines. | TestBodyStructureRFCExample, TestBodyStructureTransferEncoding, TestBodyStructureRFCMultipartExample |
| IMAP-BS-04 | RFC 3501 §7.4.2 | Normative | Multipart children retain their transfer encoding and encoded dimensions; nested multipart structures precede the subtype, and non-text/non-message parts have no text-line field. | TestBodyStructureTransferEncoding, TestBodyStructureNestedEncoding, TestBodyStructureRFCMultipartExample |
| IMAP-BS-05 | RFC 3501 §7.4.2 | MUST NOT | BODY omits extension data; BODYSTRUCTURE appends single-part MD5/disposition/language/location or multipart parameters/disposition/language/location in that order. | TestBodyStructureTransferEncoding, TestBodyStructureNestedEncoding, TestBodyStructureRFCMultipartExample |
| IMAP-BS-06 | RFC 3501 §§6.4.5, 6.4.8 | Normative | BODY and BODYSTRUCTURE metadata do not set Seen, work under EXAMINE, and describe the same message through UID FETCH; requesting both shares one raw-message load. | TestBodyStructureRFCExample, TestBodyStructureTransferEncoding, TestBodyStructureNestedEncoding, TestBodyStructureRFCMultipartExample |
| IMAP-BS-07 | RFC 3501 §7.4.2 | Normative | Preserve Content-ID, Content-Description and MIME parameters; render attachment and multipart disposition types with their parameters in the extension slot. | TestBodyStructureNestedEncoding, TestBodyStructureRFCMultipartExample |
| IMAP-BS-08 | RFC 3501 §7.4.2; RFC 3282 §2 | MAY | BODYSTRUCTURE derives the language slot from each entity's own Content-Language list: comments dropped, tags trimmed with case kept; none is NIL, one a string, several a parenthesized list; multipart and MESSAGE/RFC822 values are not inherited and tags use valid string framing. Tags are not validated against the language-tag grammar. | TestBodyStructureLanguage, TestBodyStructureNestedLanguage, TestBodyStructureLanguageStrings |
| IMAP-BS-09 | RFC 3501 §§7.4.2, 9 | Normative | MESSAGE/RFC822 carries its encapsulated envelope, body structure and line count. | TestMessageStructureRFCEnvelope, TestMessageStructureHeaderDefaults, TestMessageStructureScalarStrings, TestMessageStructureRecursiveBody, TestMessageStructureNestedMessage |
| IMAP-BS-10 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | Encode every body string using valid IMAP string framing for the source octets. | n/a: not fully audited; MIME strings are checked by IMAP-BS-13 through IMAP-BS-16, encapsulated envelope address components by IMAP-ENV-13, and complete binary-message handling remains unaudited; a raw NUL in a header or text, or a decoded NUL in FETCH data, receives NO before output or Seen writes (IMAP-ENV-12), and one in non-textual data goes out transfer-encoded (IMAP-RESP-16). Tracked in rest-mail/rest-mail-server#374. |
| IMAP-BS-11 | RFC 3501 §§7.4.2, 9; RFC 2045 §5.2; RFC 2046 §5.1.1 | Normative | Default omitted or unusable MIME fields according to MIME rules, including defaults that depend on the enclosing multipart subtype: a multipart Content-Type without a boundary is invalid and takes the default type in BODYSTRUCTURE and FETCH sections alike; a multipart with no readable parts is one empty text/plain; charset=us-ascii part (body-type-mpart needs at least one body), also inside multipart/digest; an unreadable header block describes the whole entity as text/plain; charset=us-ascii. Ordinary Content-Type/charset defaults and missing subtypes are IMAP-BS-20/21, multipart/digest child defaults IMAP-BS-12; comments in Content-ID/Description and malformed multipart framing are not part of this row. | TestBodyStructureEmptyMultipartFallback, TestBodyStructureUnreadableHeaderFallback, TestBodyStructureMultipartWithoutBoundary |
| IMAP-BS-12 | RFC 3501 §7.4.2; RFC 2046 §5.1.5 | Normative | A multipart/digest child without Content-Type is MESSAGE/RFC822 with its envelope, body structure and line count; explicit types still win, and the default does not leak into other multiparts or multiparts nested in the digest. | TestBodyStructureDigestDefault, TestBodyStructureDigestScope |
| IMAP-BS-13 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | MIME parameter values are strings: preserve empty values as quoted empty strings, escape quoted-specials, retain encoded words, frame RFC 2231 decoded CRLF values with octet-counted literals, send RFC 2231 decoded 8-bit values as encoded words unless the session enabled UTF8=ACCEPT, when valid UTF-8 is a literal, and keep an RFC 2231 value in a charset go-imap can't decode, at any segment, as one encoded word naming that charset in either session. Converting a header section changes only the parameter holding an 8-bit octet. | TestBodyStructureParameterStrings, TestMetadataStringsInUTF8Session, TestParameterInCharsetNotDecoded, TestParameterContinuationInCharset, TestParameterCharsetGoIMAPDecodes, TestParameterNamingNoCharset, TestStructureParameterInCharset, TestHeaderSectionKeepsEveryParameter, TestHeaderSectionKeepsParameterInUTF8Session |
| IMAP-BS-14 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | Content-ID, description and transfer-encoding fields use valid quoted/literal framing; missing optional fields remain NIL and missing transfer encoding defaults to 7BIT. | TestBodyStructureHeaderStrings |
| IMAP-BS-15 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | Single-part and multipart disposition parameters use string framing for empty, escaped and literal values; multipart content-type parameters preserve empty values and BODY omits extension data. | TestBodyStructureDispositionStrings |
| IMAP-BS-16 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | MIME string literals retain their byte counts and boundaries in multipart children and encapsulated MESSAGE/RFC822 bodies under FETCH/UID FETCH and SELECT/EXAMINE without Seen writes. | TestBodyStructureNestedStrings |
| IMAP-BS-17 | RFC 3501 §7.4.2; RFC 1864 §2 | Normative | BODYSTRUCTURE preserves supplied Content-MD5 in the single-part MD5 slot before disposition; absent/empty values remain NIL, multipart has no MD5 slot, and BODY omits extensions. No digest is generated or verified. | TestBodyStructureMD5Location, TestBodyStructureMultipartLocation, TestBodyStructureMessageLocation |
| IMAP-BS-18 | RFC 3501 §7.4.2 | Normative | BODYSTRUCTURE preserves Content-Location after the language slot on single-part, multipart and MESSAGE/RFC822 entities, without inheriting parent values or resolving relative URIs; absent/empty values remain NIL. | TestBodyStructureMD5Location, TestBodyStructureMultipartLocation, TestBodyStructureMessageLocation |
| IMAP-BS-19 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | Supplied MD5/location strings escape quoted-specials and are encoded words when 8-bit without UTF8=ACCEPT; metadata shares raw reads and leaves Seen unchanged under SELECT/EXAMINE and FETCH/UID FETCH. | TestBodyStructureExtensionStrings |
| IMAP-BS-20 | RFC 3501 §7.4.2; RFC 2045 §§5.2, 6.1; RFC 2046 §4.1.2 | Normative | An ordinary entity without Content-Type defaults to TEXT/PLAIN with US-ASCII charset and absent transfer encoding to 7BIT, regardless of MIME-Version; text without charset uses US-ASCII, explicit type/charset values win, and non-text types gain neither charset nor text line count. | TestBodyStructureMIMEDefaults, TestBodyStructureExplicitContentType |
| IMAP-BS-21 | RFC 2045 §§5.1, 5.2; RFC 3501 §7.4.2 | Normative | Content-Type requires type/subtype; a bare token must not invent a subtype. Apply the recommended TEXT/PLAIN US-ASCII fallback to absent/invalid type syntax and malformed parameters in ordinary entities, mixed children and encapsulated messages, discarding invalid type parameters but retaining independent content headers and encoded dimensions. | TestBodyStructureInvalidContentType, TestBodyStructureExplicitContentType |
| IMAP-BS-22 | RFC 2045 §5.1; RFC 822 §§3.4.2–3.4.3; RFC 3501 §7.4.2 | Normative | Content-Type ignores nested comments and quoted-pairs between lexical tokens, accepts whitespace around the slash, and preserves parentheses/escapes inside quoted parameter values. Comment-bearing multipart boundaries retain children; nested and encapsulated entities parse independently without changing encoded dimensions or metadata side effects. | TestBodyStructureContentTypeComments, TestBodyStructureNestedContentTypeComments |
| IMAP-BS-23 | RFC 2045 §§5.1–5.2; RFC 822 §3.4.3 | Normative | A comment separates tokens rather than joining them; split type/subtype/attribute/value tokens and unbalanced comments use invalid-type recovery, dropping invalid parameters and retaining independent Content-ID. | TestBodyStructureInvalidContentTypeComments |
| IMAP-BS-24 | RFC 3501 §§6.4.5, 7.4.2; RFC 5322 §4.2 | Normative | BODY and BODYSTRUCTURE describe every multipart child that numeric section FETCH and SEARCH see, split by the same code, including a child whose header holds a control octet and every child after it. | TestBodyStructureControlOctetPart, TestControlOctetPartConverted |

This slice preserves quoted-printable source data via raw MIME part traversal.
It does not validate or decode transfer encodings, expose numbered MIME FETCH
sections, or claim complete BODYSTRUCTURE/MIME conformance.

## MIME transfer-encoding token and unknown-encoding handling — partial slice

RFC 2045 §§1/6.1 apply RFC 822 §§3.4.2–3.4.3 comments and lexical
whitespace to a single encoding token. Fixtures are synthetic grammar cases,
not borrowed message vectors. Malformed values retain the prior opaque-string
behavior for the encoding string, with an opaque application/octet-stream type.
Unknown encodings override the declared or default Content-Type in
BODY/BODYSTRUCTURE, numeric sections and SEARCH (RFC 2045 §6.4). Raw FETCH
retains the stored headers and body. This is not a full MIME validity or
transfer-decoding check; prohibited known encodings on composite entities
remain unaudited (IMAP-MSG-09).

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-CTE-01 | RFC 2045 §§1, 6.1; RFC 822 §§3.4.2–3.4.3 | Normative | BODY/BODYSTRUCTURE derive the case-insensitive encoding token without surrounding whitespace or nested/escaped comments; retain extension tokens and encoded body octets/line counts (unknown encodings use the opaque type of IMAP-CTE-04). | TestBodyStructureEncodingComments, TestBodyStructureEncodingCommentScope |
| IMAP-CTE-02 | RFC 3501 §§6.4.5, 7.4.2; RFC 2045 §6.1 | Normative | Parse each multipart or encapsulated entity's own encoding, default absent fields to 7BIT without inheritance, and preserve metadata-only behavior under SELECT/EXAMINE and FETCH/UID FETCH. | TestBodyStructureEncodingCommentScope |
| IMAP-CTE-03 | RFC 2045 §6.1; RFC 822 §3.4.2 | Normative | A single encoding token cannot be manufactured by joining token pieces across comments or whitespace; malformed values retain a safely framed encoding string and use the opaque type of IMAP-CTE-04. | TestBodyStructureMalformedEncodingComments |
| IMAP-CTE-04 | RFC 2045 §6.4; RFC 3501 §§6.4.4, 6.4.5, 7.4.2 | MUST | Treat an entity with an unrecognized Content-Transfer-Encoding as application/octet-stream regardless of its declared or default Content-Type; preserve stored octets without recursive MIME interpretation or charset conversion, and scope the override to that entity. | TestUnknownTransferEncoding, TestUnknownTransferEncodingScope |

## MIME Content-Disposition comments — partial lexical slice

RFC 2183 §2 applies RFC 822 §§3.4.2–3.4.3 comments and lexical whitespace to
Content-Disposition. The comment lexer is shared with Content-Type
(IMAP-BS-22/23). Fixtures are synthetic grammar cases, not borrowed message
vectors. RFC 2183 parameter value syntax (dates, sizes) and RFC 2231
continuations beyond Go's MIME parser are not audited.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-CDP-01 | RFC 2183 §2; RFC 822 §§3.4.2–3.4.3; RFC 3501 §7.4.2 | Normative | BODYSTRUCTURE body-fld-dsp ignores nested/escaped and folded comments between the disposition type, parameter names and values, and preserves parentheses and quoted-pairs inside quoted parameter values. | TestBodyStructureDispositionComments |
| IMAP-CDP-02 | RFC 2183 §2; RFC 3501 §7.4.2 | Normative | Each single-part, multipart and encapsulated entity reports its own comment-bearing disposition without inheritance, under SELECT/EXAMINE and FETCH/UID FETCH. | TestBodyStructureDispositionCommentScope |
| IMAP-CDP-03 | RFC 2183 §2; RFC 822 §3.4.2 | Normative | A comment separates rather than joins tokens; split tokens, unbalanced comments or quotes, an empty type and a type/subtype pair yield NIL while independent fields are retained. | TestBodyStructureInvalidDispositionComments |

## RFC 3501: ENVELOPE — partial header slice

`TestEnvelopeExample` uses the message headers and ENVELOPE in RFC 3501 §8
(a003/a004). The full ten-field response is checked through public `Session`
FETCH and UID FETCH. Cached metadata intentionally disagrees with the raw
headers. Empty/missing headers are also exercised as robustness cases, even
where RFC 2822 requires a nonempty Date or From.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-ENV-01 | RFC 3501 §§7.4.2, 8 | Normative | Return ten fields in date, subject, from, sender, reply-to, to, cc, bcc, in-reply-to, message-id order, including the RFC example. | TestEnvelopeExample, TestEnvelopeDistinctFields |
| IMAP-ENV-02 | RFC 3501 §§2.3.3, 2.3.5, 7.4.2 | Normative | Compute date and subject from raw headers, retaining header text and unfolding continuations; do not substitute INTERNALDATE or a cached subject. | TestEnvelopeExample, TestEnvelopeDistinctFields, TestEnvelopeAbsentAndEmpty, TestEnvelopeScalarStrings |
| IMAP-ENV-03 | RFC 3501 §7.4.2 | Normative | Address lists preserve mailbox order and personal-name/mailbox/host components; absent or empty From, To, Cc and Bcc are NIL, regardless of cached metadata. | TestEnvelopeExample, TestEnvelopeDistinctFields, TestEnvelopeAbsentAndEmpty, TestEnvelopeAddressStrings |
| IMAP-ENV-04 | RFC 3501 §7.4.2 | Normative | Explicit Sender and Reply-To take their own address lists rather than From. | TestEnvelopeExample, TestEnvelopeDistinctFields |
| IMAP-ENV-05 | RFC 3501 §§6.4.5, 6.4.8, 7.4.2 | Normative | FETCH and UID FETCH return ENVELOPE without setting or persisting Seen. | TestEnvelopeExample, TestEnvelopeDistinctFields, TestEnvelopeAbsentAndEmpty, TestEnvelopeScalarStrings, TestEnvelopeReadOnlyAndSharedLoad, TestEnvelopeAddressStrings |
| IMAP-ENV-06 | RFC 3501 §7.4.2 | Normative | Absent scalar headers are NIL; present empty Date, Subject, In-Reply-To and Message-ID are empty strings. | TestEnvelopeAbsentAndEmpty |
| IMAP-ENV-07 | RFC 3501 §7.4.2 | Normative | Absent or empty Sender and Reply-To default to the From member, including when From is NIL. | TestEnvelopeAbsentAndEmpty |
| IMAP-ENV-08 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | Scalar header strings escape quote/backslash and are encoded words when 8-bit without UTF8=ACCEPT, and counted literals when valid UTF-8 with it; encoded words remain header text. | TestEnvelopeScalarStrings, TestMetadataStringsInUTF8Session |
| IMAP-ENV-09 | RFC 3501 §§2.3.3, 6.3.2, 6.4.5 | Normative | EXAMINE ENVELOPE remains read-only, INTERNALDATE remains arrival metadata and repeated items share a raw read. | TestEnvelopeReadOnlyAndSharedLoad |
| IMAP-ENV-10 | RFC 3501 §7.4.2 | Normative | Preserve RFC 2822 groups with group-start and group-end address structures. | TestEnvelopeGroups, TestEnvelopeMalformedGroups |
| IMAP-ENV-11 | RFC 3501 §7.4.2 | Normative | Preserve source routes and legal address syntax. | n/a: source routes are not supported yet and are always NIL. Other legal syntax net/mail refuses is read as IMAP-ENV-14 describes. Component string framing is checked by IMAP-ENV-13. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-ENV-12 | RFC 3501 §§4.3.1, 7.4.2 | Normative | Treat malformed header blocks and prohibited NUL octets consistently without fabricating valid header information. | TestEnvelopeMalformedHeaders, TestFetchRejectsNUL |
| IMAP-ENV-13 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | Address personal-name, mailbox and host components escape quoted-specials and use counted literals for CR/LF decoded from encoded words, in top-level and encapsulated envelopes. Without UTF8=ACCEPT a decoded 8-bit name is an encoded word and an 8-bit mailbox is left out of its field, which is NIL when none remains; with it, valid UTF-8 is a counted literal. A name in a charset go-imap has no reader for stays its original encoded word in either session, and its address stays. | TestEnvelopeAddressStrings, TestMessageStructureScalarStrings, TestMetadataStringsInUTF8Session, TestAddressNameInCharsetNotDecoded, TestAddressNameCharsetInEveryPosition, TestAddressNameMixesDecodedAndKeptWords, TestAddressNameCharsetGoIMAPDecodes, TestAddressNameOctetsNotUTF8, TestMailboxWithNoSevenBitFormIsLeftOut, TestObsoleteAddressNamesFollowTheSession |
| IMAP-ENV-14 | RFC 3501 §7.4.2; RFC 5322 §§3.4.1, 4.4; RFC 5321 §4.1.3 | Normative | Read obsolete address syntax (a local part of dotted words with quoted words, CFWS around dots) and domain or address literals into address structures; a mailbox that cannot be read, or that has an encoded word in its local part or domain (RFC 2047 §5), is left out without losing the others in the header. | TestEnvelopeObsoleteAddresses, TestObsoleteAddressNamesFollowTheSession, TestMailboxWithNoSevenBitFormIsLeftOut |

Malformed top-level header blocks yield ten NIL envelope fields rather than
cached summary data; malformed encapsulated headers retain the same ten-field
shape while their body description keeps the existing opaque TEXT/PLAIN recovery.
Raw message NUL and NUL decoded into fetched envelope/MIME strings receive NO
before any response fragment or Seen update. This is refusal, not BINARY support.
Address groups retain start/end markers, empty groups and member order, including
inside MESSAGE/RFC822; source-route and net/mail syntax limitations remain.

The ENVELOPE rows above cover top-level FETCH. The MESSAGE/RFC822 slice below
checks the separate nested-envelope helper, sharing the scalar header encoder.
The scalar literal encoder also accepts malformed raw 8-bit header text for
compatibility; this does not advertise or implement UTF8=ACCEPT.

## RFC 3501: MESSAGE/RFC822 — partial encapsulation slice

These public-Session fixtures place encapsulated messages both at the top level
and inside a multipart attachment, requesting BODY and BODYSTRUCTURE together
through FETCH/UID FETCH under SELECT/EXAMINE. The RFC §8 ENVELOPE headers are
reused inside a synthetic wrapper; the RFC supplies no corresponding raw
MESSAGE/RFC822 vector. Line counts are explicitly specified per fixture.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-MSG-01 | RFC 3501 §§7.4.2, 9 | Normative | MESSAGE/RFC822 appends the encapsulated envelope, body structure and whole-message text-line count immediately after the basic fields; octet size includes the encapsulated headers and body. | TestMessageStructureRFCEnvelope, TestMessageStructureHeaderDefaults, TestMessageStructureScalarStrings, TestMessageStructureRecursiveBody, TestMessageStructureNestedMessage |
| IMAP-MSG-02 | RFC 3501 §7.4.2 | MUST NOT | BODY omits extension data recursively; BODYSTRUCTURE keeps single-part extensions after the encapsulated envelope/body/line fields, including attachment disposition. | TestMessageStructureRFCEnvelope, TestMessageStructureHeaderDefaults, TestMessageStructureScalarStrings, TestMessageStructureRecursiveBody, TestMessageStructureNestedMessage |
| IMAP-MSG-03 | RFC 3501 §§6.4.5, 6.4.8 | Normative | Encapsulated metadata works through FETCH and UID FETCH under SELECT and EXAMINE, shares a single raw read for both forms, and leaves Seen unchanged. | TestMessageStructureRFCEnvelope, TestMessageStructureHeaderDefaults, TestMessageStructureScalarStrings, TestMessageStructureRecursiveBody, TestMessageStructureNestedMessage |
| IMAP-MSG-04 | RFC 3501 §§7.4.2, 8 | Normative | Nested envelopes describe the encapsulated headers in ten-field order, preserving Date spelling and unfolded Subject rather than taking outer-message summaries. | TestMessageStructureRFCEnvelope, TestMessageStructureHeaderDefaults, TestMessageStructureScalarStrings, TestMessageStructureRecursiveBody, TestMessageStructureNestedMessage |
| IMAP-MSG-05 | RFC 3501 §7.4.2 | Normative | Missing nested scalar fields are NIL, present empty scalars are empty strings, and empty/absent Sender and Reply-To default to From. | TestMessageStructureHeaderDefaults |
| IMAP-MSG-06 | RFC 3501 §§4.3, 7.4.2, 9 | Normative | Nested scalar strings preserve encoded words, escape quoted-specials, and frame literal-only octets with counted literals. | TestMessageStructureScalarStrings |
| IMAP-MSG-07 | RFC 3501 §§7.4.2, 9 | Normative | Recursively describe encapsulated multipart and MESSAGE/RFC822 bodies with their own headers and encoded dimensions; other MESSAGE subtypes omit RFC822-specific fields. | TestMessageStructureRecursiveBody, TestMessageStructureNestedMessage |
| IMAP-MSG-08 | RFC 3501 §§7.4.2, 9 | Normative | Return a valid envelope structure for malformed encapsulated header blocks. | TestMessageStructureMalformedEnvelope |
| IMAP-MSG-09 | RFC 3501 §§4.3.1, 7.4.2; RFC 2046 §5.2.1 | Normative | Handle invalid encodings, binary octets and excessive encapsulation consistently: a MESSAGE/RFC822 entity whose Content-Transfer-Encoding is not 7bit, 8bit or binary is one opaque APPLICATION/OCTET-STREAM leaf with its declared encoding in BODY and BODYSTRUCTURE, a numeric FETCH section returns its octets and nothing deeper, and SEARCH BODY matches its decoded octets. Raw/decoded NUL is refused on FETCH (IMAP-ENV-12), and excessive encapsulation is IMAP-MSG-10. | TestMessageRFC822Encoding |
| IMAP-MSG-10 | RFC 3501 §§6.4.4, 6.4.5, 7.4.2; RFC 2046 §§5.1, 5.2.1 | Normative | Handle excessive multipart and MESSAGE/RFC822 nesting consistently: nesting is bounded at depth 64, where such an entity is one opaque APPLICATION/OCTET-STREAM leaf in BODY and BODYSTRUCTURE, a numeric FETCH section returns its octets and nothing deeper, and SEARCH BODY matches its stored octets. | TestMIMEDepthLimit |

The nested address parser preserves groups (IMAP-ENV-10) and reads the obsolete
syntax net/mail refuses (IMAP-ENV-14); source routes remain an IMAP-ENV-11 gap.
Malformed MIME framing and complete extension-field semantics remain unaudited.

## RFC 3691 / RFC 5161 / RFC 2177: extension command exchanges

These rows cover command syntax, responses and session transitions, not a full
extension inventory. [RFC 3691](https://www.rfc-editor.org/rfc/rfc3691),
[RFC 5161](https://www.rfc-editor.org/rfc/rfc5161) and
[RFC 2177](https://www.rfc-editor.org/rfc/rfc2177) define the exchanges below.
The UNSELECT test uses §2's A341 tag; the ENABLE fixture uses
§3.1's CONDSTORE/X-GOOD-IDEA names, both unsupported by this server.
The IDLE test follows §3's A002/DONE sequence and pipelines a following NOOP.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-EXT-CAP-01 | RFC 3691 §1; RFC 5161 §1; RFC 2177 §3 | Normative | Advertise UNSELECT, ENABLE and IDLE in CAPABILITY, before and after authentication and selection. | TestExtensionCapabilities |
| IMAP-UNSELECT-01 | RFC 3691 §§2, 4 | Normative | UNSELECT (case-insensitive) returns tagged OK without expunging messages, including after marking messages Deleted or opening read-only. | TestUnselectWithoutExpunge |
| IMAP-UNSELECT-02 | RFC 3691 §2 | Normative | Successful UNSELECT leaves authentication intact and releases selection state; FETCH is refused until reselecting and old deletion marks/read-only mode do not affect a later writable selection. | TestUnselectWithoutExpunge |
| IMAP-UNSELECT-03 | RFC 3691 §2 | Normative | With no selected mailbox UNSELECT returns BAD and the connection remains usable. | TestUnselectRequiresSelection |
| IMAP-UNSELECT-04 | RFC 3691 §§2, 4 | Normative | Extra UNSELECT arguments receive BAD without dropping the selection or its deletion marks. | TestUnselectInvalidArgumentsPreserveSelection |
| IMAP-ENABLE-01 | RFC 5161 §3.1 | MUST | Ignore unknown names and known extensions not enabled through ENABLE, without enabling UTF8 as a side effect. | TestEnableUnknownAndCapabilityStability |
| IMAP-ENABLE-02 | RFC 5161 §§3.1–3.2 | MUST | Every successful ENABLE sends ENABLED before tagged OK, listing only newly enabled capabilities; send an empty list when none are new. | TestEnableUnknownAndCapabilityStability, TestEnableAdditiveAndPersistent |
| IMAP-ENABLE-03 | RFC 5161 §3.1 | MUST NOT | Enabling extensions leaves CAPABILITY unchanged, including empty and successful UTF8 enablement. | TestEnableUnknownAndCapabilityStability, TestEnableAdditiveAndPersistent |
| IMAP-ENABLE-04 | RFC 5161 §§3.1, 4 | MUST | Enable supported names case-insensitively; repeated/additive ENABLE retains mode across selection and UNSELECT without invoking EnableUTF8 twice. | TestEnableAdditiveAndPersistent |
| IMAP-ENABLE-05 | RFC 5161 §§3.1, 4 | Normative | Missing names or malformed capability atoms/separators receive BAD before any enablement; a valid retry succeeds. | TestEnableInvalidArguments |
| IMAP-ENABLE-06 | RFC 5161 §3.1; RFC 6855 §3 | Normative | Refuse ENABLE before authentication; a backend enablement failure returns NO without ENABLED or enabling mode, allowing a later retry. | TestEnableStateAndFailure |
| IMAP-IDLE-01 | RFC 2177 §§3–4 | Normative | IDLE takes no arguments: extra input receives BAD before any continuation and leaves the selection usable. | TestIdleInvalidArguments |
| IMAP-IDLE-02 | RFC 2177 §3 | MUST | Request a continuation, consume DONE, then immediately complete the original IDLE tag and process the following command; repeated IDLE exchanges preserve framing. | TestIdleDoneAndNextCommand |

The ENABLED list's newly-enabled-only behavior is a SHOULD in RFC 5161 §3.1;
its presence on success is a MUST. ENABLE does not implement CONDSTORE.
The supported UTF8 mode belongs to the session's backend; this proof observes
its enable callback, not a complete RFC 6855 audit. IDLE polling notifications,
concurrent mailbox changes, timeout policy, RFC 9051 IMAP4rev2 differences and
the remaining core/extension requirements still need a complete inventory.

## RFC 3501 §5.1: mailbox naming

A `Mailbox` names its folders in UTF-8 and the server converts at the wire:
until the client enables UTF8=ACCEPT, every mailbox name in a command or
response is modified UTF-7 (§5.1.3); after, it is UTF-8 (RFC 6855 §3). The
tests use the RFC's own examples, and FuzzModifiedUTF7, run in CI, checks the
converter against an independent regexp of the grammar and its round trip. §5.1.1's single hierarchy separator is "/",
the delimiter every LIST and LSUB reply names; §5.1.2's "#" namespaces are not
offered, and names starting with "#" are ordinary names.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-MUTF7-01 | RFC 3501 §§5.1, 5.1.3 | MUST | Without UTF8=ACCEPT enabled, LIST, LSUB, STATUS and QUOTAROOT write a store name holding non-ASCII or `&` in modified UTF-7 (`~peter/mail/&U,BTFw-/&ZeVnLIqe-`, `&` as `&-`, controls in base64), leave other names alone and return no 8-bit name; LIST patterns match the names as written. | TestMailboxNamesAreWrittenInModifiedUTF7 |
| IMAP-MUTF7-02 | RFC 3501 §§5.1, 5.1.3 | MUST | Names in SELECT, EXAMINE, STATUS, COPY, UID COPY, MOVE, APPEND, DELETE and RENAME are decoded from modified UTF-7 before they reach the store; the modified BASE64 is kept exactly and compared case-sensitively, while INBOX stays case-insensitive. | TestMailboxNamesAreDecodedFromModifiedUTF7 |
| IMAP-MUTF7-03 | RFC 3501 §5.1.3 | SHOULD | CREATE and RENAME refuse with NO a name that is not correctly modified UTF-7: no shift back (`&Jjo!`), a superfluous shift (`&U,BTFw-&ZeVnLIqe-`), base64 for printable ASCII, odd octets, unpaired surrogates, nonzero padding bits, `/` in base64 or a lone `&`; their correct forms are taken; any other command answers NO for such a name without asking the store. | TestInvalidModifiedUTF7NamesAreRefused |
| IMAP-MUTF7-04 | RFC 3501 §5.1; RFC 6855 §3 | SHOULD | Without UTF8=ACCEPT, CREATE refuses an 8-bit name with NO and no command finds a folder by one; a UTF8=ACCEPT server takes a UTF-8 name before ENABLE but still writes names in modified UTF-7 until then. | TestEightBitNamesBeforeUTF8 |
| IMAP-MUTF7-05 | RFC 6855 §3 | MUST | After ENABLE UTF8=ACCEPT, names travel as UTF-8 both ways: LIST writes the store's names as they are and command names reach the store unchanged, with no modified UTF-7 decoding. | TestUTF8AcceptNamesAreUTF8 |

## RFC 3501 §§5.2–5.5: updates, flow control, autologout and pipelining

The server learns of other sources' changes by re-reading the selected mailbox:
on NOOP and CHECK, and on each poll while a client idles (IMAP-STORE-12). FETCH,
STORE and SEARCH never send EXPUNGE (IMAP-RESP-12), so a client may pipeline
them by sequence number as §5.5 allows.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-UPDATE-01 | RFC 3501 §§5.2, 6.1.2, 7.4.1 | MUST | NOOP re-reads the selected mailbox and reports another source's expunge (EXPUNGE), flag change (FETCH FLAGS, a SHOULD) and new mail (EXISTS); an expunge alone is never an EXISTS that reduces the count, and no change or a failed read reports nothing. | TestNoopReportsOtherSourcesChanges |
| IMAP-UPDATE-02 | RFC 3501 §6.4.1 | Normative | With no housekeeping to do, CHECK is equivalent to NOOP and reports the same changes, under SELECT and EXAMINE. | TestCheckReportsLikeNoop |
| IMAP-UPDATE-03 | RFC 3501 §5.3 | MUST | No untagged response is sent while no command is in progress, even after an idle poll interval, so §5.3's flow-control rule has no unsolicited write to govern; the changes wait for the next command. | TestNoUntaggedResponseWithoutCommand |
| IMAP-UPDATE-04 | RFC 3501 §5.5 | MUST | Commands sent without waiting run to completion in the order given: FETCH + STORE + FETCH + SEARCH + CHECK of the same message answer in order, each seeing the one before. | TestPipelinedCommandsRunInOrder |
| IMAP-AUTOLOGOUT-01 | RFC 3501 §5.4 | MUST | An inactivity autologout timer lasts at least 30 minutes, and any command resets it. | n/a: the timer is internal and not settable through Session, so the server package's own tests prove it: TestAutologoutIsAtLeastThirtyMinutes and TestSession_CommandActivityResetsAutologout. |
| IMAP-STATE-01 | RFC 3501 §§3.4, 7.1.5 | MUST | The server does not close a connection unilaterally without an untagged BYE that gives the reason: when the Server stops, a session waiting for a command or idling sends BYE at once, and one running a command finishes it, then sends BYE. | n/a: Server-level, so the server package's own tests prove it: TestShutdownSaysByeToIdleSessions and TestShutdownWaitsForInflightSession. The autologout, panic and refusal BYEs are IMAP-RESP-14's. |
| IMAP-NIL-01 | RFC 3501 §4.5 | Normative | NIL is never an atom's value: a folder named NIL is listed and answered as the quoted name "NIL", and SELECT, EXAMINE and STATUS find it by that name, bare or quoted, case-sensitively. | TestMailboxNamedNIL |

## RFC 6855: UTF8=ACCEPT — partial slice

These rows apply to a session whose backend implements `UTF8Mailbox`, so the
server advertises UTF8=ACCEPT ([RFC 6855](https://www.rfc-editor.org/rfc/rfc6855));
ENABLE itself is IMAP-ENABLE-01 to -06. A backend without it keeps RFC 3501
behaviour. Mailbox names in responses are modified UTF-7 until the client
enables UTF-8 (IMAP-MUTF7-01), then literals when they carry 8-bit octets;
sending them as UTF-8 quoted-strings after ENABLE is a MAY left undone. NFC normalisation of mailbox
names (RFC 5198 §2, a SHOULD) is the backend's: this package has no Unicode
normalisation tables. UTF8=ONLY, CATENATE and downgrading (§§6–7) are not
implemented.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-UTF8-01 | RFC 6855 §3 | MUST | Take UTF-8 in quoted-strings and answer BAD to typed 8-bit octet sequences that are not well-formed UTF-8 (bare, truncated, surrogate, overlong), before a synchronizing literal's continuation; literal octets are exempt, and a backend without UTF8=ACCEPT requires 8-bit arguments to use literals (IMAP-SYNTAX-06). | TestUTF8QuotedStrings |
| IMAP-UTF8-02 | RFC 6855 §3 | MUST | CREATE and RENAME refuse, with NO, mailbox names that are not UTF-8 or contain C0 or C1 controls, DELETE, LINE SEPARATOR or PARAGRAPH SEPARATOR, quoted or literal, and accept other UTF-8 names. | TestUTF8MailboxNames |
| IMAP-UTF8-03 | RFC 6855 §3 | SHOULD | After ENABLE UTF8=ACCEPT, SEARCH and UID SEARCH with a CHARSET are BAD; before it, CHARSET is accepted as before. | TestUTF8SearchCharset |
| IMAP-UTF8-04 | RFC 6855 §4; RFC 4466 §2.2 | Normative | APPEND takes the message as "UTF8 (" literal8 ")", synchronizing or not, into a UTF-8 quoted mailbox; a missing ")" is BAD before the backend, and a backend without UTF8=ACCEPT refuses the data item with BAD. | TestUTF8AppendDataItem |
| IMAP-UTF8-05 | RFC 6855 §4 | MUST | Before ENABLE UTF8=ACCEPT, APPEND of a message with an 8-bit octet in its header fields is NO without reaching the backend, through a plain literal or the UTF8 data item; 8-bit body text is accepted, and after ENABLE the message is. | TestUTF8AppendBeforeEnable |

## RFC 4315: APPENDUID — partial response-metadata slice

[RFC 4315](https://www.rfc-editor.org/rfc/rfc4315) §3 supplies the
38505/3955 pair and saved-messages destination; the message is synthetic.
Tests also distinguish the DateAppender result from AppendUID's result.
Persistent UID assignment remains the backend's responsibility.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-UIDPLUS-CAP-01 | RFC 4315 §1 | Normative | Advertise UIDPLUS when the authenticated backend implements it; preserve advertisement after selection. | TestUIDPlusAdvertisement |
| IMAP-APPENDUID-01 | RFC 4315 §3 | SHOULD | Return destination UIDVALIDITY and assigned UID in tagged APPEND OK, including date-aware appends. | TestAppendUIDResponse |
| IMAP-APPENDUID-02 | RFC 4315 §§3–4 | MUST NOT | A single-message APPENDUID contains one UID, never a range or set. | TestAppendUIDResponse |
| IMAP-APPENDUID-03 | RFC 4315 §§3–4 | Normative | Omit unavailable UID metadata; never fabricate zero values. Accept maximum nonzero uint32 values. | TestAppendUIDUnavailable |
| IMAP-APPENDUID-04 | RFC 4315 §3 | Normative | APPENDUID describes success only: backend failure returns NO without it; a retry can succeed. | TestAppendUIDFailureAndRetry |
| IMAP-APPENDUID-05 | RFC 4315 §§3, 5 | SHOULD NOT | Disclose APPENDUID for append-only destinations. | n/a: not supported yet; no destination-readability API, as in IMAP-COPY-15. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-APPENDUID-06 | RFC 4315 §3 | Normative | Suppress APPENDUID for UIDNOTSTICKY destinations. | n/a: not supported yet; backend exposes no UIDNOTSTICKY status. Tracked in rest-mail/rest-mail-server#374. |

## RFC 6154: special-use mailboxes — partial slice

[RFC 6154](https://www.rfc-editor.org/rfc/rfc6154) §2 attributes on LIST, and
its extended-LIST selection and return options. Each role comes from the host
on `Folder.SpecialUse`; the engine never infers one from a name.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-SPECIALUSE-01 | RFC 6154 §2 | MAY | Identify sent-message storage with `\Sent`, as the host says. | TestSpecialUseOrdinaryList |
| IMAP-SPECIALUSE-02 | RFC 6154 §2 | MAY | Identify draft storage with `\Drafts`, as the host says. | TestSpecialUseOrdinaryList |
| IMAP-SPECIALUSE-03 | RFC 6154 §2 | MAY | Identify discarded-message storage with `\Trash`, as the host says. | TestSpecialUseOrdinaryList |
| IMAP-SPECIALUSE-04 | RFC 6154 §2 | MAY | Identify junk and archive storage with `\Junk` and `\Archive`, as the host says. | TestSpecialUseOrdinaryList |
| IMAP-SPECIALUSE-05 | RFC 6154 §2 | MUST | Advertise SPECIAL-USE; omit CREATE-SPECIAL-USE, which CREATE does not support. | TestSpecialUseCapabilityBoundary |
| IMAP-SPECIALUSE-06 | RFC 6154 §2 | MAY | Mailboxes may have no role; do not invent role folders or infer roles from names. | TestSpecialUseDoesNotInventFolders |
| IMAP-SPECIALUSE-07 | RFC 6154 §2 | MUST | Extended LIST selection filters special-use mailboxes; return option reports attributes. | TestSpecialUseExtendedList |
| IMAP-SPECIALUSE-08 | RFC 6154 §3 | MAY | CREATE assigns requested special-use attributes. | n/a: optional and unsupported; no role-assignment backend API. |
| IMAP-SPECIALUSE-09 | RFC 6154 §4 | MAY | METADATA changes special-use assignments. | n/a: optional and unsupported; no METADATA command or backend API. |

## RFC 3501 §§6.3.3–6.3.9: mailbox-management commands — partial slice

Folders are implicit in this engine: the `Mailbox` interface has `Folders` but
no create, delete or rename operation and no subscription store, so a folder
exists once the backend lists it. Rows that need that missing state are n/a and
name the gap rather than claiming it.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-MBOX-AUTH-01 | RFC 3501 §§3, 6.3 | Normative | CREATE, DELETE, RENAME, SUBSCRIBE, UNSUBSCRIBE, LIST and LSUB are authenticated-state commands: before login each gets only a tagged NO and no credential check. | TestMailboxCommandsNeedAuthentication |
| IMAP-CREATE-01 | RFC 3501 §§5.1, 6.3.3 | Normative | Creating INBOX, in any case, is an error answered with tagged NO; an ordinary new name gets OK. | TestInboxCannotBeCreatedOrDeleted |
| IMAP-CREATE-02 | RFC 3501 §6.3.3 | Normative | Creating a name that refers to an extant mailbox is an error answered with tagged NO. | n/a: not supported yet; folders are implicit and CREATE acknowledges any valid name without consulting `Folders`, because there is no backend create operation to make the result durable. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-CREATE-03 | RFC 3501 §6.3.3 | SHOULD | Create any superior hierarchical names a new name needs, and treat a trailing separator as a declaration of intent to create inferiors. | n/a: not supported yet; no backend create operation, so no superior name is made. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-CREATE-04 | RFC 3501 §6.3.3 | MUST | A mailbox re-created with a deleted mailbox's name uses UIDs greater than the earlier incarnation's unless its UIDVALIDITY differs. | n/a: UID assignment belongs to the backend; the engine never assigns UIDs. |
| IMAP-DELETE-01 | RFC 3501 §§5.1, 6.3.4 | Normative | Deleting INBOX, in any case, is an error answered with tagged NO. | TestInboxCannotBeCreatedOrDeleted |
| IMAP-DELETE-02 | RFC 3501 §6.3.4 | Normative | Deleting a name that does not exist is an error, and DELETE permanently removes the mailbox. | n/a: not supported yet; DELETE moves the folder's messages to Trash and answers OK for an unknown name, since there is no backend delete operation. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-DELETE-03 | RFC 3501 §6.3.4 | MUST NOT | DELETE does not remove inferior hierarchical names. | TestDeleteRenameHierarchy |
| IMAP-DELETE-04 | RFC 3501 §6.3.4 | Normative | Refuse DELETE of a Noselect hierarchy name that has inferiors. | n/a: not supported yet; the backend has no selectable/Noselect mailbox-state contract, so this conditional refusal is not enforced or proved. |
| IMAP-DELETE-05 | RFC 3501 §6.3.4 | MUST | Preserve the deleted mailbox's highest-used UID so a new mailbox of the same name does not reuse its UIDs unless UIDVALIDITY differs. | n/a: UID assignment belongs to the backend; see IMAP-CREATE-04. |
| IMAP-RENAME-01 | RFC 3501 §6.3.5 | Normative | Renaming from a name that does not exist, or to a name that already exists, is an error answered with tagged NO. | n/a: not supported yet; RENAME answers OK without checking either name, since there is no backend rename operation. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-RENAME-02 | RFC 3501 §6.3.5 | MUST | Inferior hierarchical names are renamed with their superior; a near name that only shares the prefix is untouched. | TestDeleteRenameHierarchy |
| IMAP-RENAME-03 | RFC 3501 §6.3.5 | Normative | Renaming INBOX is permitted: its messages move to the new name, INBOX stays and is left empty, and its inferiors are not renamed. | n/a: not supported yet; RENAME INBOX gets NO (see session_inbox_case_test.go). Tracked in rest-mail/rest-mail-server#374. |
| IMAP-RENAME-04 | RFC 3501 §6.3.5 | MUST | Preserve the old name's highest-used UID so a new mailbox of that name does not reuse its UIDs unless UIDVALIDITY differs. | n/a: UID assignment belongs to the backend; see IMAP-CREATE-04. |
| IMAP-SUB-01 | RFC 3501 §§6.3.6, 6.3.7 | Normative | SUBSCRIBE and UNSUBSCRIBE complete with OK in the authenticated and selected states, including for a name with no mailbox (permitted, since validation is a MAY); a missing name is BAD. | TestSubscriptionCommands |
| IMAP-SUB-02 | RFC 3501 §§6.3.6, 6.3.7, 6.3.9 | MUST NOT | Keep a subscription list: SUBSCRIBE adds a name, UNSUBSCRIBE removes it, and a subscribed name is never removed unilaterally even if its mailbox is gone. | n/a: not supported yet; there is no subscription store and LSUB reports every existing folder, so neither command changes LSUB. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-LIST-01 | RFC 3501 §6.3.8 | Normative | An empty mailbox name returns one reply with `\Noselect`, the hierarchy delimiter and an empty root name. | TestListLsubResponses |
| IMAP-LIST-02 | RFC 3501 §6.3.8 | Normative | `*` matches zero or more characters including the delimiter; `%` matches zero or more characters except the delimiter. | TestListLsubResponses |
| IMAP-LIST-03 | RFC 3501 §§5.1, 6.3.8 | Normative | Only names matching the pattern are returned; INBOX matches an INBOX pattern in any case. | TestListLsubResponses |
| IMAP-LIST-04 | RFC 3501 §6.3.8 | Normative | A non-empty reference name is prefixed to the mailbox pattern for LIST and LSUB; a pattern that starts at the hierarchy root (`/`) ignores the reference. | TestListReference |
| IMAP-LIST-05 | RFC 3501 §6.3.8 | Normative | An empty mailbox name with a non-empty reference returns `\Noselect`, the delimiter and the root name of the reference: its first level up to and including the delimiter, or empty when it has none. | TestListReference |
| IMAP-LIST-06 | RFC 3501 §6.3.8 | Normative | When `%` ends the pattern, matching levels of hierarchy are returned too, and a level that is not a selectable mailbox is returned with `\Noselect`; with `*` every name below is listed itself. | TestListHierarchyLevels |
| IMAP-LSUB-01 | RFC 3501 §§6.3.9, 7.2.3 | Normative | LSUB answers with untagged LSUB replies, not LIST replies, and completes with `OK LSUB completed`. | TestListLsubResponses, TestSubscriptionCommands |
| IMAP-LSUB-02 | RFC 3501 §6.3.9 | Normative | Under `%`, a non-subscribed superior of a subscribed inferior is returned with `\Noselect`. | n/a: every existing folder counts as subscribed, so there is no unsubscribed superior to report; depends on IMAP-SUB-02. |

## RFC 3501 §§6.5, 7: X-commands and server responses — partial slice

§7 mostly describes response forms that command rows above already prove;
those are referenced in the audit record below, not repeated here. Rows here
cover the remaining server duties, and the client duties §7 states so the
inventory is complete.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-RESP-01 | RFC 3501 §§7, 7.1 | Normative | Server data and status responses are untagged lines starting `* `; a completion carries the command's own tag. | TestTaggedCompletion, TestUntaggedBadForUndeterminedCommand |
| IMAP-RESP-02 | RFC 3501 §§7.1, 7.1.1 | MAY | The greeting's CAPABILITY response code lists IMAP4rev1 and matches the list a CAPABILITY command then returns. | TestGreetingCapabilityCode |
| IMAP-RESP-03 | RFC 3501 §7.1.3 | Normative | A line from which no tag and command can be read gets an untagged BAD, and the session goes on to the next command. | TestUntaggedBadForUndeterminedCommand |
| IMAP-RESP-04 | RFC 3501 §7.1 | SHOULD | Response codes an implementation defines for itself carry an `X` prefix. | n/a: the engine defines none. Every code it sends is registered: CAPABILITY, PERMANENTFLAGS, READ-ONLY, READ-WRITE, UIDNEXT, UIDVALIDITY, UNSEEN and BADCHARSET (RFC 3501), APPENDUID and COPYUID (RFC 4315), AUTHENTICATIONFAILED and PRIVACYREQUIRED (RFC 5530); checked against session.go and extensions.go (now in pkg/imap/server) on 2026-10-22. |
| IMAP-RESP-05 | RFC 3501 §7.1 | Normative | ALERT text is a special alert for the user. | n/a: the engine sends no ALERT; presenting one is a client duty (IMAP-RESP-CLIENT-01). |
| IMAP-RESP-06 | RFC 3501 §§6.4.4, 7.1 | Normative | A SEARCH in an unsupported charset fails with NO and a BADCHARSET code listing the supported charsets. | TestSearchCharsets |
| IMAP-RESP-07 | RFC 3501 §7.1 | Normative | PARSE reports a failure to parse a message's RFC 2822 or MIME headers. | n/a: optional and unsupported; malformed headers are answered as IMAP-ENV-12 and IMAP-MSG-08 describe, without a PARSE warning. |
| IMAP-RESP-08 | RFC 3501 §§6.3.1, 6.3.2, 7.1 | Normative | SELECT and EXAMINE carry PERMANENTFLAGS, UIDNEXT, UIDVALIDITY and UNSEEN codes on untagged OK, and READ-WRITE or READ-ONLY on the tagged OK. | TestSelectionExamples, TestSelectionUnseen |
| IMAP-RESP-09 | RFC 3501 §7.1 | Normative | TRYCREATE marks an APPEND or COPY that failed because the target mailbox does not exist. | TestTryCreateMissingDestination |
| IMAP-RESP-10 | RFC 3501 §7.1.2 | Normative | A tagged NO reports that the command failed, and the session goes on. | TestExpungeBackendFailure, TestFetchBackendFailure |
| IMAP-RESP-11 | RFC 3501 §7.1.4 | Normative | PREAUTH greets a connection that is already authenticated. | n/a: the engine never pre-authenticates; every greeting is OK (TestGreeting). |
| IMAP-RESP-12 | RFC 3501 §7.4.1 | MUST NOT | Send no EXPUNGE when no complete command is in progress, or while answering non-UID FETCH, STORE or SEARCH, even after another session removed a message. The test also defers during UID forms, which the RFC permits but does not require. | TestNoExpungeDuringFetchStoreSearch |
| IMAP-RESP-13 | RFC 3501 §§6.1.3, 7.1.5 | Normative | BYE is always untagged. On LOGOUT it comes before the tagged OK; on a failure the connection closes straight after it. | TestLogout, TestCommandLiteralRefusalCloses |
| IMAP-RESP-14 | RFC 3501 §7.1.5 | Normative | BYE announces a panic shutdown, an inactivity autologout, or a greeting that refuses the connection. | n/a: not fully supported yet. Autologout and panic BYE are proven in the server package (pkg/imap/server/session_autologout_test.go, session_panic_test.go), not on this harness; the refusal greeting is IMAP-GREET-02. Tracked in rest-mail/rest-mail-server#374. |
| IMAP-RESP-15 | RFC 3501 §7.3.2 | Normative | RECENT reports the number of messages with the `\Recent` flag. | n/a: not supported yet; the engine keeps no `\Recent` state and reports the unseen count (see TestSelectionExamples). Tracked in rest-mail/rest-mail-server#374. |
| IMAP-RESP-16 | RFC 3501 §7.4.2 | MUST | Header data is 7-bit, and non-textual body data is transfer-encoded to text before it is sent. | TestFetchHeaderSectionsSevenBit, TestFetchBodyDataSevenBit, TestFetchSizesDescribeSentOctets, TestFetchSevenBitMessageByteForByte, TestFetchBinaryKeepsStoredOctets, TestFetchUTF8SessionKeepsValidUTF8, TestControlOctetPartConverted |
| IMAP-RESP-17 | RFC 3501 §§4.3, 7.5 | Normative | A `+` continuation request asks for the rest of a command: before synchronizing literal octets and during IDLE. | TestCommandLiteralAccepted, TestAppendExample, TestIdleDoneAndNextCommand |
| IMAP-RESP-CLIENT-01 | RFC 3501 §§7, 7.1, 7.2.1, 7.2.6, 7.3.1, 7.3.2, 7.4.1, 7.4.2 | MUST | Client duties: accept any response at any time; record FLAGS, EXISTS, RECENT, EXPUNGE and FETCH updates; ignore unknown response codes and capability names; show ALERT text to the user. | n/a: client duties; this is a server engine. |
| IMAP-XCMD-01 | RFC 3501 §6.5 | Normative | An X-prefixed command the server does not define gets a tagged BAD in every state, and the session goes on. | TestExperimentalCommand |
| IMAP-XCMD-02 | RFC 3501 §6.5 | MUST | A command outside RFC 3501 has an `X` prefix. | n/a: the engine has no private commands; every other command it accepts (ENABLE, IDLE, MOVE, UNSELECT, NAMESPACE, GETQUOTA, GETQUOTAROOT) is defined by a published RFC. |

## RFC 4959: SASL-IR, the initial client response

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-SASLIR-01 | RFC 4959 §§3, 5 | Normative | SASL-IR is advertised wherever AUTH=PLAIN is, and AUTHENTICATE takes `auth-type [SP (base64 / "=")]`. | TestSASLIR |
| IMAP-SASLIR-02 | RFC 4959 §§3, 4 | Normative | A PLAIN initial response on the command line is used as the client's first response: the command completes with no continuation, as in the RFC's example. | TestSASLIR |
| IMAP-SASLIR-03 | RFC 4959 §3 | MUST | Clients that omit the initial response are still supported: the RFC 3501 continuation exchange works unchanged. | TestSASLIR |
| IMAP-SASLIR-04 | RFC 4959 §3 | MUST | `=` is a zero-length response; one that does not decode as base64, pad inside it or quoted, is handled as any client response that does not decode (NO); an empty or space-separated argument is BAD; none gets a continuation or leaves the session authenticated. | TestSASLIR |

## RFC 3516: BINARY

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-BINARY-01 | RFC 3516 §§4.1-4.3 | MUST | BINARY is advertised; FETCH BINARY[part] and BINARY.PEEK[part] answer the section with its Content-Transfer-Encoding removed: quoted-printable and base64 decoded; identity CTEs preserve opaque bytes, while supported textual leaves use canonical CRLF (IMAP-BINARY-08/11); BINARY[] is the whole stored message, a part that does not exist empty. | TestBinaryFetch |
| IMAP-BINARY-02 | RFC 3516 §4.3 | SHOULD | Decoded data holding a NUL is a literal8 (`~{n}`); other data is a literal. | TestBinaryFetch |
| IMAP-BINARY-03 | RFC 3516 §§4.2-4.3 | MUST | A partial counts octets of the decoded data and labels the origin (`BINARY[2]<2>`); BINARY.SIZE[part] is the decoded length BINARY sends, and sets no flag. | TestBinaryFetch |
| IMAP-BINARY-04 | RFC 3516 §4.2 | Normative | BINARY sets \Seen and reports it in the same FETCH, as BODY does; BINARY.PEEK does not. | TestBinarySeen |
| IMAP-BINARY-05 | RFC 3516 §4.3 | MUST | A section whose encoding cannot be removed (an unknown one, or base64 that does not decode) fails BINARY and BINARY.SIZE with NO [UNKNOWN-CTE] and no FETCH response. | TestBinaryUnknownCTE |
| IMAP-BINARY-06 | RFC 3516 §7 | Normative | section-binary is `"[" [section-part] "]"`: HEADER, TEXT, MIME, a zero or trailing-dot part, a BINARY.SIZE partial or the response-only `<n>` form is BAD with no response. | TestBinaryGrammar |
| IMAP-BINARY-07 | RFC 3516 §4.4 | MUST | APPEND's literal8 form, which may carry binary content the store cannot keep, fails with NO [UNKNOWN-CTE] before a continuation, and the session goes on. | TestBinaryAppend |

## RFC 8438: STATUS=SIZE

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-STATUSSIZE-01 | RFC 8438 §3 | MUST | STATUS=SIZE is advertised; STATUS, and LIST's STATUS return option, take SIZE, the mailbox's size in octets, no less than the sum of its messages' RFC822.SIZE (the A01 example: 44421), and 0 for an empty one; values are written as 63-bit integers. | TestStatusSize |
| IMAP-STATUSSIZE-02 | RFC 8438 §3 | SHOULD | With QUOTA, SIZE equals the storage usage that enforces the mailbox's STORAGE limit. | n/a: the quota root is the account, not one mailbox (GETQUOTAROOT names one root for every folder), so a folder's SIZE is its own share of the root's usage. |

## RFC 5819: LIST-STATUS

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-LISTSTATUS-01 | RFC 5819 §§2, 4 | MUST | LIST-STATUS is advertised; LIST's `STATUS (items)` return option follows each listed selectable mailbox's LIST with an untagged STATUS carrying the requested items in the order asked (the A01 example), beside SPECIAL-USE and in any case; without it no STATUS is sent. | TestListStatus |
| IMAP-LISTSTATUS-02 | RFC 5819 §2 | MAY | A mailbox whose STATUS the store cannot read is still listed, its STATUS dropped, and the command completes OK. | TestListStatus |
| IMAP-LISTSTATUS-03 | RFC 5819 §4; RFC 5258 §6 | Normative | `STATUS SP "(" status-att *(SP status-att) ")"`: an empty or unknown item, a missing list, doubled spaces or an unknown return option is BAD with no response. A repeated well-formed STATUS option follows RFC5258 deduplication (IMAP-LISTEXT-07). | TestListStatusRefusals |
| IMAP-LISTSTATUS-04 | RFC 5819 §2 | MUST NOT | A listed mailbox that vanishes before STATUS gets no STATUS and is marked `\Noselect` before LIST is emitted; recognize wrapped ErrNoSuchMailbox, preserve the connection, and omit unexpected-error STATUS without misclassifying the folder. Other selectability failures remain IMAP-LISTSTATUS-06. | TestListStatusVanishedMailbox |

## RFC 5182: SEARCHRES, the saved search result

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-SEARCHRES-01 | RFC 5182 §§2.1, 2.2 | MUST | SEARCHRES is advertised with ESEARCH, which it requires; SEARCH and UID SEARCH take the SAVE result option, which keeps the messages found and, alone, answers nothing untagged. | TestSearchRes |
| IMAP-SEARCHRES-02 | RFC 5182 §§2.1-2.3, 3 | MUST | `$` names the saved result wherever a sequence-set stands: FETCH, UID FETCH, SEARCH and UID SEARCH UID, alone or inside OR (examples 1 to 4), in message numbers or UIDs as the command takes; a SAVE pipelined with a command using `$` runs first. | TestSearchRes |
| IMAP-SEARCHRES-03 | RFC 5182 §2.4 | MUST | SAVE with MIN and/or MAX and neither ALL nor COUNT keeps only the messages MIN/MAX name; with ALL or COUNT it keeps every one found; the ESEARCH answer is the same as without SAVE (example 10). | TestSearchResSaveOptions |
| IMAP-SEARCHRES-04 | RFC 5182 §2.1 | MUST | A SAVE search answered NO empties the result; a BAD one, or a search without SAVE, leaves it; an expunged message leaves it; SELECT, EXAMINE, CLOSE and UNSELECT reset it. | TestSearchResVariable |
| IMAP-SEARCHRES-05 | RFC 5182 §§2.1, 2.2 | MUST | An empty `$` is a valid set that matches nothing: FETCH, COPY and STORE with it complete OK with no response (example 6). | TestSearchResVariable |
| IMAP-SEARCHRES-06 | RFC 5182 §3 | Normative | `$` is a whole sequence-set, not a number inside one: `1,$`, `$:5`, `$,2` and `1:$` are BAD. | TestSearchResGrammar |

## RFC 4731: ESEARCH, SEARCH result options

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-ESEARCH-01 | RFC 4731 §§3.1, 4; RFC 4466 §2.6 | Normative | ESEARCH is advertised; SEARCH and UID SEARCH take SP "RETURN" SP "(" [option *(SP option)] ")" before the criteria (CHARSET included), the option names in any case. | TestESearch |
| IMAP-ESEARCH-02 | RFC 4731 §3.1; RFC 4466 §2.6.2 | MUST | With result options the command answers one untagged ESEARCH, correlated by `(TAG "<tag>")`, instead of SEARCH; without them it answers SEARCH as before. | TestESearch |
| IMAP-ESEARCH-03 | RFC 4731 §3.1 | MUST | MIN and MAX are the lowest and highest match, ALL every match as a sequence-set, COUNT their number; an empty list is ALL; UID SEARCH's ESEARCH carries the UID indicator and answers in UIDs (the A282-A286 examples). | TestESearch |
| IMAP-ESEARCH-04 | RFC 4731 §3.1 | MUST | With no match the ESEARCH response is still sent; MIN, MAX and ALL are left out, and COUNT, when asked, is 0. | TestESearchNoMatches |
| IMAP-ESEARCH-05 | RFC 4466 §2.6; RFC 3501 §9 | Normative | A RETURN list that is malformed, misspaced, not followed by criteria, or names an option the server does not offer (PARTIAL) is BAD with no response. | TestESearchRefusals |

## RFC 5258: LIST-EXTENDED

Every folder counts as subscribed in this engine (LSUB says so too), so
`\Subscribed` and SUBSCRIBED selection follow existence; REMOTE adds nothing,
as there are no remote mailboxes.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-LISTEXT-01 | RFC 5258 §§3, 4 | MUST | LIST-EXTENDED is advertised; the CHILDREN return option gives each reply exactly one of \HasChildren and \HasNoChildren (example 3); REMOTE is accepted and lists no remote mailboxes (example 4); option names are case-insensitive. | TestListExtendedChildren |
| IMAP-LISTEXT-02 | RFC 5258 §§3, 3.3; RFC 5819 §2 | MUST | \NonExistent is accurate: a level that is not a mailbox, matched, with a descendant that meets the selection and is not matched, is returned \NonExistent \HasChildren (example 11) and gets no STATUS. | TestListExtendedChildren, TestListExtendedRecursiveMatch |
| IMAP-LISTEXT-03 | RFC 5258 §§3.1-3.3 | MUST | The SUBSCRIBED selection option lists the subscribed names with \Subscribed and implies the return option; the SUBSCRIBED return option marks them on any LIST; attributes are accurate and additive. | TestListExtendedSubscribed |
| IMAP-LISTEXT-04 | RFC 5258 §§3.1, 3.5 | MUST | RECURSIVEMATCH also returns a matched name whose descendant meets the selection without being matched, with CHILDINFO naming the criteria (example 8); CHILDINFO is never sent without RECURSIVEMATCH. | TestListExtendedRecursiveMatch |
| IMAP-LISTEXT-05 | RFC 5258 §3 | SHOULD | Several patterns in parentheses: a name matching any is answered once (example 7); an extended LIST ignores an empty pattern instead of treating it as the delimiter request. | TestListMultiplePatterns |
| IMAP-LISTEXT-06 | RFC 5258 §§3, 3.1 | MUST | An unknown selection or return option is BAD; RECURSIVEMATCH alone or with only REMOTE is BAD; an unterminated pattern list is BAD. | TestListExtendedRefusals |

## RFC 2087: account-wide QUOTA reporting

The backend exposes one account-wide root named by the empty astring, with
STORAGE usage and limit in bytes. It has no quota setter, extra resource model
or separate unlimited marker. Read-side proofs below do not claim those features.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-QUOTA-01 | RFC 2087 §§3, 6 | MUST | Advertised QUOTA commands and resource names are case-insensitive; the supported account root is the empty astring. | TestQuotaExchange |
| IMAP-QUOTA-02 | RFC 2087 §§4.2, 4.3, 6; RFC 3501 §§4.3, 9 | Normative | GETQUOTA and GETQUOTAROOT take exactly one astring, with quoted escapes or a counted literal; malformed, missing or extra arguments get BAD before quota access. | TestQuotaArguments, TestQuotaLiteralArguments |
| IMAP-QUOTA-03 | RFC 2087 §§4.2, 4.3 | Normative | An unknown root or mailbox gets NO without fabricated quota data; mailbox existence is checked before reading its root usage. | TestQuotaArguments, TestQuotaLookupFailures |
| IMAP-QUOTA-04 | RFC 2087 §§3, 4.2, 5.1 | Normative | GETQUOTA returns the root and STORAGE usage/limit triplet in 1024-octet units, followed by tagged OK. | TestQuotaExchange |
| IMAP-QUOTA-05 | RFC 2087 §§4.3, 5.2, 6 | Normative | GETQUOTAROOT decodes its mailbox astring, emits that mailbox and its empty-name account root as properly encoded astrings, then emits the root quota before OK. | TestQuotaExchange, TestQuotaLiteralArguments |
| IMAP-QUOTA-06 | RFC 2087 §8; RFC 3501 §6.3 | Normative | Unauthenticated requests reveal no quota values and do not call the authenticated account's quota backend. | TestQuotaExchange |
| IMAP-QUOTA-07 | RFC 2087 §§4.2, 4.3 | Normative | Folder-list or quota lookup failure returns NO without leaking backend details or emitting a partial QUOTAROOT/QUOTA reply. | TestQuotaLookupFailures |
| IMAP-QUOTA-08 | RFC 2087 §6; RFC 3501 §9 (number) | Normative | Resource triplets contain unsigned 32-bit numbers; negative or overflowing backend values are refused before any untagged reply, while the maximum is representable. | TestQuotaValueBounds |
| IMAP-QUOTA-09 | RFC 2087 §4.1 | Normative | SETQUOTA replaces all limits of the named root and returns the resulting quota. | n/a: not implemented. Mailbox has only a read-side Quota method and no quota mutation/administrator interface; SETQUOTA remains an unknown command. This is a known extension limitation, not a completed configuration feature; rest-mail/rest-mail-server#374. |
| IMAP-QUOTA-10 | RFC 2087 §5.1 | Normative | Omit resources without administrative limits; an empty resource list means the root has no resource limits. | n/a: the current backend always returns one STORAGE usage/limit pair and cannot distinguish an unlimited resource from a zero-byte cap. The existing zero-value interpretation is preserved, not silently decided by this audit; rest-mail/rest-mail-server#374. |
| IMAP-QUOTA-11 | RFC 2087 §3 | Normative | Mailboxes sharing a named root share its resource limits and actual resource accounting. | n/a: enforcement and durable accounting belong to the host backend. The engine reports the authenticated account's single Quota result but cannot enforce storage mutations performed outside it. |
| IMAP-QUOTA-12 | RFC 2087 §3 | MAY | Support implementation-defined resource types and multiple named roots. | n/a: the backend deliberately exposes only account-wide STORAGE with the empty root name; other resource/root types are not modeled. |

## RFC 2342: NAMESPACE

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-NAMESPACE-01 | RFC 2342 §§4, 5 | Normative | NAMESPACE is advertised, and answers one untagged NAMESPACE before OK: the personal namespace with prefix "" and the hierarchy delimiter LIST gives ("/"), and NIL for the other users' and shared classes, which the server does not have (example 5.1), in the authenticated and selected states. | TestNamespace |
| IMAP-NAMESPACE-02 | RFC 2342 §5 | Normative | NAMESPACE takes no arguments (BAD otherwise), and before authentication answers NO with no NAMESPACE response. | TestNamespace, TestNamespaceNoArguments |

## RFC 7888: LITERAL-, non-synchronizing literals up to 4096 octets

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-LITMINUS-01 | RFC 7888 §5 | MUST NOT | Advertise LITERAL- and never LITERAL+ beside it. | TestLiteralMinus |
| IMAP-LITMINUS-02 | RFC 7888 §3 | MUST | A non-synchronizing literal gets no continuation; its octets and the line after them are part of the same command, as in the RFC's LOGIN example. | TestLiteralMinus |
| IMAP-LITMINUS-03 | RFC 7888 §§4, 5 | Normative | A non-synchronizing literal of up to 4096 octets is taken as a command argument and as APPEND's message; a synchronizing literal of any size the command allows is unaffected. | TestLiteralMinusLimit |
| IMAP-LITMINUS-04 | RFC 7888 §§4, 5 | MUST | One over 4096 octets, as a command argument, APPEND's mailbox or APPEND's message, gets a tagged BAD with TOOBIG and a BYE with TOOBIG, and the session ends without running its in-flight octets. | TestLiteralMinusLimit, TestAppendRejectedNonSynchronizing |

## Session negotiation extension reconciliation (2026-10-26)

[RFC-SESSION-EXTENSIONS.md](RFC-SESSION-EXTENSIONS.md) reconciles every numbered
section of seven related specifications, including non-keyword protocol rules,
client duties, unsupported behavior and host policy. These additional rows do
not replace the earlier narrow proofs or make the overall inventory complete.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-TLS-AUDIT-01 | RFC 2595 §2.1 | REQUIRED | Implement the historical mandatory DHE-DSS/3DES cipher suite. | n/a: deliberately unsupported by the Go TLS implementation; obsolete TLS/cipher requirements are not re-enabled. Modern policy belongs to the host tls.Config. This is a recorded historical deviation, not a conformance proof. |
| IMAP-TLS-AUDIT-02 | RFC 2595 §§2.2–2.3 | SHOULD | Offer server-wide and per-user controls for refusing unprotected credentials. | n/a: the engine requires TLS for every user whenever a TLS config is supplied, but has no per-user privacy policy hook. A nil TLS config is the explicit development/legacy cleartext mode; rest-mail/rest-mail-server#374 retains this boundary. |
| IMAP-TLS-AUDIT-03 | RFC 2595 §§2.4, 3.1, 3.2, 9 | MUST | Clients verify the connected server name, reject unsafe names, refresh capabilities after TLS and avoid LOGIN when LOGINDISABLED is present. | n/a: client-only hostname, cache, downgrade-warning and credential-transmission duties; this package implements the server. Server counterparts remain IMAP-STARTTLS-01 through IMAP-STARTTLS-08. |
| IMAP-TLS-AUDIT-04 | RFC 2595 §§2.5, 9 | MUST | Check that negotiated TLS meets local authentication and privacy policy, including rejecting weak configurations. | n/a: the engine terminates failed handshakes (IMAP-STARTTLS-07), but acceptable TLS versions, cipher suites, certificates and client-certificate verification are supplied by the host tls.Config. No host-policy enforcement beyond that configuration is claimed. |
| IMAP-TLS-AUDIT-05 | RFC 2595 §§4–5, 8 | Normative | Provide POP3 STLS and ACAP STARTTLS negotiation, refreshed greetings and their capability registrations. | n/a: these are other application protocols; this library serves IMAP only. No POP3 or ACAP behavior is inferred from IMAP tests. |
| IMAP-TLS-AUDIT-06 | RFC 2595 §7 | Normative | Prefer STARTTLS over separate implicit-TLS ports in the historical deployment guidance. | n/a: port/deployment policy is outside Session; this historical recommendation is superseded by RFC8314 implicit-TLS guidance. Server supports host-selected listeners; no port policy changes are made here. |
| IMAP-PLAIN-01 | RFC 4616 §2 | MUST | Accept only well-formed UTF-8 PLAIN data with exactly two NUL delimiters, rejecting empty authentication identity/password and additional NULs before any backend call. | TestPlainMalformedPayloads |
| IMAP-PLAIN-02 | RFC 4616 §2 | Normative | An explicit authorization identity must be authorized; an unsupported different identity is refused, leaving the session unauthenticated and able to retry. | TestPlainMalformedPayloads |
| IMAP-PLAIN-03 | RFC 4616 §2 | MUST | Accept at least 255 octets each for authzid, authcid and password, including multibyte UTF-8, both with and without an initial response. | TestPlainRequiredCredentialLengths |
| IMAP-PLAIN-04 | RFC 4616 §2 | Normative | Prepare presented and stored credentials consistently before comparison or hashing; a failed or empty prepared value must fail verification. | n/a: Backend.Authenticate owns stored credentials, normalization/preparation and hashing. No SASLprep dependency or preparation policy is imposed by the engine; pass-through preparation is permitted by RFC4616. The backend must reject preparation failures and empty prepared output. |
| IMAP-PLAIN-05 | RFC 4616 §2 | Normative | Derive an omitted authorization identity from the prepared authentication identity and apply the application authorization policy. | n/a: the host backend returns the authenticated Mailbox and owns canonical identity derivation. The engine only supports an empty or byte-identical explicit authzid, not delegated identities or a separate authorization hook. |
| IMAP-PLAIN-06 | RFC 4616 §§1, 5 | SHOULD | Advertise and use PLAIN by default only when adequate data security services are active. | n/a: TLS-configured sessions enforce the protected default (IMAP-STARTTLS-01/08), but a nil TLS config intentionally permits development/legacy cleartext PLAIN and LOGIN. This documented exception is not a universal secure-default claim. |
| IMAP-SASLIR-05 | RFC 4959 §6 | MUST | The parser accepts the full encoded PLAIN response required for three 255-octet components on one command line, as well as the continuation exchange. | TestPlainRequiredCredentialLengths |
| IMAP-SASLIR-06 | RFC 4959 §3 | MUST | Reject non-base64 characters, embedded CR/TAB and misplaced padding in either response form before backend authentication; a later valid retry succeeds. | TestPlainBase64Alphabet |
| IMAP-SASLIR-07 | RFC 4959 §§3, 6 | MUST NOT | Clients send an initial response only when SASL-IR is advertised, otherwise falling back to a continuation exchange. | n/a: client-only capability/fallback duty; the server supports omission through IMAP-SASLIR-03 and the required-length proof. |
| IMAP-SASLIR-08 | RFC 4959 §3 | MUST | Reject an initial response for a supported SASL mechanism that cannot take one with tagged BAD. | n/a: the only supported mechanism is PLAIN, which accepts an initial response; there is no supported non-initial-response mechanism. Unknown mechanisms receive NO and are not advertised (IMAP-AUTHENTICATE-04). |
| IMAP-ENABLE-07 | RFC 5161 §3.1 | Normative | Permit LOGIN followed immediately by ENABLE and ENABLE followed immediately by SELECT, preserving enabled mode and ordered completions. | TestEnablePipelinedLoginAndSelect |
| IMAP-ENABLE-08 | RFC 5161 §§3.1, 3.3 | MUST NOT | Clients issue ENABLE only before selecting a mailbox, and extension designers avoid unnecessary ENABLE requirements. | n/a: client/designer duties; servers need not police selection history. The server ignores UTF8 enablement while selected, and no new enable-able extension is introduced. |
| IMAP-IDLE-03 | RFC 2177 §3 | Normative | While IDLE owns the continuation, report newly delivered mail, external expunges and flag changes using current sequence numbers; DONE completes IDLE before the next command. | TestIdleMailboxNotificationTranscript |
| IMAP-IDLE-04 | RFC 2177 §§3–4 | Normative | IDLE is an authenticated/selected-state command and may return NO when the server will not allow it at that time. | n/a: this implementation requires a selected mailbox and returns NO otherwise. It does not offer account-wide IDLE events in the authenticated-but-unselected state. This is its explicit NO policy, not a positive proof for both permitted states. |
| IMAP-IDLE-05 | RFC 2177 §3 | MUST NOT | Clients use IDLE only when advertised, send no commands before DONE, still accept unsolicited updates and renew before 29 minutes if needed. | n/a: client-only duties. Immediate server completion/order is IMAP-IDLE-02, and the server inactivity timer remains IMAP-AUTOLOGOUT-01; no wall-clock timer claim is added. |
| IMAP-LITMINUS-05 | RFC 7888 §3 | Normative | The two counted non-synchronizing LOGIN strings in the RFC example preserve their embedded spaces without continuations and leave a pipelined next command intact. | TestLiteralLoginRFC7888Example |
| IMAP-LITMINUS-06 | RFC 7888 §3 | MUST NOT | Send non-synchronizing literals from server to client; message, header and body FETCH responses retain synchronizing literal framing. | TestServerLiteralResponsesSynchronize |
| IMAP-LITMINUS-07 | RFC 7888 §§6, 8 | Normative | Allow the extended non-synchronizing literal8 syntax when BINARY and LITERAL- are both supported. | n/a: binary APPEND storage is unsupported and is refused with UNKNOWN-CTE (IMAP-BINARY-07); optional UTF8 data-item syntax is covered by IMAP-UTF8-04. This audit does not claim binary storage or every literal8 combination is implemented. |
| IMAP-LITMINUS-08 | RFC 7888 §7 | Normative | Use LITERAL- with MULTIAPPEND as with LITERAL+. | n/a: MULTIAPPEND is not implemented or advertised. Existing single-message APPEND framing is not proof of multiple-message atomicity; rest-mail/rest-mail-server#374. |

## Mailbox discovery and status reconciliation (2026-10-26)

[RFC-MAILBOX-EXTENSIONS.md](RFC-MAILBOX-EXTENSIONS.md) records the grouped
five-spec section review, source pins, corrected historical LIST-STATUS rows
and the remaining backend/client/unsupported boundaries. Existing IDs are kept;
no source case is silently dropped or counted as complete.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-NAMESPACE-03 | RFC 2342 §§2, 5, 7 | SHOULD NOT | Expose other users or their mailbox names without an explicit grant; put their user identifier beneath the other-users prefix. | n/a: other-users and shared namespaces are not offered. The host Backend returns the authenticated user's mailbox and must filter inaccessible names; no new sharing or ACL model is selected. |
| IMAP-NAMESPACE-04 | RFC 2342 §§5–6 | MUST | Use modified UTF-7 for international namespace prefixes and prefix non-standard response extensions with X-. | n/a: the fixed single personal namespace has an empty ASCII prefix and no response extensions. Configurable prefixes, alternate delimiters and extension attributes are not exposed by the backend interface. |
| IMAP-NAMESPACE-05 | RFC 2342 §§3, 5 | Normative | Clients handle multiple namespaces, permit manual prefixes and resolve ambiguous target namespaces with the user. | n/a: client-only discovery, configuration and mailbox-creation decisions; the server offers the single personal namespace in IMAP-NAMESPACE-01. |
| IMAP-SPECIALUSE-10 | RFC 6154 §§2, 6 | MUST NOT | Return only standardized special-use attributes, covering all seven known roles, repeated roles on different folders and folders without a recognized role. | TestSpecialUseCompleteAttributeSet |
| IMAP-SPECIALUSE-11 | RFC 6154 §2 | MAY | A mailbox may have multiple special uses, and uses may be specific to the authenticated user. | n/a: Folder.SpecialUse represents at most one role per mailbox. Multiple folders may share a role; user-specific folder/role presentation is the host mailbox view. The optional multiple-role representation is not implemented. |
| IMAP-SPECIALUSE-12 | RFC 6154 §§3–4, 7 | MUST NOT | When optional CREATE USE or METADATA assignment is supported, validate roles, avoid invalid side effects and refuse a failed assignment without creating the mailbox. | n/a: CREATE-SPECIAL-USE and METADATA are not implemented or advertised; no role-assignment backend operation is available. Existing optional gaps IMAP-SPECIALUSE-08/09 remain, and METADATA is deliberately excluded by the owner. |
| IMAP-SPECIALUSE-13 | RFC 6154 §6 | MUST | Clients ignore LIST attributes they do not understand. | n/a: client-only forward-compatibility duty. Server filtering of unsupported role attributes is IMAP-SPECIALUSE-10. |
| IMAP-SPECIALUSE-14 | RFC 6154 §§5.3–5.4 | Normative | Exercise the CREATE USE and METADATA assignment example exchanges. | n/a: those optional commands are not supported; the examples are explicitly outside the offered LIST-only role-discovery behavior. No passing transcript is inferred from ordinary LIST tests. |
| IMAP-LISTEXT-07 | RFC 5258 §3 | MUST | Treat repeated STATUS return options as one occurrence: preserve the first parameter list, validate repeated syntax and perform one metadata lookup per returned mailbox. | TestListRepeatedStatusOptions |
| IMAP-LISTEXT-08 | RFC 5258 §§1, 3–3.5, 4 | MUST | Clients use advertised options, infer stronger attributes, ignore unknown extended fields and tolerate children changing after a LIST reply. | n/a: client-only discovery, attribute-inference and race-handling obligations; no client implementation lives in this server engine. |
| IMAP-LISTEXT-09 | RFC 5258 §§3.1–3.3 | MUST | Subscription attributes accurately distinguish subscribed existing names, subscribed missing names and unsubscribed parents. | n/a: the deliberate subscription model treats every existing folder as subscribed and retains no subscriptions to missing folders. Actual subscription persistence and the corresponding example variants are not implemented; no owner design is changed. |
| IMAP-LISTEXT-10 | RFC 5258 §3.1 | Normative | REMOTE includes remote mailboxes and accurately reports their remote attribute. | n/a: the backend exposes only local folders, with no remote-mailbox or referral model. REMOTE is accepted but cannot produce remote entries; the current boundary is preserved. |
| IMAP-LISTEXT-11 | RFC 5258 §§4, 8 | SHOULD NOT | Expose child-existence information when the authenticated user can access none of those children. | n/a: the host must filter Folders to the authenticated user's permitted view. Folder has no ACL/selectability metadata; the engine cannot audit inaccessible rows supplied by an incorrect host. |
| IMAP-LISTEXT-12 | RFC 5258 §5 | Normative | Cover the full eleven-example hierarchy, including selective/missing subscriptions, remote folders, alternate child-inference attributes and their variants. | n/a: existing tests cover supported hierarchy, recursive-match, patterns and children slices. The full example family needs persistent subscriptions and remote/ACL metadata, which are outside the offered backend. This is not a claim that all eleven examples run verbatim. |
| IMAP-LISTSTATUS-05 | RFC 5819 §§2–3 | MUST NOT | Return STATUS for a parent included only because a descendant meets selection; query only mailboxes that themselves match the selection criteria. | TestListStatusRecursiveSelection |
| IMAP-LISTSTATUS-06 | RFC 5819 §2 | MUST | Mark every mailbox that cannot be selected with Noselect and omit its STATUS, including permission-denied races. | n/a: ErrNoSuchMailbox is recognized and proved by IMAP-LISTSTATUS-04. Other permission/selectability failures have no typed backend representation, so arbitrary storage errors may only drop STATUS. A broader ACL/selectability contract remains in rest-mail/rest-mail-server#374. |
| IMAP-LISTSTATUS-07 | RFC 5819 §5 | Normative | Large LIST-STATUS requests must allow other IMAP connections to make progress. | n/a: each Session has its own goroutine, but Folders and Messages are synchronous host calls with no context or fairness hook. Host storage/resource scheduling and a global load guarantee are outside this wire proof; rest-mail/rest-mail-server#374. |
| IMAP-STATUSSIZE-03 | RFC 8438 §§3–4 | MUST | Emit an unsigned 63-bit SIZE without negative metadata or overflow; accept totals above 32 bits and at the 63-bit maximum, refusing invalid direct STATUS data and omitting invalid LIST-STATUS data. | TestStatusSizeNumericBounds |
| IMAP-STATUSSIZE-04 | RFC 8438 §3 | MUST | Clients accept SIZE values throughout the unsigned 63-bit range. | n/a: client-only integer-width requirement. The server numeric boundary and wide-sum proofs are IMAP-STATUSSIZE-03; Message.Size is a native int supplied by the host. |

## Search and message extension reconciliation (2026-10-26)

[RFC-MESSAGE-EXTENSIONS.md](RFC-MESSAGE-EXTENSIONS.md) records the grouped
six-spec section review, source pins, narrow proofs and remaining boundaries.
Existing requirement IDs and test cases remain; BINARY text and grammar-section
commentary is corrected with the canonical source evidence.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-BINARY-08 | RFC 3516 §§4.2–4.3, 6 | MUST | Canonicalize bare CR/LF in supported ASCII-compatible textual leaves after CTE decoding and before BINARY.SIZE or partial offsets; preserve existing CRLF, UTF-8 octets, absent final newline, stored BODY bytes and flags. | TestBinaryTextCanonicalLineEndings, TestBinaryCanonicalTextBoundaries |
| IMAP-BINARY-09 | RFC 3516 §§3, 4.3 | Normative | Preserve opaque decoded bytes and select literal8 when NUL is present; do not apply byte-oriented text repair to nontext, NUL-bearing, UTF-16 or unknown-charset payloads. | TestBinaryOpaqueOctetsPreserved |
| IMAP-BINARY-10 | RFC 3516 §5 | MUST NOT | Decode RFC2047 encoded-word headers as though they were Content-Transfer-Encoding; whole-message BINARY preserves those encoded header octets. | TestBinaryEncodedHeadersStayEncoded |
| IMAP-BINARY-11 | RFC 3516 §6 | MUST | Transmit every textual line-oriented section with canonical CRLF regardless of its stored representation. | n/a: the new repair covers non-NUL TEXT leaves with absent/US-ASCII/UTF-8/ISO-8859-1 charset. Whole-message/container framing and other charsets still require a canonical host representation. Arbitrary charset-aware/container repair is not implemented; rest-mail/rest-mail-server#374. |
| IMAP-BINARY-12 | RFC 3516 §6 | MUST | Describe binary-stored content using a base-IMAP-compatible BODYSTRUCTURE CTE and return BODY content matching that description. | n/a: the library preserves host message representations and does not downgrade or re-encode binary bodies for base clients. This deliberately unsupported rewriting is not inferred from BINARY decoding tests; rest-mail/rest-mail-server#374. |
| IMAP-BINARY-13 | RFC 3516 §§4.4, 6 | MUST NOT | Lose data when rewriting the CTE of an accepted binary APPEND; avoid gratuitous rewrites that invalidate signatures. | n/a: binary APPEND storage and CTE rewriting are not offered. The permitted refusal path is IMAP-BINARY-07; no lossless binary storage or rewriting claim is added. |
| IMAP-BINARY-14 | RFC 3516 §6 | SHOULD | Clients retain their own content-transfer decoding fallback and avoid unnecessary expensive size requests. | n/a: client-only decoding and request-selection guidance; this package implements the server. |
| IMAP-ESEARCH-06 | RFC 4731 §3.2 | MUST | With CONDSTORE, return the appropriate MODSEQ value with ESEARCH MIN/MAX/ALL/COUNT combinations. | n/a: CONDSTORE and modification sequences are not implemented or advertised; tracked by rest-mail/go-imap#162. No partial result-option support implies MODSEQ support. |
| IMAP-ESEARCH-07 | RFC 4731 §3.1 | MUST NOT | Clients assume a particular message/UID order in an ESEARCH ALL sequence set. | n/a: client-only response interpretation. The engine currently returns the host snapshot order, assuming the backend ascending-UID contract; that assumption is not independently enforced. |
| IMAP-ESEARCH-08 | RFC 4731 §5 | Normative | Bound expensive search work and optionally optimize MIN/MAX without changing ALL/COUNT semantics. | n/a: a search scans the selected host snapshot and may load complete messages. Host storage/resource budgets and indexed execution are outside this wire inventory; no global denial-of-service or optimization claim is made. |
| IMAP-SEARCHRES-07 | RFC 5182 §§2.1–2.3; RFC 4315 §2.1 | MUST | Execute dependent SAVE/COPY/STORE/UID EXPUNGE in arrival order, using the saved identities, removing only saved Deleted messages and preserving unrelated deletion marks. | TestSearchResPipelineThroughUIDExpunge |
| IMAP-SEARCHRES-08 | RFC 5182 §2.1 | Normative | Reset the saved search result when UIDVALIDITY changes while the mailbox remains open. | n/a: the engine does not change or observe a new UIDVALIDITY while selected. Persistent mailbox identity and live metadata invalidation require a backend contract; rest-mail/rest-mail-server#374 and #804 remain. |
| IMAP-SEARCHRES-09 | RFC 5182 §§2.5, 4 | MUST | If a save is refused for a resource limit, send NO [NOTSAVED] and empty the saved result. | n/a: the optional save-limit/refusal mechanism is not implemented. One saved UID slice is retained per session with no global budget; there is no offered refusal path to claim as tested. |
| IMAP-SEARCHRES-10 | RFC 5182 §2.1 | MUST NOT | A NO search without SAVE changes the saved result; a NO search with SAVE clears it, following the normative rule despite the printed example 5 omitting SAVE. | TestSearchResNonSaveFailurePreservesVariable |
| IMAP-UIDPLUS-AUDIT-01 | RFC 4315 §§2.1, 3 | Normative | Clients without UIDPLUS use safe STORE/EXPUNGE fallback or discover appended/copied identities through SELECT/FETCH/SEARCH. | n/a: client-only fallback and identity-discovery duties; the server capability/response proofs remain IMAP-UIDPLUS-CAP-01, IMAP-APPENDUID and IMAP-COPY rows. |
| IMAP-UIDPLUS-AUDIT-02 | RFC 4315 §§3–4 | MUST NOT | For MULTIAPPEND responses, include extraneous UIDs or star and preserve the appended-message order; a single append still uses one UID. | n/a: MULTIAPPEND is not implemented or advertised. Single-message APPENDUID is proved separately; multi-message ordering/atomicity is not inferred from it. |
| IMAP-UIDPLUS-AUDIT-03 | RFC 4315 §§3, 5, 9 | SHOULD NOT | Use nonpersistent UIDNOTSTICKY stores; if present, disclose their status and suppress misleading COPYUID/APPENDUID or unreadable-destination metadata. | n/a: the backend exposes no UIDNOTSTICKY or destination-readability state. Existing IMAP-APPENDUID-05/06 and IMAP-COPY-15 retain the privacy gap; persistent identity remains a host obligation and no new policy is selected. |
| IMAP-MOVE-15 | RFC 6851 §3.3 | MUST NOT | Synthesize STORE responses or set Deleted as a MOVE side effect; preserve unrelated deletion marks and emit UIDPLUS mapping before EXPUNGE. | TestMoveDoesNotSynthesizeStore |
| IMAP-MOVE-16 | RFC 6851 §§4.1, 6 | Normative | Apply quota and target security/scanning policy correctly when a same-root MOVE differs from COPY. | n/a: Mailbox.Move/Mover.MoveUID own storage, quotas and scanning/auditing. The engine cannot infer temporary quota suspension or target security policy; no host policy is silently changed. |
| IMAP-MOVE-17 | RFC 6851 §4.2 | Normative | Enforce the union of UID STORE, UID COPY and UID EXPUNGE ACL rights for MOVE. | n/a: ACL is not implemented or advertised and the backend owns authorization. The engine has no ACL-rights query or independent union check. |
| IMAP-MOVE-18 | RFC 6851 §4.4 | MUST | With QRESYNC/modification-sequence support, emit VANISHED and update source/target mod-sequences and HIGHESTMODSEQ as required. | n/a: QRESYNC/CONDSTORE and modification sequences are not implemented; rest-mail/go-imap#162. Existing EXPUNGE dispatch is not proof of these conditional obligations. |
| IMAP-MOVE-19 | RFC 6851 §4.5 | Normative | Apply IMAP Sieve COPY events to MOVE without producing a FLAG event. | n/a: IMAP Sieve event hooks are not part of the backend or engine. Per-message movement and host event integration remain separate responsibilities. |
| IMAP-UTF8-06 | RFC 6855 §§3, 6, 9 | MUST NOT | Emit UTF-8 mailbox names before client opt-in: LIST/LSUB retain modified UTF-7 until ENABLE and then return counted UTF-8 names; advertise ACCEPT without ONLY or obsolete UTF8 capabilities. | TestUTF8MailboxOutputRequiresEnable |
| IMAP-UTF8-07 | RFC 6855 §6 | Normative | In UTF8=ONLY mode, require ENABLE before commands needing UTF-8 and reject them with NO [CANNOT]; never advertise ONLY and ACCEPT together. | n/a: UTF8=ONLY is not implemented or advertised. The offered optional ACCEPT mode and its capability boundary are proved separately. |
| IMAP-UTF8-08 | RFC 6855 §§7–8 | Normative | Present a base-compatible message/header representation to legacy clients and use standardized downgrade algorithms if surrogates are constructed. | n/a: message/header downgrading, surrogate caching and capability-dependent identity policy are deliberately unsupported. Stored messages are sent as supplied by the host; no legacy-client safety or surrogate UIDVALIDITY claim is made. |
| IMAP-UTF8-09 | RFC 6855 §3 | MUST | Mailbox names meet the full Net-Unicode normalization requirements as well as the control-character exclusions. | n/a: existing IMAP-UTF8-02 rejects forbidden controls and accepts valid UTF-8, but NFC normalization/equivalence is not enforced by the standard-library-only engine. Host naming policy and rev2 design remain rest-mail/rest-mail-server#374/#718. |
| IMAP-UTF8-10 | RFC 6855 §4 | Normative | Accept the UTF8 data item inside CATENATE when both extensions are advertised. | n/a: CATENATE is not implemented or advertised. Ordinary APPEND UTF8 data items remain IMAP-UTF8-04/05. |
| IMAP-UTF8-11 | RFC 6855 §§3, 5, 7, 10 | MUST | Clients ENABLE ACCEPT, use AUTHENTICATE for UTF-8 credentials, omit SEARCH CHARSET after enabling and discard incompatible legacy caches after upgrading. | n/a: client-only negotiation, credential-command and cache duties. Host identity/preparation policy is separate; server syntax and SEARCH refusal counterparts are IMAP-SYNTAX-07 and IMAP-UTF8-03. |

## RFC 3501 §9: formal syntax — partial slice

§9's grammar for the arguments of each command is in that command's rows above
(sequence sets, astrings, flag lists, dates, fetch items, search keys). Rows
here cover the rules every command line and response shares.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-SYNTAX-01 | RFC 3501 §9 (resp-text, text) | Normative | A status response never ends at its response code: SP and at least one character of text follow it, on SELECT's and EXAMINE's UNSEEN, UIDVALIDITY, UIDNEXT and PERMANENTFLAGS lines, on MOVE's COPYUID line and on every tagged completion. | TestResponseCodesCarryText |
| IMAP-SYNTAX-02 | RFC 3501 §9 (tag) | Normative | A tag is one or more ASTRING-CHARs other than `+`: SP, CTL, 8-bit octets, `(`, `)`, `{`, `%`, `*`, DQUOTE, backslash and `+` make the line an untagged BAD whose command does not run; `]` and other ATOM-CHARs are accepted. | TestTagGrammar, TestTaggedCompletion |
| IMAP-SYNTAX-03 | RFC 3501 §9 (command-any, command-nonauth, command-select, SP); RFC 2177; RFC 3691 | Normative | CAPABILITY, LOGOUT, NOOP, STARTTLS, CHECK, CLOSE, EXPUNGE, IDLE and UNSELECT end at their name: anything after it, even one trailing space, gets BAD and the command does not run. | TestCommandsWithoutArguments |
| IMAP-SYNTAX-04 | RFC 3501 §§6.2.3, 9 (login, astring) | Normative | LOGIN takes exactly userid SP password, each an astring: an atom with an atom-special or an 8-bit octet, a quoted string with any escape but `\"` and `\\`, an unterminated or adjoined quote, or a missing, extra or doubled separator gets BAD without the credentials being checked. | TestLoginArguments |
| IMAP-SYNTAX-05 | RFC 3501 §9 (notes 1-3) | MUST | Command names and keywords are accepted in any case; SP is exactly one space, so a doubled space or a TAB gets BAD; a NUL gets BAD. | TestCaseAndSpacing |
| IMAP-SYNTAX-06 | RFC 3501 §9; RFC 6855 §3 | MUST | Without UTF8=ACCEPT, reject typed 8-bit atoms and quoted strings with BAD before soliciting a synchronizing literal, including APPEND and command continuations. Literal octets remain exempt; unread non-synchronizing payloads require BYE and closure. | TestUnsupportedUTF8CommandText, TestUnsupportedUTF8AppendText |
| IMAP-SYNTAX-07 | RFC 3501 §9; RFC 6855 §5 | MUST | Do not extend LOGIN to typed UTF-8 credentials. Reject quoted or atom 8-bit username/password text before backend authentication; SASL AUTHENTICATE PLAIN can carry UTF-8 credentials. | TestUTF8CredentialsRequireAuthenticate |
| IMAP-SYNTAX-08 | RFC 3501 §§6.4.5–6.4.7, 9 (nz-number, seq-range) | Normative | Preserve unsigned 32-bit range endpoints on 32-bit and 64-bit hosts: forward and reversed ranges above signed-int maximum select the same existing messages, while ranges wholly above the snapshot and absent single numbers select none. FETCH, STORE and COPY dispatch only the selected messages. | TestWideSequenceDispatch |
| IMAP-SYNTAX-09 | RFC 3501 §§4.1, 9 | Normative | Command names stay ASCII regardless of UTF8=ACCEPT; Unicode case equivalents never execute SELECT, SEARCH, STORE or CLOSE. Refuse before literal continuation, closing with BYE if a non-synchronizing literal is unread. | TestCoreCommandNamesAreASCII, TestCoreTextRejectionLiteralBoundaries |
| IMAP-SYNTAX-10 | RFC 3501 §§4.1, 4.3, 9; RFC 6855 §3 | Normative | UTF8=ACCEPT extends quoted strings, not atoms, keywords, UID subcommands or bare mailbox names; reject typed non-ASCII atoms before fetching or changing data, including literal continuations and APPEND, while preserving legal quoted and counted values. | TestCoreUTF8DoesNotExtendAtoms, TestCoreQuotedAndLiteralValuesRemainText, TestCoreTextRejectionLiteralBoundaries |


## RFC 3501: remaining prose and grammar requirements

These rows complete the RFC3501 inventory alongside the earlier command
rows. `n/a: not fully audited` is an explicit **unproved** boundary, not a claim
that a server obligation is optional or implemented incorrectly. `n/a: not
supported yet` describes a known implementation gap; host/client duties and
unused optional features are distinguished in their reasons. In particular,
IMAP-SELECT-09 and IMAP-STATUS-08 are unchanged: #804 needs the owner's design
decision. No engine behavior or RFC9051 scope (#718) is changed here.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP-INV-LINK-01 | RFC 3501 §2.1 | Normative | Use a reliable ordered byte stream; a TCP IMAP listener uses port 143. | n/a: listener and transport deployment are host duties; Session receives an already-open net.Conn and does not bind a port. |
| IMAP-INV-UID-01 | RFC 3501 §2.3.1.1 | MUST NOT | Reuse a mailbox-name/UIDVALIDITY/UID identity for another message, including after deletion or renaming and recreation of the mailbox. | n/a: host-backend duty; UID allocation and historical identity are not exposed as transactions to Session. IMAP-CREATE-04, IMAP-DELETE-05 and IMAP-RENAME-04 cover the individual mailbox operations; #804 remains owner-pending. |
| IMAP-INV-UID-02 | RFC 3501 §2.3.1.1 | MUST NOT | Change a message UID during a session; UIDs SHOULD persist between sessions and any change MUST be detectable by a greater UIDVALIDITY. | n/a: host-backend duty; Message.UID and UIDPlusMailbox.UIDValidity are supplied by the host. Cached-snapshot tests do not establish persistence or cross-session validity increments. |
| IMAP-INV-UID-03 | RFC 3501 §2.3.1.1 | MUST | Assign UIDs strictly increasingly as messages arrive; if external reordering requires new UIDs, regenerate them with a changed validity value. | n/a: host-backend duty; the engine neither allocates UIDs nor controls non-IMAP writers. Zero, duplicate, descending and exhausted host UID assignments are not fully audited. |
| IMAP-INV-UID-04 | RFC 3501 §2.3.1.1 | Normative | A store unable to persist UIDs regenerates them per session and supplies a distinct UIDVALIDITY for each such session. | n/a: host-backend duty conditional on nonpersistent storage; there is no Session UID allocator or nonpersistent-store mode. |
| IMAP-INV-UID-05 | RFC 3501 §2.3.1.1 | MUST NOT | Change internal date, RFC2822 size, envelope, body structure or any message text for a fixed mailbox-name/UIDVALIDITY/UID identity. | n/a: host-backend duty; the engine derives static FETCH items from supplied content and metadata but cannot prevent a backend or another writer replacing them. This also inventories the static-data rule in §§6.4.5 and 9. |
| IMAP-INV-SEQ-01 | RFC 3501 §2.3.1.2 | MUST | Order message sequence numbers by ascending UID, from 1 through the mailbox count; an arrival takes the next position and expunges immediately renumber successors. | n/a: partly host-backend duty; Session uses the order of Mailbox.Messages without sorting or validating it. IMAP-EXPUNGE-02 proves expunge renumbering, not arbitrary backend ordering or arrival order. |
| IMAP-INV-FLAGS-01 | RFC 3501 §2.3.2 | Normative | Permanent flag changes are visible to concurrent and subsequent sessions; session-only flags affect only the current session. | n/a: host-backend duty for durable system flags and keywords; the model supplies Deleted/Keywords and component fixtures prove their persistence. Arbitrary per-flag permanent/session policy and durability of every backend are not wire-proved. |
| IMAP-INV-FLAGS-02 | RFC 3501 §2.3.2 | SHOULD | Treat a message as Recent when it is impossible to determine whether this is the first session notified of its arrival. | n/a: not supported yet; the model has no arrival-notification or Recent state. The unseen approximation in IMAP-SELECT-08 cannot establish this fallback. |
| IMAP-INV-FLAGS-03 | RFC 3501 §2.3.2 | MAY | Permit user-defined keywords when permanent keyword creation is advertised with \*. | TestKeywordsAdvertisedAndStored |
| IMAP-INV-DATE-01 | RFC 3501 §2.3.3 | SHOULD | For SMTP delivery use final-delivery time as INTERNALDATE, not the message Date header; COPY preserves the source date and APPEND follows its date argument/default rules. | n/a: host-backend duty for delivery timestamps and COPY; IMAP-FETCH-07 tests supplied metadata, IMAP-APPEND-06 tests DateAppender dispatch, and IMAP-APPEND-20/IMAP-COPY-12 retain the unsupported backend boundaries. |
| IMAP-INV-SIZE-01 | RFC 3501 §2.3.4 | Normative | RFC2822 size is the complete stored message octet count, including headers, not decoded body size or a character count. | n/a: host-backend duty to supply correct Message.Size. IMAP-FETCH-07 and IMAP-SEARCH-17 test use of metadata, not consistency between arbitrary metadata and stored bytes. |
| IMAP-INV-TEXT-01 | RFC 3501 §4.3.1 | SHOULD | Transmit 8-bit or multioctet literal text only when its character set is identified; binary data must be textually transfer-encoded. | TestFetchBodyDataSevenBit, TestFetchEncodedBodyWithStray8bit, TestEnvelopeStringsSevenBit, TestEnvelopeTextNotUTF8SevenBit, TestLongDecodedNameStaysOneString, TestBodyStructureStringsSevenBit, TestAddressWithNoSevenBitFormIsNIL, TestMetadataStringsInUTF8Session |
| IMAP-INV-STATE-01 | RFC 3501 §6 | Normative | A BAD rejection never changes connection or selected-mailbox state; NO generally preserves state, except unsuccessful SELECT/EXAMINE. | n/a: not audited for every command. Malformed SELECT/EXAMINE now keep the old selection on BAD (TestSelectionRefusalKeepsSelection, IMAP-SELECT-11); other commands' BAD and NO paths are not proved here. |
| IMAP-INV-UPDATE-01 | RFC 3501 §7 | SHOULD | With simultaneous mailbox access, send appropriate unilateral FETCH and EXPUNGE updates when another agent changes flags or removes messages; new-mail checks report EXISTS and RECENT. | n/a: only NOOP/CHECK/IDLE polling is proved (IMAP-UPDATE-01/02, IMAP-STORE-12). Command-wide observation, concurrent change races and true Recent notifications are not fully supported or audited; IMAP-RESP-12 still governs safe EXPUNGE timing. |
| IMAP-INV-CAP-01 | RFC 3501 §7.2.1 | MUST | Keep responses within the base protocol until the client invokes an associated advertised extension; advertise only registered standard/standards-track capabilities or X-prefixed private names. | n/a: not fully audited across all advertised extension combinations; individual extension rows cover their invocation paths, not a complete negative opt-in matrix. No private capability is defined. RFC 9051 negotiation is separate #718 scope. |
| IMAP-INV-CREATE-01 | RFC 3501 §6.3.3 | Normative | Return CREATE OK only after creating a new mailbox with the requested name, and return NO on creation failure. | n/a: not supported yet; CREATE acknowledges ordinary names without a backend creation operation. IMAP-CREATE-02 records duplicate acceptance; successful durable creation and storage failures are not proved. |
| IMAP-INV-CREATE-02 | RFC 3501 §6.3.3 | MUST | Ignore an unneeded trailing-delimiter declaration and create the name without that final delimiter. | n/a: not supported as a durable operation; no backend create API exists. IMAP-CREATE-03 records missing superior creation, but does not prove the distinct mandatory trailing-delimiter canonicalization. |
| IMAP-INV-DELETE-01 | RFC 3501 §6.3.4 | Normative | Deleting a selectable parent with inferiors removes its messages but preserves the inferiors and leaves a Noselect hierarchy name. | n/a: not supported completely; message movement to Trash is not a durable mailbox deletion, and no selectable/Noselect parent state is exposed by the backend. Inferior preservation alone is IMAP-DELETE-03. |
| IMAP-INV-RENAME-01 | RFC 3501 §6.3.5 | SHOULD | Create superior hierarchical names needed for the destination of RENAME. | n/a: not supported yet; the backend has no durable mailbox creation/rename operation, so moving messages cannot establish the required superior names. |
| IMAP-INV-LIST-01 | RFC 3501 §6.3.8 | SHOULD | Return LIST promptly and avoid excessive work determining Marked/Unmarked or other attributes. | n/a: host-backend latency and scale duty; synchronous Folders calls have no performance bound and no public-session load/latency test establishes this recommendation. |
| IMAP-INV-LIST-02 | RFC 3501 §§6.3.8, 7.2.2 | Normative | For a flat namespace, return NIL as the hierarchy delimiter, including on the empty-pattern root discovery request. | n/a: optional flat namespaces are not offered; this backend contract uses slash-delimited hierarchical names. Nonempty-reference root discovery is already proved by IMAP-LIST-05. |
| IMAP-INV-LIST-03 | RFC 3501 §6.3.8 | SHOULD | Preserve any reference prefix included in an interpreted LIST name in the same form, so clients can detect a breakout from the reference context. | n/a: not fully audited beyond IMAP-LIST-04 reference-prefix fixtures; filesystem home expansion and alternate namespace breakout policies are not exposed by this backend. |
| IMAP-INV-LIST-04 | RFC 3501 §6.3.8 | MAY | Hide otherwise accessible mailbox names from wildcard matching; if the namespace convention is offered, treat leading # as a breakout from the reference context. | n/a: optional hidden-mailbox/namespace policy is not offered; # names are ordinary folder names and there is no alternate namespace lookup. Explicit namespace conventions are not inferred from NAMESPACE support. |
| IMAP-INV-LSUB-01 | RFC 3501 §6.3.9 | MAY | LSUB attributes may differ from LIST attributes for the same name; LIST attributes take precedence. | n/a: client precedence duty and optional server distinction; no subscription store or independent subscription attributes exist (IMAP-SUB-02). |
| IMAP-INV-SEARCH-01 | RFC 3501 §6.4.4 | MAY | Exclude MIME leaf body parts whose terminal media type is neither TEXT nor MESSAGE from SEARCH matching. | n/a: optional exclusion is not chosen; the engine searches all MIME leaves. IMAP-SEARCH-10 supplies decoding proofs, not an exclusion-policy requirement. |
| IMAP-INV-UID-06 | RFC 3501 §6.4.8 | MUST | Every FETCH response caused by any UID command includes UID, even if the command did not explicitly request that item. | n/a: not fully audited beyond UID FETCH (IMAP-FETCH-09) and UID STORE (IMAP-STORE-04). Unilateral FETCH responses during UID COPY/SEARCH and concurrent update timing have no complete wire proof. |
| IMAP-INV-XCMD-01 | RFC 3501 §6.5.1 | MUST | Private experimental responses use an X prefix and are sent only after the client invokes the associated experimental command. | n/a: no private experimental command or response is defined; rejecting unknown X commands (IMAP-XCMD-01) does not exercise an implemented experimental exchange. |
| IMAP-INV-RESP-01 | RFC 3501 §7.1.2 | Normative | An untagged NO is a warning and need not make the associated command fail; tagged NO indicates operational failure. | n/a: optional untagged warning emission is not offered by the engine. Tagged failures are proved separately by IMAP-RESP-10. |
| IMAP-INV-SASL-01 | RFC 3501 §6.2.2 | Normative | Omit automatic unprotected post-authentication capabilities when a SASL security layer was negotiated; the client reissues CAPABILITY under that layer. | n/a: no SASL security layer is offered; PLAIN-only authentication makes the security-layer condition inapplicable (IMAP-AUTHENTICATE-11). |
| IMAP-INV-SEC-01 | RFC 3501 §11.1 | MUST | Implement TLS_RSA_WITH_RC4_128_MD5 as the original RFC3501 mandatory-to-implement TLS cipher suite. | n/a: not implemented by this inventory; legacy cipher interoperability is not tested or enabled. TLS policy is supplied by the host Go tls.Config. This records the original RFC requirement, not advice to enable this cipher. |
| IMAP-INV-SEC-02 | RFC 3501 §11.1 | SHOULD | Implement TLS_DHE_DSS_WITH_3DES_EDE_CBC_SHA; other cipher suites are optional under the original specification. | n/a: not implemented or wire-tested; legacy cipher configuration is host/runtime policy. No cipher or security policy is changed by this documentation audit. |
| IMAP-INV-SEC-03 | RFC 3501 §11.1 | MUST | Check that STARTTLS and the following TLS negotiation succeeded before continuing protected IMAP commands. | TestStartTLSExample, TestStartTLSFailedNegotiation |
| IMAP-INV-SEC-04 | RFC 3501 §11.2 | SHOULD NOT | Reveal why credentials were invalid in a failed AUTHENTICATE response. | TestAuthenticationFailureDetailsStayPrivate |
| IMAP-INV-SEC-05 | RFC 3501 §11.2 | SHOULD NOT | Distinguish an invalid LOGIN user name from an invalid password in the wire error. | TestAuthenticationFailureDetailsStayPrivate |
| IMAP-INV-SEC-06 | RFC 3501 §11.2 | SHOULD | Provide a mechanism to limit or delay failed LOGIN and AUTHENTICATE attempts. | TestAuthenticationFailureBudgetAppliesToBothForms |
| IMAP-INV-SEC-07 | RFC 3501 §11.2 | MUST | Provide a configuration that protects credentials or advertises LOGINDISABLED, withholds plaintext SASL mechanisms, and refuses LOGIN/PLAIN even for valid credentials until protected. | TestStartTLSExample, TestStartTLSCleartextCredentialsRefused |
| IMAP-INV-CLIENT-01 | RFC 3501 §2.2.1 | MUST | Clients use distinct command tags, obey exact syntax, finish continuations before starting another command, and wait for each synchronizing literal continuation even at zero octets. | n/a: client-only duties; server continuations and rejection behavior are inventoried by IMAP-LITERAL, IMAP-APPEND and IMAP-SYNTAX rows. |
| IMAP-INV-CLIENT-02 | RFC 3501 §5.5 | MUST NOT | Clients pipeline ambiguous commands or send a sequence-number command before completion of a command that permits EXPUNGE; UID commands have their own ambiguity rules. | n/a: client-only scheduling duty; server in-order execution is IMAP-UPDATE-04 and EXPUNGE safety is IMAP-RESP-12. |
| IMAP-INV-CLIENT-03 | RFC 3501 §11.1 | MUST | Clients verify the certificate against the original connection hostname without insecure canonicalization, follow the specified name/wildcard matching rules, and handle a mismatch by confirmation or termination. | n/a: client-only certificate validation duty; Session is the server endpoint. The source permits a leftmost-component wildcard and a match against any certificate name, recommends dNSName SAN when present, and matches names without regard to case. |
| IMAP-INV-CLIENT-04 | RFC 3501 §3.4 | MUST | On LOGOUT clients read tagged OK before closing; clients SHOULD use LOGOUT rather than unilateral close and SHOULD drain responses until EOF after BYE. | n/a: client-only lifecycle duties. Server logout ordering is IMAP-LOGOUT-01; a peer that already closed may be closed without sending BYE. |
| IMAP-INV-CLIENT-05 | RFC 3501 §5.1 | MUST NOT | Clients create 8-bit mailbox names in rev1; clients SHOULD interpret legacy 8-bit listings as UTF-8, handle server name case policy, and use the required quoting for special characters. | n/a: client-only naming duties; server modified UTF-7 and INBOX handling are IMAP-MUTF7 and mailbox-command rows. |
| IMAP-INV-CLIENT-06 | RFC 3501 §6.3.8 | MUST NOT | A hierarchical browser assumes reference interpretation unless the reference is a hierarchy level ending in its delimiter; clients SHOULD avoid other references absent an explicit user request. | n/a: client-only browsing policy; server reference interpretation is inventoried separately. |
| IMAP-INV-CLIENT-07 | RFC 3501 §7.4.2 | MUST | Clients accept future BODYSTRUCTURE/flag extensions, decode body transfer encodings, interpret body text with its charset/type, and SHOULD treat NIL and empty envelope strings identically. | n/a: client-only data interpretation/forward-compatibility duties; server generation limits and ENVELOPE distinctions have separate rows. |
| IMAP-INV-STATE-02 | RFC 3501 §6 | Normative | Rejected non-selection commands preserve the selected mailbox and existing flags: unknown command and malformed CAPABILITY/FETCH/STORE/SEARCH are exercised under SELECT and EXAMINE. | TestRFC3501RejectedNonSelectionKeepsState |
| IMAP-INV-NAME-01 | RFC 3501 §§1.2, 5.1.1, 7.2.2 | MUST | Export left-to-right hierarchy with one consistent separator at all levels, never ampersand: LIST and LSUB use slash for INBOX, Work and Work/Q1. | TestRFC3501HierarchyDelimiter |
| IMAP-INV-LIST-05 | RFC 3501 §7.2.2 | MUST | Returned LIST names are valid LIST/LSUB references and, unless Noselect, selectable mailbox arguments; all children of a top-level node share the delimiter. | n/a: Folders and Messages consistency/selectability is a host-backend duty. IMAP-INV-NAME-01 proves fixed delimiter output, not validity of every arbitrary backend-supplied name or lookup. |
| IMAP-INV-TEXT-02 | RFC 3501 §7.4.2 | Normative | Permit 8-bit body section output only when that section's body parameter list identifies its charset; header and MIME sections stay 7-bit. | TestFetchHeaderSectionsSevenBit, TestFetchBodyDataSevenBit, TestFetchSevenBitMessageByteForByte, TestEncodedWordsStayWithinLimits, TestHeaderSectionKeepsEveryParameter, TestHeaderSectionKeepsParameterInUTF8Session |
| IMAP-INV-FLAGS-04 | RFC 3501 §2.3.2 | Normative | Recent identifies the first session notified of an arrival; after read-write notification subsequent sessions do not see it, while EXAMINE and STATUS never consume it. | n/a: not supported yet; no first-notification or Recent state exists. Which simultaneous connection receives Recent is deliberately unspecified by the RFC. IMAP-SELECT-08 and IMAP-STATUS-07 retain the existing gaps. |
| IMAP-INV-FLAGS-05 | RFC 3501 §7.1 | Normative | A STORE flag absent from PERMANENTFLAGS is ignored or session-only, never persisted; \* permits creating permanent keywords. | n/a: arbitrary per-flag write policy is not exposed by the backend; fixed system flags and keyword creation have component proofs, but all flag-policy/read-only combinations are not audited. |
| IMAP-INV-SYNTAX-01 | RFC 3501 §2.2.2 | SHOULD | Enforce the specified command syntax and reject missing/extraneous spaces or arguments with matching tagged BAD rather than running the malformed command. | n/a: not fully supported or audited across every command/source form. Literal SEARCH dates and Recent STORE input are accepted contrary to their grammar; a read-only STORE can receive NO before argument validation. Narrow refusals remain individually mapped. |
| IMAP-INV-LIST-06 | RFC 3501 §7.2.2 | Normative | Noinferiors means no child hierarchy levels exist now or can be created under the name in future. | n/a: optional leaf-only mailbox attributes are not offered; Folder exposes no child-creation policy and the engine does not emit Noinferiors. |
| IMAP-INV-LIST-07 | RFC 3501 §7.2.2 | Normative | Noselect denotes a name that cannot be selected as a mailbox, not a selectable empty mailbox. | n/a: backend selectability is not generally exposed; synthetic hierarchy parents have IMAP-LIST-06 proofs, but arbitrary Folders/Messages agreement is the host duty in IMAP-INV-LIST-05. |
| IMAP-INV-LIST-08 | RFC 3501 §7.2.2 | Normative | Marked denotes server interest/probable additions since last selection; Unmarked means no additional messages since last selection. | n/a: optional interest attributes are not offered; no last-selection interest history is represented or exported through Folder. |
| IMAP-INV-LIST-09 | RFC 3501 §7.2.2 | SHOULD NOT | Send Marked or Unmarked for a Noselect name or when determining mailbox interest is infeasible. | n/a: neither interest marker is emitted; no conditional interest-classification feature exists to map to a named wire proof. The exact meaning of each marker remains IMAP-INV-LIST-08. |
| IMAP-INV-CLIENT-08 | RFC 3501 §9 | MUST | SEARCH CHARSET arguments use registered IANA charset names. | n/a: client argument-selection and specification-registration duty; the server's US-ASCII support and unsupported-charset refusal are IMAP-SEARCH-07 and IMAP-RESP-06. |
| IMAP-INV-TEXT-03 | RFC 3501 §4.3.1 | MAY | Treat a string with excessive control characters as binary and require transfer encoding accordingly. | n/a: optional control-density classification is not chosen; no threshold or such classification API is offered. The mandatory NUL/binary rule remains IMAP-RESP-16 and IMAP-INV-ABNF-08. |
| IMAP-INV-ABNF-01 | RFC 3501 §9 | Normative | Address tuples have exactly personal-name, route, mailbox and host nstrings; strip RFC2822 quoting from personal/local names, and use NIL-host start/end group markers. | n/a: not fully supported; obsolete route addresses are lost (IMAP-ENV-11); all quoting/obsolete address alternatives are not wire-proved. |
| IMAP-INV-ABNF-02 | RFC 3501 §9 | Normative | Atoms contain one or more permitted ASCII non-specials; astring additionally permits ] or a quoted/literal string, but not an empty bare token. | n/a: not fully audited for every command position and octet; existing syntax/astring proofs are narrower than the full cross-product. |
| IMAP-INV-ABNF-03 | RFC 3501 §9 | Normative | AUTHENTICATE uses an atom mechanism and CRLF-separated base64 responses; base64 is case-sensitive with complete groups and only the prescribed final padding forms. | n/a: not fully audited for every base64 alphabet/padding/embedded-control combination. PLAIN cancellation and common malformed responses are IMAP-AUTHENTICATE rows. |
| IMAP-INV-ABNF-04 | RFC 3501 §9 | MUST NOT | Use generic basic-body syntax for MESSAGE/RFC822; choose the structured message form with envelope, nested body and encoded line count, the text form with lines, or a nonempty multipart sequence and subtype. | n/a: not fully audited over every legal MIME type, nested shape and fallback. Existing text/multipart/message examples do not prove all alternatives. |
| IMAP-INV-ABNF-05 | RFC 3501 §9 | MUST NOT | Emit extensions on nonextensible BODY or undefined future extension data; on BODYSTRUCTURE use ordered optional suffixes: MD5/disposition/language/location for one part, parameters/disposition/language/location for multipart. | n/a: no undefined extension fields are implemented; defined extension ordering/omission has component proofs but no exhaustive optional-prefix/nesting matrix. |
| IMAP-INV-ABNF-06 | RFC 3501 §9 | Normative | Body fields use parameter pairs or NIL, id/description nstrings, an encoding string and unsigned encoded octet/line counts; disposition is type plus parameters or NIL, language is nstring or a nonempty string list, and location/MD5 are nstrings. | n/a: not fully audited for every nstring/list alternative, arbitrary metadata and uint32 overflow; IMAP-BS-10 retains the complete string-serialization boundary. |
| IMAP-INV-ABNF-07 | RFC 3501 §9 | MUST | A capability list contains IMAP4rev1 and space-separated AUTH=mechanism or atom entries; new names need X or IANA registration, and RFC1730 compatibility must list IMAP4 first. | n/a: RFC1730 compatibility and private capability definitions are not offered. Base rev1 presence is proved by IMAP-CAP-01/02; full registration/ordering/extension policy is not a wire proof. |
| IMAP-INV-ABNF-08 | RFC 3501 §9 | MUST NOT | Literal payloads contain NUL; the decimal count is an unsigned 32-bit number and counts exact non-NUL octets following CRLF, including zero and embedded CR/LF. | n/a: not fully audited across all argument positions, count-overflow forms and source provenance. Existing literal bounds/framing tests do not establish the full grammar. |
| IMAP-INV-ABNF-09 | RFC 3501 §9 | Normative | Commands are tag, one SP, a state-permitted command and CRLF; exact argument counts and one-SP separators are mandatory, including no-argument commands. | n/a: not fully audited across every command/state/source-form combination; the existing command suites are component evidence, not universal parser proof. |
| IMAP-INV-ABNF-10 | RFC 3501 §9 | Normative | Use the exact command-specific argument order: mailbox astrings, optional APPEND flags/date then literal, required sequence sets and data items, one-or-more SEARCH keys, nonempty STATUS item lists, and UID only around COPY/FETCH/SEARCH/STORE in the base protocol. | n/a: not fully audited for all alternative source forms; literal materialization can erase distinctions between an atom/date and a string. Extensions have their separate capability and grammar rows. |
| IMAP-INV-ABNF-11 | RFC 3501 §9 | Normative | Responses use exact CRLF framing, + SP for continuation, * SP for data or fatal BYE, and the command tag for OK/NO/BAD completion; greetings allow OK/PREAUTH/BYE and fatal BYE closes immediately. | n/a: PREAUTH is not offered and arbitrary framing/error/transport combinations are not fully audited. Existing public-session tests prove the stated individual paths, not every response alternative. |
| IMAP-INV-ABNF-12 | RFC 3501 §9 | Normative | SEARCH dates are bare or quoted day-month-four-digit-year, never literals; APPEND/INTERNALDATE use a quoted fixed-width day, month, four-digit year, hh:mm:ss and signed four-digit east-of-UTC offset. | n/a: not fully supported for source types: materialized SEARCH literal dates can become quoted dates. Existing calendar and fixed-width fixtures do not prove source-form preservation in every route. |
| IMAP-INV-ABNF-13 | RFC 3501 §9 | Normative | Numbers are unsigned decimal uint32; nz-number/uniqueid start with 1–9, cannot be zero or have leading zeros, and uniqueids are strictly increasing. | n/a: host-generated numeric identity/metadata validity and exhaustion are not fully supported; IMAP-SELECT-09/IMAP-STATUS-08 remain unchanged for #804. Input bounds have separate narrow proofs. |
| IMAP-INV-ABNF-14 | RFC 3501 §9 | Normative | ENVELOPE has ten ordered fields; scalar fields are nstrings and address fields are NIL or nonempty address lists. Absent/empty address lists become NIL and absent/empty Sender/Reply-To default to From. | n/a: not fully audited for all legal/obsolete headers and every nstring/list combination; source-route and malformed-header boundaries remain IMAP-ENV-11/12. |
| IMAP-INV-ABNF-15 | RFC 3501 §9 | Normative | FETCH takes one data item or a nonempty item list, or one exclusive macro; BODY sections use bracketed positive numeric paths, HEADER/TEXT only at top level or an encapsulated message, numeric-prefix MIME, and nonempty astring header lists. Partial requests use uint32 origin and nonzero count. | n/a: not fully audited over every nested section type and header-list source form; the per-command invalid/composed/partial tests remain the narrower proofs. |
| IMAP-INV-ABNF-16 | RFC 3501 §9 | MUST NOT | Generate undefined system-flag extensions; accept Recent only in fetched flag lists, never STORE/APPEND flag input, and allow wildcard \* only in PERMANENTFLAGS. | n/a: not fully audited; STORE now refuses Recent with BAD (TestStoreRefusesRecent, IMAP-STORE-11), but APPEND flag input and wildcard use are not proved here. Valid persistent keyword creation is independently proved by IMAP-STORE-06; all future-flag/host-input combinations are not audited. |
| IMAP-INV-ABNF-17 | RFC 3501 §9 | Normative | Mailbox arguments treat every ASCII case of INBOX specially; LIST patterns additionally allow * and %. LIST/LSUB rows contain a flag list, one quoted delimiter character or NIL, and a mailbox astring; Noselect/Marked/Unmarked are mutually exclusive. | n/a: flat namespaces, arbitrary attributes and every response-string alternative are not fully audited. Fixed delimiter and ordinary listing proofs do not establish the complete grammar. |
| IMAP-INV-ABNF-18 | RFC 3501 §9 | MUST NOT | Change static FETCH attributes for one message identity; emit message data with a positive sequence number and a nonempty item/value list. FLAGS may vary; mailbox/status/count forms use their specified unsigned numbers and attribute/value pairs. | n/a: immutable values are host duties (IMAP-INV-UID-05), and all backend-generated uint32/empty-list alternatives are not fully audited. Component response examples are not an exhaustive serializer proof. |
| IMAP-INV-ABNF-19 | RFC 3501 §9 | Normative | Keep NIL distinct from empty string/list; strings are quoted ASCII without CR/LF or counted literals, and quoted escapes allow only double quote and backslash. | n/a: not fully audited across every parser and serializer position; literal materialization erases original string type and raw quoted controls need a complete provenance audit. |
| IMAP-INV-ABNF-20 | RFC 3501 §9 | Normative | Tags are nonempty astring characters excluding +; response text is nonempty ASCII without CR/LF, optional [code] is followed by SP and text, known numeric codes are positive uint32, and unknown-code arguments cannot contain ]. | n/a: not fully audited for all host-derived diagnostics and positive response-code overflow. Tag/common text tests do not establish all serializer paths. |
| IMAP-INV-ABNF-21 | RFC 3501 §9 | Normative | SEARCH operands use the specified atom/astring/date/number forms and recursive NOT/OR/groups; sets contain positive endpoints or *, inclusive ranges work in either direction, * means the largest used value (UIDNEXT for an empty UID mailbox), and overlaps may be coalesced or reordered. | n/a: source-form and empty-mailbox UIDNEXT semantics are not fully audited. Out-of-range message sequence numbers should get BAD, including * on an empty mailbox, but current dispatch silently ignores absent numbers (IMAP-SYNTAX-08). |
| IMAP-INV-ABNF-22 | RFC 3501 §9 | MUST | When grammar alternatives overlap, give the earlier alternative priority; follow case-insensitive ASCII tokens unless otherwise specified, exact single SP separators and the ban on NUL. | n/a: case/SP/NUL have component proofs (IMAP-SYNTAX-05), but precedence is not fully audited over all overlapping alternatives, including system flags, mailbox INBOX and structured MIME types. |

### Corrections and proof limits (2026-11-03)

- IMAP-SELECT-11 previously described deselection after a syntax BAD as a
  required behavior. Its tests actually assert that deviation. The mapped row
  now also claims the state preservation that later engine work proved, and
  IMAP-INV-STATE-01 records what remains unproved for other commands.
- IMAP-DELETE-04 previously invented a SHOULD about explaining a refused delete.
  The source instead forbids deleting a Noselect parent with inferiors. Its row
  now states that actual rule and the unsupported backend selectability boundary.
- IMAP-RESP-12's test intentionally also defers EXPUNGE during UID forms. The
  RFC prohibition applies only to non-UID FETCH/STORE/SEARCH and incomplete
  commands; deferring during UID forms is an implementation choice, not a MUST.
- Stale keyword/Deleted boundaries are corrected: IMAP-SEARCH-11 now separates
  its empty-keyword fixture from actual keyword persistence; IMAP-APPEND-19 and
  IMAP-COPY-12 no longer claim absent model fields. IMAP-EXPUNGE-07 gains a
  two-session persistence proof; IMAP-STORE-11 loses a misleading Recent proof,
  whose unchanged test body remains component evidence for IMAP-STORE-02.
- Inventory completeness is not conformance: Recent, flag-policy combinations, durable folder
  operations, immutable host identities, rollback, every grammar alternative's
  wire proof and the original legacy TLS suites retain explicit boundaries.
  Entire RFC sample transcripts, reference comparisons, race/timed fuzzing,
  coverage measurement and other RFC inventories are not established here.

## RFC 9051: complete section index

Audit source: RFC Editor RFC9051 text, retrieved 2026-11-03, SHA-256
`acf52a50eb748ea21b5ffdcb31c761d3fbba6242d53d4f53c2555de039c5c3cf` (the same
text as the first-pass record below). The source is reference material, not
vendored runtime input. Every numbered section and appendix is listed once,
including headings without independent requirements. Where RFC 9051 restates an
RFC 3501 rule, the row cites the RFC 3501 inventory's `IMAP-` rows instead of
repeating them; `IMAP2-` rows hold rev2-specific requirements. References ending
in `-*` mean all rows in that family, not that one test proves the family. Read
each row's actual Tests cell: many rev2 rows are `n/a` because the rule is
unsupported or unproved. The engine advertises IMAP4rev1 only, so no row here
claims IMAP4rev2 conformance.

The 232 named section-9 productions are dispositioned in the RFC 9051 rows of
the [core grammar crosswalk](RFC-GRAMMAR.md), whose pinned source index CI
already checks for missing, duplicate and unknown productions; this index does
not repeat it. Rev2-only grammar (number64, UTF-8 defaults, removed rev1
forms) remains the boundary IMAP2-SEC-217.

Gaps not covered by an existing issue are filed and linked from their rows:
rest-mail/rest-mail-server#1668 (UID in unsolicited FETCH),
rest-mail/rest-mail-server#1669 (mailbox-name normalization and OLDNAME),
rest-mail/rest-mail-server#1670 (SCRAM-SHA-256),
rest-mail/rest-mail-server#1671 (mailbox access rights in the engine) and
rest-mail/rest-mail-server#1672 (gateway TLS and password baseline). Already
tracked elsewhere: IMAP4rev2 mode, ENABLE IMAP4rev2, LIST on SELECT, CLOSED,
default ESEARCH, STATUS DELETED and the predefined keywords
(rest-mail/rest-mail-server#718); keyword storage (rest-mail/go-imap#158);
shared Deleted (rest-mail/go-imap#159); durable folder operations
(rest-mail/go-imap#160); atomic multi-message COPY/MOVE (rest-mail/go-imap#161);
UIDNEXT history (rest-mail/rest-mail-server#804); APPEND dates
(rest-mail/rest-mail-server#1554, #1557).

| Section | Title | Requirement rows or explicit exclusion |
|---------|-------|-----------------------------------------|
| 1 | How to Read This Document | n/a: Reading guide; substantive conventions are indexed at 1.2. |
| 1.1 | Organization of This Document | n/a: Document organization; no additional wire behavior. |
| 1.2 | Conventions Used in This Document | IMAP-SYNTAX-05; IMAP-INV-TEXT-01; IMAP2-SEC-218 |
| 1.3 | Special Notes to Implementors | n/a: Implementor reading notes and historical context; no additional server requirement. |
| 2 | Protocol Overview | n/a: Overview heading; see every subsection below. |
| 2.1 | Link Level | IMAP-INV-LINK-01 |
| 2.2 | Commands and Responses | IMAP-GREET-01; IMAP-TAG-01; IMAP-INV-ABNF-11 |
| 2.2.1 | Client Protocol Sender and Server Protocol Receiver | IMAP-INV-CLIENT-01; IMAP-TAG-01; IMAP-RESP-17; IMAP2-CORE-02 |
| 2.2.2 | Server Protocol Sender and Client Protocol Receiver | IMAP-RESP-01; IMAP-INV-SYNTAX-01; IMAP2-CORE-05 |
| 2.3 | Message Attributes | n/a: Attribute overview; each attribute is indexed below. |
| 2.3.1 | Message Numbers | IMAP-INV-SEQ-01; IMAP-INV-UID-01 |
| 2.3.1.1 | Unique Identifier (UID) Message Attribute | IMAP-INV-UID-*; IMAP-SELECT-03; IMAP-SELECT-04; IMAP-SELECT-09; IMAP-STATUS-08; IMAP2-CORE-07; IMAP2-CORE-08; IMAP2-CORE-09; IMAP2-CORE-10; IMAP2-CORE-11; IMAP2-CORE-12; IMAP2-CORE-13 |
| 2.3.1.2 | Message Sequence Number Message Attribute | IMAP-INV-SEQ-01; IMAP-EXPUNGE-02; IMAP2-CORE-14 |
| 2.3.2 | Flags Message Attribute | IMAP-INV-FLAGS-*; IMAP-STORE-*; IMAP-EXPUNGE-07; IMAP2-CORE-15 |
| 2.3.3 | Internal Date Message Attribute | IMAP-INV-DATE-01; IMAP-FETCH-07; IMAP-APPEND-20; IMAP-COPY-12; IMAP2-CORE-19 |
| 2.3.4 | RFC822.SIZE Message Attribute | IMAP-INV-SIZE-01; IMAP-FETCH-07; IMAP2-CORE-20 |
| 2.3.5 | Envelope Structure Message Attribute | IMAP-ENV-* |
| 2.3.6 | Body Structure Message Attribute | IMAP-BS-*; IMAP-MSG-*; IMAP2-SEC-223 |
| 2.4 | Message Texts | IMAP-FETCH-*; IMAP-MSG-*; IMAP2-CORE-30 |
| 3 | State and Flow Diagram | IMAP-GREET-01; IMAP-MBOX-AUTH-01; IMAP-INV-STATE-01; IMAP2-CORE-21; IMAP2-CORE-22 |
| 3.1 | Not Authenticated State | IMAP-GREET-01; IMAP-LOGIN-01; IMAP-AUTHENTICATE-*; IMAP2-CORE-21 |
| 3.2 | Authenticated State | IMAP-SELECT-06; IMAP-FETCH-13; IMAP-SEARCH-09; IMAP2-CORE-22 |
| 3.3 | Selected State | IMAP-SELECT-*; IMAP-EXAMINE-*; IMAP-UNSELECT-*; IMAP-CLOSE-* |
| 3.4 | Logout State | IMAP-LOGOUT-01; IMAP-STATE-01; IMAP-RESP-13; IMAP-RESP-14; IMAP2-CORE-23; IMAP2-CORE-24 |
| 4 | Data Formats | IMAP-INV-ABNF-*; IMAP2-SEC-217 |
| 4.1 | Atom | IMAP-INV-ABNF-02; IMAP-SYNTAX-09 |
| 4.1.1 | Sequence Set and UID Set | IMAP-INV-ABNF-21; IMAP-SYNTAX-08; IMAP-SEARCHRES-* |
| 4.2 | Number | IMAP-INV-ABNF-13; IMAP2-SEC-217 |
| 4.3 | String | IMAP-LITERAL-*; IMAP-LITMINUS-*; IMAP-SYNTAX-04; IMAP-INV-ABNF-19; IMAP2-CORE-26; IMAP2-CORE-29 |
| 4.3.1 | 8-Bit and Binary Strings | IMAP-INV-TEXT-01; IMAP-INV-TEXT-02; IMAP-BINARY-*; IMAP2-CORE-30 |
| 4.4 | Parenthesized List | IMAP-INV-ABNF-18; IMAP-INV-ABNF-19 |
| 4.5 | NIL | IMAP-NIL-01; IMAP-ENV-*; IMAP-INV-ABNF-19 |
| 5 | Operational Considerations | n/a: Operational heading; each rule is indexed below. |
| 5.1 | Mailbox Naming | IMAP-MUTF7-*; IMAP-UTF8-*; IMAP-SELECT-12; IMAP2-CORE-31 |
| 5.1.1 | Mailbox Hierarchy Naming | IMAP-LIST-01; IMAP-LIST-04; IMAP-INV-NAME-01; IMAP2-CORE-32 |
| 5.1.2 | Namespaces | IMAP-NAMESPACE-*; IMAP2-CORE-33; IMAP2-CORE-34 |
| 5.1.2.1 | Historic Mailbox Namespace Naming Convention | IMAP-NAMESPACE-*; IMAP2-CORE-33 |
| 5.1.2.2 | Common Namespace Models | IMAP-NAMESPACE-*; IMAP2-CORE-33; IMAP2-CORE-34 |
| 5.2 | Mailbox Size and Message Status Updates | IMAP-UPDATE-01; IMAP-INV-UPDATE-01; IMAP2-CORE-35 |
| 5.3 | Response When No Command in Progress | IMAP-UPDATE-03; IMAP-RESP-12; IMAP2-CORE-37 |
| 5.4 | Autologout Timer | IMAP-AUTOLOGOUT-01; IMAP2-CORE-38 |
| 5.5 | Multiple Commands in Progress (Command Pipelining) | IMAP-UPDATE-04; IMAP-RESP-12; IMAP2-CORE-40 |
| 6 | Client Commands | IMAP-INV-STATE-01; IMAP-INV-STATE-02; IMAP-INV-ABNF-09 |
| 6.1 | Client Commands - Any State | IMAP-CAP-*; IMAP-NOOP-01; IMAP-LOGOUT-01 |
| 6.1.1 | CAPABILITY Command | IMAP-CAP-*; IMAP-INV-CAP-01; IMAP2-CMD-44; IMAP2-CMD-45 |
| 6.1.2 | NOOP Command | IMAP-NOOP-01; IMAP-UPDATE-01; IMAP2-CORE-35 |
| 6.1.3 | LOGOUT Command | IMAP-LOGOUT-01; IMAP2-CMD-47 |
| 6.2 | Client Commands - Not Authenticated State | IMAP-AUTH-STATE-01; IMAP-ANON-01; IMAP-STARTTLS-*; IMAP-AUTHENTICATE-*; IMAP-LOGIN-* |
| 6.2.1 | STARTTLS Command | IMAP-STARTTLS-*; IMAP2-CMD-48; IMAP2-CMD-49; IMAP2-CMD-50 |
| 6.2.2 | AUTHENTICATE Command | IMAP-AUTHENTICATE-*; IMAP-SASLIR-*; IMAP-PLAIN-*; IMAP2-CMD-51; IMAP2-CMD-52; IMAP2-CMD-53; IMAP2-CMD-54; IMAP2-CMD-55; IMAP2-CMD-56; IMAP2-CMD-57; IMAP2-CMD-57-02; IMAP2-CMD-58; IMAP2-CMD-59 |
| 6.2.3 | LOGIN Command | IMAP-LOGIN-*; IMAP2-CMD-62 |
| 6.3 | Client Commands - Authenticated State | IMAP-MBOX-AUTH-01 |
| 6.3.1 | ENABLE Command | IMAP-ENABLE-*; IMAP2-CMD-63; IMAP2-CMD-64; IMAP2-CMD-65; IMAP2-CMD-66; IMAP2-CMD-69; IMAP2-CMD-70 |
| 6.3.1.1 | Note to Designers of Extensions That May Use the ENABLE | n/a: Guidance to designers of future ENABLE extensions; no behavior of this server. |
| 6.3.2 | SELECT Command | IMAP-SELECT-*; IMAP2-CMD-72; IMAP2-CMD-73; IMAP2-CMD-74; IMAP2-CMD-75; IMAP2-CMD-76 |
| 6.3.3 | EXAMINE Command | IMAP-EXAMINE-*; IMAP-SELECT-*; IMAP2-CMD-72; IMAP2-CMD-78 |
| 6.3.4 | CREATE Command | IMAP-CREATE-*; IMAP-INV-CREATE-*; IMAP2-CMD-79; IMAP2-CMD-80; IMAP2-CMD-81; IMAP2-CMD-82; IMAP2-CMD-83 |
| 6.3.5 | DELETE Command | IMAP-DELETE-*; IMAP-INV-DELETE-01; IMAP2-CMD-84; IMAP2-CMD-85; IMAP2-CMD-86; IMAP2-CMD-87 |
| 6.3.6 | RENAME Command | IMAP-RENAME-*; IMAP-INV-RENAME-01; IMAP2-CMD-88; IMAP2-CMD-89; IMAP2-CMD-90; IMAP2-CMD-91; IMAP2-CMD-92 |
| 6.3.7 | SUBSCRIBE Command | IMAP-SUB-*; IMAP2-CMD-93 |
| 6.3.8 | UNSUBSCRIBE Command | IMAP-SUB-* |
| 6.3.9 | LIST Command | IMAP-LIST-*; IMAP-LISTEXT-*; IMAP-INV-LIST-*; IMAP2-CMD-94; IMAP2-CMD-94-02; IMAP2-CMD-95; IMAP2-CMD-97; IMAP2-CMD-99; IMAP2-CMD-100; IMAP2-CMD-101 |
| 6.3.9.1 | LIST Selection Options | IMAP-LISTEXT-*; IMAP-SPECIALUSE-*; IMAP2-CMD-102; IMAP2-CMD-103; IMAP2-CMD-104; IMAP2-CMD-105 |
| 6.3.9.2 | LIST Return Options | IMAP-LISTEXT-*; IMAP-LISTSTATUS-*; IMAP2-CMD-107; IMAP2-CMD-108; IMAP2-CMD-109 |
| 6.3.9.3 | General Principles for Returning LIST Responses | IMAP-LISTEXT-*; IMAP2-CMD-100; IMAP2-RESP-187 |
| 6.3.9.4 | Additional LIST-Related Requirements on Clients | n/a: Client-only duty to infer implied LIST attributes; the server side is 6.3.9.3. |
| 6.3.9.5 | The CHILDREN Return Option | IMAP-LISTEXT-*; IMAP2-CMD-111; IMAP2-CMD-112 |
| 6.3.9.6 | CHILDINFO Extended Data Item | IMAP-LISTEXT-*; IMAP2-CMD-113; IMAP2-CMD-114; IMAP2-CMD-115 |
| 6.3.9.7 | OLDNAME Extended Data Item | IMAP2-CMD-116 |
| 6.3.9.8 | LIST Command Examples | n/a: Illustrative LIST transcripts; their component behaviors are indexed at 6.3.9 to 6.3.9.7, without a verbatim replay claim. |
| 6.3.10 | NAMESPACE Command | IMAP-NAMESPACE-*; IMAP2-CMD-119; IMAP2-CMD-120 |
| 6.3.11 | STATUS Command | IMAP-STATUS-*; IMAP-STATUSSIZE-*; IMAP2-CMD-122; IMAP2-CMD-124; IMAP2-DELTA-03 |
| 6.3.12 | APPEND Command | IMAP-APPEND-*; IMAP-APPENDUID-*; IMAP2-CMD-125; IMAP2-CMD-126; IMAP2-CMD-127; IMAP2-CMD-128; IMAP2-CMD-129; IMAP2-CMD-130; IMAP2-CMD-131; IMAP2-CMD-132 |
| 6.3.13 | IDLE Command | IMAP-IDLE-*; IMAP2-CMD-133; IMAP2-CMD-134 |
| 6.4 | Client Commands - Selected State | IMAP-FETCH-13; IMAP-SEARCH-09; IMAP-STORE-03; IMAP2-CORE-22 |
| 6.4.1 | CLOSE Command | IMAP-CLOSE-*; IMAP-SELECT-07 |
| 6.4.2 | UNSELECT Command | IMAP-UNSELECT-* |
| 6.4.3 | EXPUNGE Command | IMAP-EXPUNGE-*; IMAP-UIDEXPUNGE-* |
| 6.4.4 | SEARCH Command | IMAP-SEARCH-*; IMAP-ESEARCH-*; IMAP-INV-SEARCH-01; IMAP2-CMD-135; IMAP2-CMD-135-02; IMAP2-CMD-137; IMAP2-CMD-138; IMAP2-CMD-140; IMAP2-CMD-141; IMAP2-CMD-142; IMAP2-CMD-143; IMAP2-CMD-143-02; IMAP2-CMD-144; IMAP2-CMD-145 |
| 6.4.4.1 | SAVE Result Option and SEARCH Result Variable | IMAP-SEARCHRES-*; IMAP2-CMD-146; IMAP2-CMD-147 |
| 6.4.4.2 | Multiple Commands in Progress | IMAP-SEARCHRES-*; IMAP2-CMD-148 |
| 6.4.4.3 | Refusing to Save Search Results | IMAP-SEARCHRES-*; IMAP2-CMD-149 |
| 6.4.4.4 | Examples Showing Use of the SAVE Result Option | n/a: Illustrative SAVE transcripts; their behaviors are indexed at 6.4.4.1 to 6.4.4.3, without a verbatim replay claim. |
| 6.4.5 | FETCH Command | IMAP-FETCH-*; IMAP-BINARY-*; IMAP-ENV-*; IMAP-BS-*; IMAP-MSG-*; IMAP2-CMD-150; IMAP2-CMD-151 |
| 6.4.5.1 | FETCH Section Specification | IMAP-FETCH-*; IMAP-MSG-*; IMAP2-CMD-152; IMAP2-CMD-153 |
| 6.4.6 | STORE Command | IMAP-STORE-*; IMAP-INV-FLAGS-*; IMAP2-CMD-154; IMAP2-CMD-155 |
| 6.4.7 | COPY Command | IMAP-COPY-*; IMAP-UIDPLUS-*; IMAP2-CMD-156; IMAP2-CMD-157; IMAP2-CMD-157-02; IMAP2-CMD-158; IMAP2-CMD-159 |
| 6.4.8 | MOVE Command | IMAP-MOVE-*; IMAP2-CMD-160; IMAP2-CMD-161; IMAP2-CMD-162; IMAP2-CMD-163; IMAP2-CMD-163-02; IMAP2-CMD-164 |
| 6.4.9 | UID Command | IMAP-FETCH-09; IMAP-STORE-04; IMAP-SEARCH-04; IMAP-UIDEXPUNGE-*; IMAP-INV-UID-06; IMAP2-CMD-165; IMAP2-CMD-166 |
| 6.5 | Client Commands - Experimental/Expansion | IMAP-XCMD-*; IMAP-INV-XCMD-01; IMAP2-CMD-167; IMAP2-CMD-168 |
| 7 | Server Responses | IMAP-RESP-*; IMAP-INV-ABNF-11; IMAP2-CMD-172 |
| 7.1 | Server Responses - Generic Status Responses | IMAP-RESP-*; IMAP-INV-ABNF-20; IMAP2-RESP-174; IMAP2-RESP-175; IMAP2-RESP-176 |
| 7.1.1 | OK Response | IMAP-GREET-01; IMAP-RESP-02 |
| 7.1.2 | NO Response | IMAP-RESP-10; IMAP-INV-RESP-01 |
| 7.1.3 | BAD Response | IMAP-BAD-01; IMAP-RESP-03; IMAP-INV-STATE-01 |
| 7.1.4 | PREAUTH Response | IMAP-RESP-11; IMAP2-RESP-178 |
| 7.1.5 | BYE Response | IMAP-GREET-02; IMAP-LOGOUT-01; IMAP-STATE-01; IMAP-RESP-13; IMAP-RESP-14; IMAP2-CORE-24 |
| 7.2 | Server Responses - Server Status | IMAP-INV-ABNF-18 |
| 7.2.1 | ENABLED Response | IMAP-ENABLE-*; IMAP2-CMD-66 |
| 7.2.2 | CAPABILITY Response | IMAP-CAP-*; IMAP-INV-CAP-01; IMAP2-RESP-180; IMAP2-RESP-181; IMAP2-RESP-182; IMAP2-RESP-183; IMAP2-RESP-184 |
| 7.3 | Server Responses - Mailbox Status | IMAP-INV-ABNF-18 |
| 7.3.1 | LIST Response | IMAP-LIST-*; IMAP-LISTEXT-*; IMAP-SPECIALUSE-*; IMAP2-RESP-186; IMAP2-RESP-187; IMAP2-RESP-188; IMAP2-RESP-189; IMAP2-RESP-190 |
| 7.3.2 | NAMESPACE Response | IMAP-NAMESPACE-* |
| 7.3.3 | STATUS Response | IMAP-STATUS-*; IMAP-STATUSSIZE-*; IMAP-LISTSTATUS-*; IMAP2-CMD-124; IMAP2-DELTA-03 |
| 7.3.4 | ESEARCH Response | IMAP-ESEARCH-*; IMAP2-RESP-191; IMAP2-RESP-192; IMAP2-RESP-193; IMAP2-RESP-194; IMAP2-RESP-195; IMAP2-RESP-196 |
| 7.3.5 | FLAGS Response | IMAP-SELECT-01; IMAP-SELECT-13; IMAP-INV-FLAGS-* |
| 7.4 | Server Responses - Mailbox Size | IMAP-INV-ABNF-18 |
| 7.4.1 | EXISTS Response | IMAP-SELECT-01; IMAP-UPDATE-01; IMAP2-CORE-35 |
| 7.5 | Server Responses - Message Status | IMAP-INV-ABNF-18 |
| 7.5.1 | EXPUNGE Response | IMAP-EXPUNGE-02; IMAP-RESP-12; IMAP2-RESP-199 |
| 7.5.2 | FETCH Response | IMAP-FETCH-*; IMAP-ENV-*; IMAP-BS-*; IMAP-MSG-*; IMAP-RESP-16; IMAP2-RESP-201; IMAP2-RESP-202; IMAP2-RESP-203; IMAP2-RESP-204; IMAP2-RESP-205; IMAP2-RESP-207; IMAP2-RESP-208; IMAP2-RESP-209; IMAP2-RESP-210; IMAP2-RESP-210-02; IMAP2-RESP-211; IMAP2-RESP-212; IMAP2-RESP-213; IMAP2-RESP-215 |
| 7.6 | Server Responses - Command Continuation Request | IMAP-RESP-17; IMAP-LITERAL-*; IMAP-AUTHENTICATE-02 |
| 8 | Sample IMAP4rev2 Connection | n/a: Illustrative transcript, not an additional requirement; this audit does not claim a verbatim replay of the sample. |
| 9 | Formal Syntax | IMAP-SYNTAX-*; IMAP-INV-ABNF-*; IMAP2-SEC-216; IMAP2-SEC-217; IMAP2-SEC-218; IMAP2-SEC-219; IMAP2-SEC-220; IMAP2-SEC-221; IMAP2-SEC-222; IMAP2-SEC-223; IMAP2-SEC-224; IMAP2-SEC-225; IMAP2-SEC-226; IMAP2-SEC-227; IMAP2-SEC-228; IMAP2-SEC-229; IMAP2-SEC-230; IMAP2-SEC-231; IMAP2-SEC-232 |
| 10 | Author's Note | n/a: Author acknowledgement and history; no server behavior. |
| 11 | Security Considerations | IMAP-STARTTLS-*; IMAP-TLS-*; IMAP-INV-SEC-* |
| 11.1 | TLS-Related Security Considerations | IMAP-INV-SEC-01; IMAP-TLS-*; IMAP2-SEC-233; IMAP2-SEC-234; IMAP2-SEC-236 |
| 11.2 | STARTTLS Command versus Use of Implicit TLS Port | IMAP-STARTTLS-*; IMAP2-SEC-238 |
| 11.3 | Client Handling of Unsolicited Responses Not Suitable for the | n/a: Client-only duties to ignore unsuitable unsolicited responses before authentication and outside selected state. |
| 11.4 | COPYUID and APPENDUID Response Codes | IMAP-APPENDUID-05; IMAP-COPY-15; IMAP2-SEC-241 |
| 11.5 | LIST Command and Other Users' Namespace | IMAP2-SEC-242 |
| 11.6 | Use of MD5 | IMAP-BS-*; IMAP2-SEC-220 |
| 11.7 | Other Security Considerations | IMAP-INV-SEC-*; IMAP2-SEC-243; IMAP2-SEC-244; IMAP2-SEC-245; IMAP2-SEC-246; IMAP2-SEC-247 |
| 12 | IANA Considerations | n/a: IANA registration procedure, not a Session operation; capability naming duties are IMAP2-RESP-184 and IMAP2-SEC-224. |
| 12.1 | Updates to IMAP Capabilities Registry | n/a: IANA capability-registry update, not a Session operation. |
| 12.2 | GSSAPI/SASL Service Name | n/a: IANA SASL service-name registration; the name matters only for mechanisms not offered (IMAP-AUTHENTICATE-12). |
| 12.3 | LIST Selection Options, LIST Return Options, and LIST Extended | n/a: IANA LIST option and extended-data registries, not a Session operation. |
| 12.4 | IMAP Mailbox Name Attributes and IMAP Response Codes | n/a: IANA mailbox-attribute and response-code registries, not a Session operation. |
| 13 | References | n/a: Bibliography heading. |
| 13.1 | Normative References | n/a: Normative bibliography; referenced RFCs define terms, and this index does not inventory every requirement of every referenced RFC. |
| 13.2 | Informative References | n/a: Informative bibliography; no requirement. |
| 13.2.1 | Related Protocols | n/a: Informative bibliography of related protocols; no requirement. |
| 13.2.2 | Historical Aspects of IMAP and Related Protocols | n/a: Informative history of IMAP; no requirement. |
| A | Backward Compatibility with IMAP4rev1 | IMAP-MUTF7-*; IMAP2-COMPAT-249; IMAP2-COMPAT-250 |
| A.1 | Mailbox International Naming Convention for Compatibility with IMAP4rev1 | IMAP-MUTF7-*; IMAP2-COMPAT-251; IMAP2-COMPAT-252; IMAP2-COMPAT-253; IMAP2-COMPAT-254; IMAP2-COMPAT-255 |
| B | Backward Compatibility with BINARY Extension | IMAP-BINARY-*; IMAP2-RESP-202 |
| C | Backward Compatibility with LIST-EXTENDED Extension | IMAP-LISTEXT-*; IMAP2-CMD-101 |
| D | 63-Bit Body Part and Message Sizes | IMAP2-SEC-217; IMAP-INV-SIZE-01 |
| E | Changes from RFC 3501 / IMAP4rev1 | IMAP2-DELTA-03; IMAP2-SEC-217; IMAP2-SEC-225; IMAP2-SEC-226; IMAP2-CORE-15; IMAP2-CMD-74; IMAP2-CMD-133; IMAP2-CMD-57-02; see the Appendix E change crosswalk below. |
| F | Other Recommended IMAP Extensions | n/a: Recommends other extensions (QRESYNC, CONDSTORE, OBJECTID and more) outside the IMAP4rev2 base; not offered and not a base requirement. |


## RFC 9051: core inventory (sections 2–5; first pass)

The rev2 inventory is separate from the rev1 proofs above. A shared rule can be
tested through the current engine, but that does not make IMAP4rev2 available:
the engine still advertises only IMAP4rev1. Unsupported rev2 behavior is stated
as such, with its existing implementation issue, rather than checked off.
The sections below are a first pass through explicit requirement-keyword
paragraphs, with an Appendix E crosswalk. Full ABNF and non-keyword behavior
still need reconciliation. Backend contracts and root-package proofs stay
distinct from this Session harness; the overall checklist remains PARTIAL.

Client-only duties in §§2.2, 2.3.2, 4.3, 5.2 and 5.5 (unique generated tags,
continuation ordering, response caching, keyword UI behavior, literal waits,
and sequence-number pipeline precautions) do not apply to this server engine.
RFC 2119 definitions in §1.2 are not protocol implementation requirements.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP2-CORE-02 | RFC 9051 §2.2.1 | MUST | Accept reuse of a command tag; a reused tag still identifies each completion across authentication, selection and a BAD command. | TestReusedCommandTags |
| IMAP2-CORE-05 | RFC 9051 §2.2.2 | SHOULD | Reject malformed command framing and arguments with BAD, retaining a usable connection for the next well-formed command. | TestCaseAndSpacing, TestTagGrammar, TestCommandsWithoutArguments, TestLoginArguments |
| IMAP2-CORE-07 | RFC 9051 §2.3.1.1 | MUST NOT | Reuse a mailbox-name/UIDVALIDITY/UID identity for a different message, including after a mailbox is recreated. | n/a: UID allocation and persistent mailbox identity belong to the host backend; the engine cannot enforce identity across backend replacements. The base-interface UIDVALIDITY fallback remains IMAP-SELECT-09, tracked in rest-mail/rest-mail-server#374. |
| IMAP2-CORE-08 | RFC 9051 §2.3.1.1 | MUST | Keep each message UID stable within a session; a UID change between sessions must change UIDVALIDITY, and SHOULD NOT happen. | n/a: persistence is the host backend contract, not enforced by the engine; it accepts each Messages snapshot and UIDPlusMailbox metadata. Tracked in rest-mail/rest-mail-server#374. |
| IMAP2-CORE-09 | RFC 9051 §2.3.1.1 | MUST | Change UIDNEXT whenever messages are assigned new UIDs, including subsequently expunged messages, and never change it without new assignments. | n/a: not supported by the base interface; current maximum plus one loses assignment history after expunge. Same gap as IMAP-SELECT-09 and IMAP-STATUS-08; rest-mail/rest-mail-server#374. |
| IMAP2-CORE-10 | RFC 9051 §2.3.1.1 | MUST | Increase UIDVALIDITY when UIDs from a previous session no longer persist. | n/a: the host supplies UIDVALIDITY through UIDPlusMailbox; the engine does not maintain the persistent counter. Its fallback is documented in IMAP-SELECT-09; rest-mail/rest-mail-server#374. |
| IMAP2-CORE-11 | RFC 9051 §2.3.1.1 | SHOULD | Persist UIDs across sessions whenever possible. | n/a: storage lifetime and UID persistence are backend responsibilities; the Session engine has no persistent store. |
| IMAP2-CORE-12 | RFC 9051 §2.3.1.1 | MUST | Keep assigned UIDs strictly ascending in mailbox order; regenerate their identity when external reordering breaks that order. | n/a: the engine uses backend message order and does not validate or repair UID allocation. Backend identity enforcement remains rest-mail/rest-mail-server#374. |
| IMAP2-CORE-13 | RFC 9051 §2.3.1.1 | MUST | Keep internal date, message size, envelope, structure and text immutable for a UID identity, and never reuse an expunged UID under the same UIDVALIDITY. | n/a: these immutable values are supplied by the host backend, which the engine cannot prevent from replacing data. This is a backend contract, not a conformance claim for arbitrary backends. |
| IMAP2-CORE-14 | RFC 9051 §2.3.1.2 | MUST | Number messages from one in ascending UID order and assign the next sequence number when a new message arrives. | n/a: the engine numbers its backend snapshot in slice order; ascending UID order is assumed, not enforced. See IMAP2-CORE-12; rest-mail/rest-mail-server#374. |
| IMAP2-CORE-15 | RFC 9051 §2.3.2 | SHOULD | Preserve and search the recommended $MDNSent, $Forwarded, $Junk, $NotJunk and $Phishing keywords through APPEND, COPY and MOVE. | n/a: not supported yet. FlagUpdate has no keyword storage and APPEND/COPY cannot preserve arbitrary keywords. Existing IMAP-STORE-06 honestly withholds keyword creation; rest-mail/rest-mail-server#374 and #718. |
| IMAP2-CORE-19 | RFC 9051 §2.3.3 | SHOULD | Preserve the source internal date on COPY/MOVE and honor APPEND arrival-date semantics; SMTP delivery dates come from final delivery. | n/a: the host assigns delivery dates, Copy/Move do not carry metadata, and explicit APPEND dates require DateAppender. Same gaps as IMAP-COPY-12 and IMAP-APPEND-20; rest-mail/rest-mail-server#374. |
| IMAP2-CORE-20 | RFC 9051 §2.3.4 | SHOULD | Report the number of octets in the complete message representation returned by BODY[], not a character count or rendered text length. | TestFetchSizeMatchesBodyOctets |
| IMAP2-CORE-21 | RFC 9051 §3.1 | MUST | Require authentication before authenticated and selected-state commands are permitted. | TestGreeting |
| IMAP2-CORE-22 | RFC 9051 §3.2 | MUST | Require a selected mailbox before message commands, including after authentication has succeeded. | TestFetchRequiresSelection, TestCopyRequiresSelection, TestMoveRequiresSelection, TestSearchRequiresSelection, TestExpungeRequiresSelection |
| IMAP2-CORE-23 | RFC 9051 §3.4 | MUST | On LOGOUT send an untagged BYE and tagged OK before closing the connection. | TestLogout |
| IMAP2-CORE-24 | RFC 9051 §3.4 | SHOULD NOT | Close a connection unilaterally without first sending BYE with its reason; peer EOF may omit BYE. | n/a: Server shutdown, autologout and panic handling are outside the public Session wire harness. Root tests are named by IMAP-STATE-01, IMAP-RESP-14 and IMAP-AUTOLOGOUT-01; this inventory adds no new claim about those paths. |
| IMAP2-CORE-26 | RFC 9051 §4.3 | MUST NOT | Send non-synchronizing literals server-to-client; accept client non-synchronizing literals only within the 4096-octet baseline limit and send no continuation for them. | TestLiteralMinus, TestLiteralMinusLimit, TestFetchSizeMatchesBodyOctets |
| IMAP2-CORE-29 | RFC 9051 §4.3 | MUST | Accept well-formed UTF-8 in quoted strings without NUL, CR or LF as a rev2 baseline rather than an optional extension. | n/a: IMAP4rev2 mode is not implemented (#718). Optional UTF8=ACCEPT is tested by IMAP-UTF8-01, but is not the rev2 baseline. Rev1 typed-text refusal and literal framing are proved by IMAP-SYNTAX-06 and IMAP-SYNTAX-07 (rest-mail/go-imap#150). |
| IMAP2-CORE-30 | RFC 9051 §4.3.1 | MUST | Encode binary data outside BINARY/BINARY.PEEK literal8 responses into a textual transfer encoding before transmission. | n/a: transfer encoding belongs to the host message representation; the engine does not rewrite BODY[] content. Rev2 baseline and compatibility need rest-mail/rest-mail-server#718; existing BINARY rows cover only the offered extension. |
| IMAP2-CORE-31 | RFC 9051 §5.1 | MUST | Require Net-Unicode mailbox names, rejecting invalid creation names or normalizing and consistently resolving accepted decomposed UTF-8 names. | n/a: rev2 naming is not implemented (#718); UTF8=ACCEPT validation is an optional rev1 extension and does not establish NFC normalization or rev2 default naming. Gap: rest-mail/rest-mail-server#1669. |
| IMAP2-CORE-32 | RFC 9051 §5.1.1 | MUST | Use a single ASCII hierarchy delimiter consistently in left-to-right hierarchical mailbox names. | TestListReference, TestNamespace |
| IMAP2-CORE-33 | RFC 9051 §5.1.2 | MUST | Place INBOX in the personal namespace; normally expose only one personal namespace. | TestNamespace |
| IMAP2-CORE-34 | RFC 9051 §5.1.2 | MUST | Require an explicit grant before exposing another user's personal namespace; normally expose at most one other-users namespace. | n/a: other-users namespaces are not offered. NAMESPACE reports NIL for that class (IMAP-NAMESPACE-01); authentication and mailbox access control belong to the host backend. |
| IMAP2-CORE-35 | RFC 9051 §5.2 | MUST | Report observed mailbox size changes automatically during a command, and SHOULD report observed message flag changes without explicit flag requests. | TestNoopReportsOtherSourcesChanges |
| IMAP2-CORE-37 | RFC 9051 §5.3 | MUST | If sending unsolicited responses while no command is in progress, use nonblocking writes or respect available transport window capacity. | TestNoUntaggedResponseWithoutCommand |
| IMAP2-CORE-38 | RFC 9051 §5.4 | MUST | Allow at least thirty minutes before authenticated inactivity autologout and reset its timer on any received command. | n/a: root-package timer behavior, beyond the public Session harness; IMAP-AUTOLOGOUT-01 names TestAutologoutIsAtLeastThirtyMinutes and TestSession_CommandActivityResetsAutologout. No new timer claim is inferred. |
| IMAP2-CORE-40 | RFC 9051 §5.5 | MUST | When pipelined commands could be ambiguous, complete them in the order the client supplied. | TestPipelinedCommandsRunInOrder |

## RFC 9051: command inventory (sections 6.1–6.3.6; first pass)

Continuation of the rev2 requirement audit. The older RFC and extension rows
remain intact; a test reference proves the described shared behavior, not rev2
capability negotiation. Client-only recommendations for TLS capability caching,
avoiding insecure LOGIN and selecting ENABLE arguments are outside this engine.
Required-response lists in the command synopsis are accounted for by the
corresponding command rows rather than counted as separate behavior.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP2-CMD-44 | RFC 9051 §6.1.1 | MUST | Return exactly one untagged CAPABILITY containing IMAP4rev2 before successful completion. | n/a: IMAP4rev2 is not offered yet. The current engine advertises IMAP4rev1 only; rest-mail/rest-mail-server#718. |
| IMAP2-CMD-45 | RFC 9051 §6.1.1 | MUST | Implement STARTTLS and LOGINDISABLED on cleartext ports, and AUTH=PLAIN with the protected authentication configuration. | TestStartTLSExample, TestStartTLSRefusals |
| IMAP2-CMD-47 | RFC 9051 §6.1.3 | MUST | Return BYE before the tagged LOGOUT completion and then close the transport. | TestLogout |
| IMAP2-CMD-48 | RFC 9051 §6.2.1 | MUST | Answer STARTTLS on an already protected connection with BAD, keeping it usable. | TestStartTLSRefusals |
| IMAP2-CMD-49 | RFC 9051 §6.2.1 | MUST | Begin TLS after the STARTTLS OK CRLF, without executing plaintext pipelined after STARTTLS inside the protected session. | TestStartTLSExample, TestStartTLSDiscardsPipelinedPlaintext, TestStartTLSFailedNegotiation |
| IMAP2-CMD-50 | RFC 9051 §6.2.1 | SHOULD NOT | Continue advertising STARTTLS after TLS has started. | TestStartTLSExample |
| IMAP2-CMD-51 | RFC 9051 §6.2.2 | SHOULD | Reject an unsupported SASL mechanism with tagged NO rather than starting an exchange. | TestAuthenticateCommand |
| IMAP2-CMD-52 | RFC 9051 §6.2.2 | MUST | Frame the SASL exchange with base64 continuations; reject cancellation and invalid base64 with BAD while preserving unauthenticated state. | TestAuthenticateCommand, TestSASLIR |
| IMAP2-CMD-53 | RFC 9051 §6.2.2 | MUST | Require the initial response as an unquoted base64 token, with a single equals sign representing an empty response. | TestSASLIR |
| IMAP2-CMD-54 | RFC 9051 §6.2.2 | MUST | Reject malformed initial-response base64, including misplaced padding, with tagged BAD. | TestSASLIR |
| IMAP2-CMD-55 | RFC 9051 §6.2.2 | MUST | Reject an initial response when the chosen mechanism cannot take one. | n/a: the only offered SASL mechanism is PLAIN, which permits an initial response. Other mechanisms are not offered (IMAP-AUTHENTICATE-10). |
| IMAP2-CMD-56 | RFC 9051 §6.2.2 | MUST | Implement AUTHENTICATE and at least the PLAIN mechanism; additional SASL mechanisms and security layers are not mandatory. | TestAuthenticateCommand, TestSASLIR |
| IMAP2-CMD-57 | RFC 9051 §6.2.2 | MUST | Provide a configuration that refuses plaintext password mechanisms until TLS protects the connection. | TestStartTLSCleartextCredentialsRefused |
| IMAP2-CMD-57-02 | RFC 9051 §6.2.2 | SHOULD | Offer additional non-plaintext SASL mechanisms such as GSSAPI, SCRAM-SHA-256 or EXTERNAL. | n/a: only PLAIN is offered; the backend supplies password authentication, not a challenge-response or TLS identity interface. See IMAP-AUTHENTICATE-10; rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1670. |
| IMAP2-CMD-58 | RFC 9051 §6.2.2 | SHOULD | List offered authentication mechanisms in CAPABILITY. | TestAuthenticateCommand, TestStartTLSExample |
| IMAP2-CMD-59 | RFC 9051 §6.2.2 | MUST | Support the continuation exchange when a client omits its optional SASL initial response. | TestAuthenticateCommand, TestSASLIR |
| IMAP2-CMD-62 | RFC 9051 §6.2.3 | MUST | Provide a protected deployment configuration that advertises LOGINDISABLED and refuses LOGIN before TLS. | TestStartTLSExample, TestStartTLSCleartextCredentialsRefused |
| IMAP2-CMD-63 | RFC 9051 §6.3.1 | MUST | Ignore ENABLE arguments that name unknown extensions. | TestEnableUnknownAndCapabilityStability, TestEnableAdditiveAndPersistent |
| IMAP2-CMD-64 | RFC 9051 §6.3.1 | MUST | Ignore known capabilities that cannot be enabled with ENABLE. | TestEnableUnknownAndCapabilityStability |
| IMAP2-CMD-65 | RFC 9051 §6.3.1 | MUST | Enable a supported enableable extension for the connection, without a way to disable it later. | TestEnableAdditiveAndPersistent |
| IMAP2-CMD-66 | RFC 9051 §6.3.1 | MUST | Send ENABLED on every successful ENABLE, including when no extension was enabled. | TestEnableUnknownAndCapabilityStability, TestEnableAdditiveAndPersistent |
| IMAP2-CMD-69 | RFC 9051 §6.3.1 | SHOULD | Make successive ENABLE commands additive and report only extensions newly enabled by that command. | TestEnableAdditiveAndPersistent |
| IMAP2-CMD-70 | RFC 9051 §6.3.1 | MUST NOT | Change the CAPABILITY list as a consequence of ENABLE. | TestEnableUnknownAndCapabilityStability, TestEnableAdditiveAndPersistent |
| IMAP2-CMD-72 | RFC 9051 §6.3.2–6.3.3 | MUST | Before successful SELECT or EXAMINE return FLAGS, EXISTS, LIST, PERMANENTFLAGS, UIDNEXT and UIDVALIDITY. | n/a: rev1 selection returns the older required set without the rev2 LIST response. UID persistence also has the base-backend gap in IMAP-SELECT-09; rest-mail/rest-mail-server#718. |
| IMAP2-CMD-73 | RFC 9051 §6.3.2 | MUST | Return accurate selected-mailbox LIST attributes, and OLDNAME when accepting a normalized form of the supplied UTF-8 name. | n/a: selected-mailbox LIST and OLDNAME normalization responses are not implemented; rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1669. |
| IMAP2-CMD-74 | RFC 9051 §6.3.2 | MUST | Deselect the old mailbox before attempting another SELECT, leaving no selection on failure, and announce closure with CLOSED. | n/a: the deselection/state behavior is tested by IMAP-SELECT-07. An IMAP4rev2 session is sent `* OK [CLOSED]` first when SELECT or EXAMINE, successful or not, replaces a selection, and not after CLOSE or UNSELECT (TestSelectClosedRev2, TestSelectClosedRev2OnFailure and TestSelectClosedRev2NotAfterClose in package server), but no command makes a session rev2 until ENABLE IMAP4rev2; rest-mail/rest-mail-server#718. Rev1 sessions are IMAP-CLOSED-01. |
| IMAP2-CMD-75 | RFC 9051 §6.3.2 | SHOULD | Identify a successful writable SELECT with READ-WRITE in its tagged completion. | TestSelectionExamples |
| IMAP2-CMD-76 | RFC 9051 §6.3.2 | MUST | When access permits reading but not writing, SELECT the mailbox read-only and announce READ-ONLY. | n/a: only EXAMINE explicitly makes a selection read-only; the backend cannot report read-only permissions to SELECT (IMAP-SELECT-10). Tracked in rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1671. |
| IMAP2-CMD-78 | RFC 9051 §6.3.3 | MUST | Begin the successful EXAMINE tagged response text with READ-ONLY. | TestSelectionExamples |
| IMAP2-CMD-79 | RFC 9051 §6.3.4 | MUST | Only acknowledge CREATE after creating a new mailbox; refuse extant names and invalid Net-Unicode, or normalize and report OLDNAME. | n/a: no backend mailbox-creation operation or Unicode normalization interface exists. CREATE can acknowledge an extant name (IMAP-CREATE-02); rest-mail/rest-mail-server#374 and #718. Gap: rest-mail/rest-mail-server#1669. |
| IMAP2-CMD-80 | RFC 9051 §6.3.4 | MUST | Include Subscribed when an unsolicited LIST announces a newly created mailbox that was automatically subscribed. | n/a: the engine neither auto-subscribes created mailboxes nor sends unsolicited LIST responses. |
| IMAP2-CMD-81 | RFC 9051 §6.3.4 | MUST | Treat a trailing hierarchy delimiter as a declaration and create the name without that delimiter. | n/a: no durable backend CREATE exists; the trailing declaration is not implemented as a mailbox-creation operation (IMAP-CREATE-03). Tracked in rest-mail/rest-mail-server#374. |
| IMAP2-CMD-82 | RFC 9051 §6.3.4 | SHOULD | Create necessary superior hierarchy names during CREATE. | n/a: no backend CREATE operation exists (IMAP-CREATE-03); rest-mail/rest-mail-server#374. |
| IMAP2-CMD-83 | RFC 9051 §6.3.4 | MUST | Avoid reusing a deleted incarnation's UIDs unless the recreated mailbox has a different UIDVALIDITY. | n/a: allocation and persistent UID identity belong to the host backend (IMAP-CREATE-04); rest-mail/rest-mail-server#374. |
| IMAP2-CMD-84 | RFC 9051 §6.3.5 | MUST NOT | Remove inferior hierarchy names when deleting their parent; refuse deleting a Noselect parent that has inferiors. | n/a: preservation of inferior messages is tested by IMAP-DELETE-03, but the engine cannot distinguish Noselect parents when deleting; its backend has no durable DELETE operation. Tracked in rest-mail/rest-mail-server#374. |
| IMAP2-CMD-85 | RFC 9051 §6.3.5 | SHOULD | When refusing deletion because a mailbox has inferiors, return NO with HASCHILDREN. | n/a: the current engine does not refuse this case and offers no backend hierarchy-deletion policy; rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1671. |
| IMAP2-CMD-86 | RFC 9051 §6.3.5 | MUST | Preserve the deleted mailbox's highest UID or change UIDVALIDITY on recreation. | n/a: persistent UID allocation is a host-backend obligation (IMAP-DELETE-05); rest-mail/rest-mail-server#374. |
| IMAP2-CMD-87 | RFC 9051 §6.3.5 | SHOULD | When normalizing a DELETE name, report the normalized mailbox and OLDNAME with NonExistent. | n/a: the engine does not normalize mailbox names or offer OLDNAME responses; rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1669. |
| IMAP2-CMD-88 | RFC 9051 §6.3.6 | MUST | Rename the inferior hierarchy along with its parent, preserving unrelated prefix matches. | TestDeleteRenameHierarchy |
| IMAP2-CMD-89 | RFC 9051 §6.3.6 | SHOULD | Create necessary superior hierarchy names for a RENAME target. | n/a: the backend has no durable rename/create interface and implicit folders are not materialized (IMAP-RENAME-01); rest-mail/rest-mail-server#374. |
| IMAP2-CMD-90 | RFC 9051 §6.3.6 | MUST | Preserve the old name's highest assigned UID when renaming, unless a later incarnation uses another UIDVALIDITY. | n/a: UID allocation is the host's persistent-store responsibility (IMAP-RENAME-04); rest-mail/rest-mail-server#374. |

## RFC 9051: subscription, listing and APPEND inventory (first pass)

Continuation through section 6.3.13. Client-only hierarchy-browser assumptions, CHILDINFO race handling, inferred attributes and namespace choices are outside this server engine. Existing rev1/extension proofs remain separate from the rev2 differences recorded here.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP2-CMD-91 | RFC 9051 §6.3.6 | SHOULD | Report OLDNAME when RENAME normalizes either supplied name. | n/a: Unicode normalization and OLDNAME responses are not implemented; rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1669. |
| IMAP2-CMD-92 | RFC 9051 §6.3.6 | MUST | Include Subscribed in unsolicited LIST responses for renamed mailboxes that were automatically subscribed. | n/a: the engine does not auto-subscribe renamed mailboxes or send unsolicited rename LIST responses. |
| IMAP2-CMD-93 | RFC 9051 §6.3.7 | SHOULD NOT | Remove a subscribed name merely because the corresponding mailbox no longer exists. | n/a: a durable subscription list is not implemented. Existing folders are treated as subscribed and SUBSCRIBE/UNSUBSCRIBE do not persist changes (IMAP-SUBSCRIBE-01); rest-mail/rest-mail-server#374. |
| IMAP2-CMD-94 | RFC 9051 §6.3.9 | SHOULD | Avoid unnecessary message/count/content work for LIST when STATUS data was not requested. | TestListMetadataDoesNotReadMessages |
| IMAP2-CMD-94-02 | RFC 9051 §6.3.9 | SHOULD | Return large mailbox listings without undue delay, including hierarchy processing. | n/a: bounded hierarchy-processing cost has not been established. list.go scans the full entry list for descendants for each result, even without CHILDREN. The metadata-only proof above does not prove latency or linear complexity; rest-mail/rest-mail-server#374. |
| IMAP2-CMD-95 | RFC 9051 §6.3.9 | MUST | Return only mailbox names matching the canonical patterns in the supplied reference context. | TestListReference, TestListMultiplePatterns |
| IMAP2-CMD-97 | RFC 9051 §6.3.9 | SHOULD | Keep an included reference prefix in its original form so clients can recognize the naming context. | TestListReference |
| IMAP2-CMD-99 | RFC 9051 §6.3.9 | MUST | Complete a valid pattern with no accepted matches as OK with no LIST data. | TestListReference |
| IMAP2-CMD-100 | RFC 9051 §6.3.9 | MUST NOT | Add mailbox names merely because return options were supplied; retain the pattern and selection criteria. | TestListExtendedChildren, TestListExtendedRecursiveMatch, TestListMultiplePatterns |
| IMAP2-CMD-101 | RFC 9051 §6.3.9 | MUST | Support initial LIST options, reject unknown options with BAD, and treat repeated options as occurring once regardless of order. | n/a: duplicate STATUS return options are deliberately rejected by parseList/listReturnOptions and TestListStatusRefusals, unlike this rev2 rule. Selection options and other return options are deduplicated; rest-mail/rest-mail-server#718. |
| IMAP2-CMD-102 | RFC 9051 §6.3.9.1 | MUST | SUBSCRIBED selection returns exactly subscribed names matching the patterns, including subscribed nonexistent mailboxes. | n/a: no persistent subscription list exists; all existing folders are treated as subscribed, so nonexistent subscriptions and unsubscription cannot be represented. See IMAP-SUBSCRIBE-01; rest-mail/rest-mail-server#374. |
| IMAP2-CMD-103 | RFC 9051 §6.3.9.1 | MUST | Compute Subscribed accurately when SUBSCRIBED selection is requested. | n/a: true subscription state is not stored. The offered extension marks every existing folder subscribed; rest-mail/rest-mail-server#374. |
| IMAP2-CMD-104 | RFC 9051 §6.3.9.1 | MUST | Compute Remote accurately when REMOTE is requested; local mailboxes are not labeled remote. | TestListExtendedChildren |
| IMAP2-CMD-105 | RFC 9051 §6.3.9.1 | MUST | Reject RECURSIVEMATCH alone, or combined only with REMOTE, with tagged BAD. | TestListExtendedRefusals |
| IMAP2-CMD-107 | RFC 9051 §6.3.9.2 | MUST | Compute subscription state and every other attribute accurately under the SUBSCRIBED return option. | n/a: persistent subscription state is not supported; current shared LIST attributes are tested, but all folders are treated as subscribed. See IMAP2-CMD-102; rest-mail/rest-mail-server#374. |
| IMAP2-CMD-108 | RFC 9051 §6.3.9.2 | MUST | For matching selectable mailboxes, follow each LIST with the requested STATUS response. | TestListStatus, TestListExtendedChildren |
| IMAP2-CMD-109 | RFC 9051 §6.3.9.2 | MUST NOT | Send STATUS when its lookup cannot select the listed mailbox, and mark that LIST result Noselect instead. | n/a: the current engine omits failed STATUS but sends LIST before reading statusData, so it cannot add Noselect on failure. The existing RFC5819 fixture preserves that behavior; rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1671. |
| IMAP2-CMD-111 | RFC 9051 §6.3.9.5 | MUST | Return HasChildren or HasNoChildren when the CHILDREN return option was requested, including non-mailbox hierarchy levels. | TestListExtendedChildren |
| IMAP2-CMD-112 | RFC 9051 §6.3.9.5 | SHOULD NOT | Advertise children when none are accessible to the authenticated user; prefer HasNoChildren in that case. | n/a: the host supplies the accessible folder view and enforces access. The engine has no separate ACL or hidden-child information to audit this distinction. |
| IMAP2-CMD-113 | RFC 9051 §6.3.9.6 | MUST NOT | Return CHILDINFO unless RECURSIVEMATCH was requested. | TestListExtendedRecursiveMatch, TestListExtendedChildren |
| IMAP2-CMD-114 | RFC 9051 §6.3.9.6 | SHOULD | Use NonExistent with HasChildren for an absent parent of a matching descendant when CHILDINFO is unavailable. | TestListExtendedChildren |
| IMAP2-CMD-115 | RFC 9051 §6.3.9.6 | SHOULD | Suppress redundant CHILDINFO when the matching child is itself returned. | TestListExtendedRecursiveMatch |
| IMAP2-CMD-116 | RFC 9051 §6.3.9.7 | SHOULD | Return OLDNAME when a supplied mailbox name differs from the normalized name used by SELECT, EXAMINE, CREATE, RENAME or DELETE. | n/a: mailbox normalization and OLDNAME are not implemented; rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1669. |
| IMAP2-CMD-119 | RFC 9051 §6.3.10 | SHOULD | Use the authenticated user identifier as the hierarchy level beneath an other-users namespace prefix. | n/a: no other-users namespace is offered; NAMESPACE returns NIL for it, as tested in IMAP-NAMESPACE-01. |
| IMAP2-CMD-120 | RFC 9051 §6.3.10 | SHOULD NOT | List other users whose personal mailboxes have not been shared with the authenticated user. | n/a: no other-users namespace or user-directory listing is offered; mailbox authorization is the backend's responsibility. |
| IMAP2-CMD-122 | RFC 9051 §6.3.11 | MUST | Execute STATUS on the selected mailbox without disturbing its selection. | TestStatusPreservesSelection |
| IMAP2-CMD-124 | RFC 9051 §6.3.11 | MUST | Report a mailbox SIZE at least as large as the sum of its messages' RFC822.SIZE values. | TestStatusSize |
| IMAP2-CMD-125 | RFC 9051 §6.3.12 | MUST | Preserve 8-bit APPEND messages or reversibly encode them when necessary. | n/a: the rev2 baseline accepting UTF-8 headers is not implemented; current rev1 requires ENABLE UTF8=ACCEPT for 8-bit headers and otherwise refuses them (IMAP-UTF8-05). Body octets are preserved; rest-mail/rest-mail-server#718. |
| IMAP2-CMD-126 | RFC 9051 §6.3.12 | SHOULD | Apply requested APPEND flags, defaulting to an empty list when omitted. | n/a: FlagUpdate cannot carry arbitrary keywords or Deleted through APPEND, although supported flags are tested. See IMAP-APPEND-19; rest-mail/rest-mail-server#374. |
| IMAP2-CMD-127 | RFC 9051 §6.3.12 | SHOULD | Honor an explicit APPEND internal date, otherwise use the current arrival time. | n/a: explicit dates require DateAppender and fallback backends drop them; default arrival metadata belongs to the backend. See IMAP-APPEND-20; rest-mail/rest-mail-server#374. |
| IMAP2-CMD-128 | RFC 9051 §6.3.12 | MUST | On APPEND failure restore the mailbox to its previous state, apart from a possible UIDNEXT advance. | n/a: atomic persistence is the host Append/AppendUID contract. The engine sends one complete payload but has no rollback API if a backend writes partially before returning an error. |
| IMAP2-CMD-129 | RFC 9051 §6.3.12 | MUST | Refuse APPEND to a nonexistent mailbox without creating it, using TRYCREATE unless creation is known impossible. | n/a: the engine relies on the backend to refuse a missing destination; generic Append backends may create implicitly. Typed ErrNoSuchMailbox is supported, but does not establish the rev2 guarantee for every backend; rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1671. |
| IMAP2-CMD-130 | RFC 9051 §6.3.12 | MUST NOT | Disclose APPENDUID to a caller that can append but cannot read the destination. | n/a: destination readability is not exposed by the backend (IMAP-APPENDUID-05); rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1671. |
| IMAP2-CMD-131 | RFC 9051 §6.3.12 | SHOULD | Notify a selected mailbox of newly appended mail using EXISTS. | TestAppendSelectedMailbox |
| IMAP2-CMD-132 | RFC 9051 §6.3.12 | SHOULD | Report OLDNAME when APPEND normalizes the destination mailbox name. | n/a: mailbox normalization and OLDNAME are not implemented; rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1669. |
| IMAP2-CMD-133 | RFC 9051 §6.3.13 | MUST | Include UID in unsolicited FETCH updates sent during IDLE. | n/a: current IDLE flag updates contain FLAGS without UID. Rev2 unsolicited FETCH rules are not implemented; rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1668. |

## RFC 9051: selected-state command inventory (first pass)

The supported ESEARCH and SEARCHRES extensions have shared proofs, while the rev2 defaults are recorded separately. Client duties about ignoring legacy SEARCH responses, result ordering, sending commands during IDLE, and synchronizing request continuations do not apply to this engine. Backend atomicity and persistent message data are not inferred from dispatch fixtures.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP2-CMD-134 | RFC 9051 §6.3.13 | MUST | Finish IDLE promptly after DONE, then accept the next command without interleaved polling replies. | TestIdleDoneAndNextCommand |
| IMAP2-CMD-135 | RFC 9051 §6.4.4 | MUST | Reject unimplemented SEARCH result options with BAD; result-option ordering is insignificant. | TestESearchRefusals, TestESearch |
| IMAP2-CMD-135-02 | RFC 9051 §6.4.4 | Normative | Use ESEARCH with ALL by default when SEARCH has no result-options list. | n/a: without RETURN the current rev1 engine sends SEARCH, not ESEARCH. RETURN () already means ALL; the rev2 default awaits rest-mail/rest-mail-server#718. |
| IMAP2-CMD-137 | RFC 9051 §6.4.4 | MUST NOT | Include MIN when no messages match; still send the ESEARCH response. | TestESearchNoMatches |
| IMAP2-CMD-138 | RFC 9051 §6.4.4 | MUST NOT | Include MAX when no messages match; still send the ESEARCH response. | TestESearchNoMatches |
| IMAP2-CMD-140 | RFC 9051 §6.4.4 | MUST NOT | Include ALL when no messages match; still send the ESEARCH response. | TestESearchNoMatches |
| IMAP2-CMD-141 | RFC 9051 §6.4.4 | MUST | Include the requested COUNT result even when its value is zero. | TestESearch, TestESearchNoMatches |
| IMAP2-CMD-142 | RFC 9051 §6.4.4 | MUST | Return a single ESEARCH response for the baseline MIN, MAX, ALL and COUNT options, alone or combined. | TestESearch, TestESearchNoMatches |
| IMAP2-CMD-143 | RFC 9051 §6.4.4 | MUST | Decode MIME transfer encodings and encoded header words before comparing SEARCH strings, and support US-ASCII and UTF-8. | TestSearchDecodesMIME, TestSearchCharsets |
| IMAP2-CMD-143-02 | RFC 9051 §6.4.4 | MUST | Assume UTF-8 when CHARSET is omitted in rev2. | n/a: rev2 search defaults are not implemented. The optional rev1 UTF8=ACCEPT path and explicit CHARSET handling are separate; rest-mail/rest-mail-server#718. |
| IMAP2-CMD-144 | RFC 9051 §6.4.4 | MUST | Return NO rather than BAD for an unsupported search CHARSET, with BADCHARSET and supported alternatives where supplied. | TestSearchCharsets |
| IMAP2-CMD-145 | RFC 9051 §6.4.4 | SHOULD | Compare ASCII search text case-insensitively and use substring matching, including empty HEADER strings as field-presence tests. | TestSearchDecodesMIME, TestSearchHeaderKeys |
| IMAP2-CMD-146 | RFC 9051 §6.4.4.1 | MUST NOT | Change the saved-search variable for a search that does not successfully replace it, including a BAD command or a search without SAVE. | TestSearchResVariable |
| IMAP2-CMD-147 | RFC 9051 §6.4.4.1 | MUST | Remove expunged messages from the saved result and preserve its correct message identities after sequence renumbering. | TestSearchResVariable |
| IMAP2-CMD-148 | RFC 9051 §6.4.4.2 | MUST | Run a pipelined SAVE search and a following command using its saved result in arrival order. | TestSearchRes |
| IMAP2-CMD-149 | RFC 9051 §6.4.4.3 | MUST | If refusing to save a search result because of resource limits, return NO with NOTSAVED and clear the saved result. | n/a: this implementation has no configured saved-result resource limit and no NOTSAVED refusal. Backend NO clears the result as tested in IMAP-SEARCHRES-04; resource-limit refusal remains rest-mail/rest-mail-server#718. |
| IMAP2-CMD-150 | RFC 9051 §6.4.5 | MUST NOT | Change static message attributes for one UID identity. | n/a: immutable message content and metadata are host-backend obligations, not enforced against a backend that replaces its data. See IMAP2-CORE-13. |
| IMAP2-CMD-151 | RFC 9051 §6.4.5 | SHOULD | Report flag changes caused by implicit Seen setting in FETCH responses. | TestFetchSections, TestUIDFetchExample |
| IMAP2-CMD-152 | RFC 9051 §6.4.5.1 | MUST | Number MIME parts consecutively and address nested multipart/message parts using period-separated part numbers. | TestFetchNumericSections, TestFetchNumericSectionSingle |
| IMAP2-CMD-153 | RFC 9051 §6.4.5.1 | MUST | Permit HEADER/TEXT selectors only at the root or on encapsulated RFC822/GLOBAL messages, and require a numeric prefix for MIME selectors. | n/a: RFC822 section syntax is tested by IMAP-FETCH-14 and invalid-section proofs, but MESSAGE/GLOBAL traversal is not implemented. Full rev2 message-type support awaits rest-mail/rest-mail-server#718. |
| IMAP2-CMD-154 | RFC 9051 §6.4.6 | SHOULD | Suppress a STORE command's own untagged flag reply when SILENT is used. | TestStoreSilent |
| IMAP2-CMD-155 | RFC 9051 §6.4.6 | SHOULD | Report observed external flag changes even when a previous STORE used SILENT. | TestStoreSilentStillReportsOtherSourcesChanges |
| IMAP2-CMD-156 | RFC 9051 §6.4.7 | SHOULD | Preserve flags and internal date during COPY. | n/a: host Copy/CopyUID owns persistence and the interface cannot carry session-local Deleted or keywords (IMAP-COPY-12); rest-mail/rest-mail-server#374. |
| IMAP2-CMD-157 | RFC 9051 §6.4.7 | MUST | Refuse a destination the backend identifies as nonexistent with NO and TRYCREATE, without engine-side creation. | TestTryCreateMissingDestination |
| IMAP2-CMD-157-02 | RFC 9051 §6.4.7 | MUST NOT | Have the host COPY operation implicitly create a nonexistent destination mailbox. | n/a: destination existence and durable creation policy are host-backend obligations. The engine has no create operation and maps ErrNoSuchMailbox, but cannot prevent a host Copy from auto-creating. |
| IMAP2-CMD-158 | RFC 9051 §6.4.7 | MUST | Restore the destination after any failed COPY of a message set, except for a possible UIDNEXT advance. | n/a: multi-message COPY iterates backend calls and has no transaction or rollback interface; earlier copies can remain after a later failure. Tracked in rest-mail/rest-mail-server#374. |
| IMAP2-CMD-159 | RFC 9051 §6.4.7 | MUST NOT | Disclose COPYUID when the caller may copy to the destination but cannot read it. | n/a: destination-read permissions are not exposed by the backend (IMAP-COPY-15); rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1671. |
| IMAP2-CMD-160 | RFC 9051 §6.4.8 | SHOULD | Preserve flags and internal date during MOVE. | n/a: the host Move/MoveUID owns persistence and metadata preservation; dispatch fixtures do not prove it (IMAP-MOVE-10); rest-mail/rest-mail-server#374. |
| IMAP2-CMD-161 | RFC 9051 §6.4.8 | MUST NOT | Generate STORE responses or set Deleted as intermediate MOVE steps. | TestMoveExample, TestMoveUIDExample |
| IMAP2-CMD-162 | RFC 9051 §6.4.8 | MUST | Make each message move atomic or leave it unaffected; never lose a message and avoid leaving duplicates even on partial command failure. | n/a: per-message atomicity is trusted to the host Move or Mover implementation and cannot be established by the dispatch fixture. Same boundary as IMAP-MOVE-10; rest-mail/rest-mail-server#374. |
| IMAP2-CMD-163 | RFC 9051 §6.4.8 | MUST | Refuse a backend-reported missing MOVE destination with NO and TRYCREATE, without engine-side creation. | TestTryCreateMissingDestination |
| IMAP2-CMD-163-02 | RFC 9051 §6.4.8 | MUST NOT | Have the host MOVE operation auto-create a missing destination. | n/a: the host controls destination existence and creation policy; the engine maps ErrNoSuchMailbox but has no separate durable existence/creation interface. |
| IMAP2-CMD-164 | RFC 9051 §6.4.8 | REQUIRED | Send COPYUID in an untagged OK before MOVE expunge responses. | n/a: this is conditional on optional UIDPLUS and valid backend UID metadata in the current rev1 engine. The ordering is tested by IMAP-MOVE-02, but unconditional rev2 UID metadata is not implemented; rest-mail/rest-mail-server#718. |
| IMAP2-CMD-165 | RFC 9051 §6.4.9 | MUST | Mark an ESEARCH response caused by UID SEARCH as UID and return UIDs rather than sequence numbers. | TestESearch |
| IMAP2-CMD-166 | RFC 9051 §6.4.9 | MUST | Keep FETCH/EXPUNGE prefixes as sequence numbers and include UID in FETCH responses caused by UID commands. | TestUIDFetchExample, TestUIDStoreReportsUID, TestUIDExpungeSubset |
| IMAP2-CMD-167 | RFC 9051 §6.5 | MUST | Associate non-core commands with an advertised capability. | n/a: the offered commands follow the rev1 and extension vocabulary, including commands deprecated by rev2. Rev2 mode and the revised command set are not implemented; rest-mail/rest-mail-server#718. |
| IMAP2-CMD-168 | RFC 9051 §6.5 | MUST NOT | Send added extension responses unless the client requested the associated command or ENABLE behavior. | TestEnableUnknownAndCapabilityStability, TestNoUntaggedResponseWithoutCommand |
| IMAP2-CMD-172 | RFC 9051 §7 | SHOULD | Report observed concurrent flag changes and expunges alongside mailbox-size updates. | TestNoopReportsOtherSourcesChanges |

## RFC 9051: response inventory (first pass)

Server responses are checked independently of clients' caching, unknown-code handling, ALERT presentation and BODYSTRUCTURE extension tolerance. Registered extension tags and capability names are specification/IANA concerns; this engine defines no private ones. The listed shared proofs do not imply that the engine negotiates rev2.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP2-RESP-174 | RFC 9051 §7.1 | MUST NOT | Use a UID set or range in APPENDUID for a single appended message. | TestAppendUIDResponse |
| IMAP2-RESP-175 | RFC 9051 §7.1 | MUST | Remove the keyword-creation wildcard from PERMANENTFLAGS when the keyword limit has been reached. | n/a: arbitrary keyword creation and a configurable keyword limit are not offered. PERMANENTFLAGS never includes the wildcard (IMAP-STORE-06). |
| IMAP2-RESP-176 | RFC 9051 §7.1 | SHOULD NOT | Use nonpersistent UID stores represented by UIDNOTSTICKY. | n/a: the engine has no UIDNOTSTICKY store metadata; persistent UID allocation is a host responsibility. The fallback UID identity gap is IMAP-SELECT-09; rest-mail/rest-mail-server#374. |
| IMAP2-RESP-178 | RFC 9051 §7.1.4 | SHOULD | Only use PREAUTH on a connection protected by TLS or another confidentiality layer. | n/a: the engine does not offer PREAUTH; every connection begins not authenticated with OK, as tested by IMAP-GREET-01. |
| IMAP2-RESP-180 | RFC 9051 §7.2.2 | MUST | Include IMAP4rev2 in a rev2 CAPABILITY response. | n/a: the engine currently advertises only IMAP4rev1; rest-mail/rest-mail-server#718. |
| IMAP2-RESP-181 | RFC 9051 §7.2.2 | MUST | Provide AUTH=PLAIN and the cleartext-port STARTTLS/LOGINDISABLED capabilities with protected authentication. | TestStartTLSExample, TestStartTLSRefusals |
| IMAP2-RESP-182 | RFC 9051 §7.2.2 | Normative | When LOGINDISABLED is advertised, refuse LOGIN with NO without checking supplied credentials. | TestStartTLSExample, TestStartTLSCleartextCredentialsRefused |
| IMAP2-RESP-183 | RFC 9051 §7.2.2 | MUST | Select the correct rev1/rev2 baseline response behavior from advertised capabilities and ENABLE negotiation. | n/a: dual-mode negotiation and ENABLE IMAP4rev2 are not implemented; rest-mail/rest-mail-server#718. |
| IMAP2-RESP-184 | RFC 9051 §7.2.2 | SHOULD NOT | Offer unregistered capability names; register newly defined names through IANA. | n/a: no new/private capability name is defined here. extensions.go lists published IMAP4rev1 and extension capabilities; registration is a specification-author action rather than engine behavior. |
| IMAP2-RESP-186 | RFC 9051 §7.3.1 | SHOULD NOT | Expose child existence through HasChildren when none of the children are accessible. | n/a: the host supplies the authorized folder view; the engine has no hidden-child ACL information, as in IMAP2-CMD-112. |
| IMAP2-RESP-187 | RFC 9051 §7.3.1 | MUST NOT | Combine Marked, Unmarked and Noselect attributes on one LIST result; omit unknown interest metadata. | TestListHierarchyLevels, TestListExtendedChildren |
| IMAP2-RESP-188 | RFC 9051 §7.3.1 | MUST | Use the same hierarchy separator for all children of a top-level hierarchy node. | TestListReference, TestNamespace |
| IMAP2-RESP-189 | RFC 9051 §7.3.1 | MUST | Return mailbox names that can be reused as unambiguous command arguments unless marked Noselect or NonExistent. | TestMailboxNamesAreWrittenInModifiedUTF7, TestMailboxNamesAreDecodedFromModifiedUTF7, TestMailboxNamedNIL |
| IMAP2-RESP-190 | RFC 9051 §7.3.1 | MUST | Use registered LIST extended-data tags; clients must ignore unrecognized extended fields. | n/a: no private extended-data tag is defined. CHILDINFO is registered by RFC5258; OLDNAME is not implemented. Client unknown-field handling does not apply to the server engine. |
| IMAP2-RESP-191 | RFC 9051 §7.3.4 | SHOULD | Return each ESEARCH result item only once, including when requested options repeat. | TestESearchDuplicateResultOptions |
| IMAP2-RESP-192 | RFC 9051 §7.3.4 | MUST NOT | Include MIN on an empty ESEARCH result, but still send the response. | TestESearchNoMatches |
| IMAP2-RESP-193 | RFC 9051 §7.3.4 | MUST NOT | Include MAX on an empty ESEARCH result, but still send the response. | TestESearchNoMatches |
| IMAP2-RESP-194 | RFC 9051 §7.3.4 | MUST | Return complete matching sequence/UID sets without ranges that include nonexistent or nonmatching UIDs. | TestESearch |
| IMAP2-RESP-195 | RFC 9051 §7.3.4 | MUST NOT | Include ALL on an empty ESEARCH result, but still send the response. | TestESearchNoMatches |
| IMAP2-RESP-196 | RFC 9051 §7.3.4 | MUST | Include a requested COUNT item even when the result count is zero. | TestESearch, TestESearchNoMatches |
| IMAP2-RESP-199 | RFC 9051 §7.5.1 | MUST NOT | Send EXPUNGE with no command in progress, during argument continuation negotiation, or while answering FETCH/STORE/SEARCH. | TestNoExpungeWhileLiteralPending, TestNoUntaggedResponseWithoutCommand, TestNoExpungeDuringFetchStoreSearch |
| IMAP2-RESP-201 | RFC 9051 §7.5.2 | SHOULD | Use a normal string for decoded 8-bit BINARY data without NUL, rather than literal8. | TestBinaryFetch |
| IMAP2-RESP-202 | RFC 9051 §7.5.2 | MUST | Canonicalize line-oriented textual BINARY output to CRLF regardless of stored line endings. | n/a: binaryData returns decoded text without CRLF normalization. Full rev2 textual BINARY behavior awaits rest-mail/rest-mail-server#718. |
| IMAP2-RESP-203 | RFC 9051 §7.5.2 | MUST | Refuse a BINARY decoding request with NO and UNKNOWN-CTE when the transfer encoding cannot be decoded. | TestBinaryUnknownCTE |
| IMAP2-RESP-204 | RFC 9051 §7.5.2 | MUST | Make BINARY.SIZE equal to the octets returned by the corresponding decoded BINARY item. | TestBinaryFetch |
| IMAP2-RESP-205 | RFC 9051 §7.5.2 | MUST | Refuse BINARY.SIZE when the transfer encoding cannot be decoded, using NO and UNKNOWN-CTE. | TestBinaryUnknownCTE |
| IMAP2-RESP-207 | RFC 9051 §7.5.2 | MUST NOT | Add a BODY origin offset unless the client requested a partial BODY item. | TestFetchSections, TestFetchPartialOctets |
| IMAP2-RESP-208 | RFC 9051 §7.5.2 | MUST | Transfer-encode binary data before returning it as ordinary BODY content. | n/a: ordinary message representation and transfer encoding belong to the host store; the engine returns stored BODY bytes without rewriting them. See IMAP2-CORE-30; rest-mail/rest-mail-server#718. |
| IMAP2-RESP-209 | RFC 9051 §7.5.2 | MUST | Keep multipart BODYSTRUCTURE extension fields in order and omit them from BODY. | TestBodyStructureRFCMultipartExample, TestBodyStructureMultipartLocation |
| IMAP2-RESP-210 | RFC 9051 §7.5.2 | SHOULD | Decode and concatenate RFC2231 media-type parameter continuations and character sets. | TestBodyStructureContinuedParameters |
| IMAP2-RESP-210-02 | RFC 9051 §7.5.2 | Normative | Preserve the rev2 parameter-name/character-set convention for extended UTF-8 values. | n/a: the current MIME formatter uses the decoded base parameter name, rather than the RFC9051 extended-name illustration. Full rev2 parameter representation remains rest-mail/rest-mail-server#718. |
| IMAP2-RESP-211 | RFC 9051 §7.5.2 | SHOULD | Decode RFC2231 Content-Disposition parameter continuations. | TestBodyStructureContinuedParameters |
| IMAP2-RESP-212 | RFC 9051 §7.5.2 | MUST NOT | Send undefined future BODYSTRUCTURE extension fields. | n/a: the formatter has no private/future extension fields; it emits only defined MD5, disposition, language and location fields. Client tolerance of later fields is not a server obligation. |
| IMAP2-RESP-213 | RFC 9051 §7.5.2 | MUST | Keep single-part BODYSTRUCTURE extension fields in order and omit them from BODY. | TestBodyStructureRFCExample, TestBodyStructureMD5Location |
| IMAP2-RESP-215 | RFC 9051 §7.5.2 | MUST | Include UID in unsolicited FETCH updates. | n/a: current NOOP/IDLE flag updates can contain FLAGS without UID, as recorded in IMAP2-CMD-133. Rev2 update formatting awaits rest-mail/rest-mail-server#718. Gap: rest-mail/rest-mail-server#1668. |

## RFC 9051: grammar, security and compatibility inventory

The normative grammar notes and security requirements below are separate from a complete audit of every ABNF production. TLS runtime policy, backend account policy and IANA registration duties are distinguished from Session wire behavior. Client-only certificate verification, connection-port selection, alert presentation and unknown-response handling are not server-engine obligations. Appendices A and A.1 are recorded in the following compatibility rows; Appendix E repeats changes rather than creating extra requirements, and the index is not normative protocol text.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP2-SEC-216 | RFC 9051 §9 | MUST | Give the earlier grammar alternative priority when productions overlap, including system flags versus extension flags. | TestStoreExample, TestSearchAnsweredDraftKeys |
| IMAP2-SEC-217 | RFC 9051 §9 | MUST | Follow the rev2 ABNF, including exact spaces, complete argument forms and string framing. | n/a: shared rev1 grammar is extensively tested, but rev2 adds number64 and UTF-8 defaults and removes legacy constructs; those productions and mode selection are not implemented. Full rev2 grammar awaits rest-mail/rest-mail-server#718. |
| IMAP2-SEC-218 | RFC 9051 §9 | MUST | Treat protocol command and keyword spelling case-insensitively unless a production says otherwise. | TestCaseAndSpacing |
| IMAP2-SEC-219 | RFC 9051 §9 | MUST NOT | Accept ASCII NUL except where the OCTET production permits it. | TestCaseAndSpacing, TestAppendRejectsNULMessageOctet |
| IMAP2-SEC-220 | RFC 9051 §9 | MUST NOT | Generate future BODYSTRUCTURE extension fields before a standards-track definition permits them. | n/a: the formatter defines no private/future extension fields; only the specified MD5, disposition, language and location are emitted. |
| IMAP2-SEC-221 | RFC 9051 §9 | MUST NOT | Return single-part extension data in the nonextensible BODY form. | TestBodyStructureRFCExample, TestBodyStructureMD5Location |
| IMAP2-SEC-222 | RFC 9051 §9 | MUST NOT | Return multipart extension data in the nonextensible BODY form. | TestBodyStructureRFCMultipartExample, TestBodyStructureMultipartLocation |
| IMAP2-SEC-223 | RFC 9051 §9 | MUST NOT | Format MESSAGE/RFC822 or MESSAGE/GLOBAL using the generic basic-body production. | n/a: RFC822 has structured traversal proofs, but MESSAGE/GLOBAL structured handling is not implemented. Full rev2 message-type support awaits rest-mail/rest-mail-server#718. |
| IMAP2-SEC-224 | RFC 9051 §9 | SHOULD | Register newly defined capabilities with IANA under the RFC-required policy. | n/a: no new/private capability is defined by this library; registration is a specification-author process. See IMAP2-RESP-184. |
| IMAP2-SEC-225 | RFC 9051 §9 | MUST | Advertise IMAP4rev2, include IMAP4rev1 when compatibility is offered, and put IMAP4 first if RFC1730 compatibility is offered. | n/a: no rev2 or RFC1730 mode is offered; current capabilities advertise only the rev1 baseline. Dual-mode grammar awaits rest-mail/rest-mail-server#718. |
| IMAP2-SEC-226 | RFC 9051 §9 | MUST NOT | Generate undefined flag extensions, and preserve the distinction between rev2 flags and deprecated Recent. | n/a: current rev1 exposes Recent and does not negotiate the rev2 flag vocabulary. No private system flag is defined; rev2 compatibility awaits rest-mail/rest-mail-server#718. |
| IMAP2-SEC-227 | RFC 9051 §9 | MUST | Interpret every ASCII case variant of INBOX as the distinguished inbox rather than an ordinary case-sensitive name. | TestSelectionMailboxAstrings, TestInboxCannotBeCreatedOrDeleted, TestStatusRequestedItems |
| IMAP2-SEC-228 | RFC 9051 §9 | MUST | Format LIST extended-data tags as registered standard/vendor tag forms. | n/a: the only emitted extended tag is the standard CHILDINFO string. No vendor tag interface is offered; OLDNAME is not implemented. Gap: rest-mail/rest-mail-server#1669. |
| IMAP2-SEC-229 | RFC 9051 §9 | MUST NOT | Change static FETCH data for one UID identity. | n/a: immutable content and metadata are host-backend obligations, not enforced against replacement data. See IMAP2-CORE-13 and IMAP2-CMD-150. |
| IMAP2-SEC-230 | RFC 9051 §9 | MUST NOT | Use dot, slash, percent or star within a vendor name-component. | n/a: no vendor extension-name component is defined or accepted by this engine. |
| IMAP2-SEC-231 | RFC 9051 §9 | MUST | Use an IANA-registered character-set name in the SEARCH CHARSET argument. | n/a: this is a client argument-selection/registration requirement; the server's unsupported-charset refusal is checked by IMAP2-CMD-144. |
| IMAP2-SEC-232 | RFC 9051 §9 | MUST | Register vendor tokens with IANA. | n/a: the engine defines no vendor token; this is a specification/registration duty. |
| IMAP2-SEC-233 | RFC 9051 §11.1 | MUST | Follow the relevant TLS recommendations with the rev2 document taking precedence on conflicts. | n/a: cryptographic policy is the host-supplied tls.Config and Go TLS runtime. The Session harness proves negotiation and protection gates, not a complete RFC8314 cryptographic-profile audit; rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1672. |
| IMAP2-SEC-234 | RFC 9051 §11.1 | MUST | Support TLS1.2 or newer, the mandated TLS1.2 ECDHE-RSA/AES128-GCM suite, and the recommended TLS1.3/interoperability profile. | n/a: the host supplies certificates and tls.Config. Existing TLS handshake proofs do not establish the complete required cipher/certificate/profile matrix; rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1672. |
| IMAP2-SEC-236 | RFC 9051 §11.1 | MUST | Only continue after a successful STARTTLS result and acceptable TLS negotiation. | TestStartTLSExample, TestStartTLSFailedNegotiation |
| IMAP2-SEC-238 | RFC 9051 §11.2 | MUST | Offer an implicit-TLS listener and support STARTTLS if a cleartext listener is enabled. | n/a: implicit listeners are Server-level rather than this public Session wire harness. Root tests cover the protected capability path; TestStartTLSExample covers the cleartext upgrade. Full deployment/listener configuration remains a host obligation. Gap: rest-mail/rest-mail-server#1672. |
| IMAP2-SEC-241 | RFC 9051 §11.4 | SHOULD NOT | Disclose APPENDUID or COPYUID to a caller that cannot SELECT or EXAMINE the destination. | n/a: the host backend does not expose destination readability to the engine (IMAP-APPENDUID-05 and IMAP-COPY-15); rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1671. |
| IMAP2-SEC-242 | RFC 9051 §11.5 | MUST NOT | Reveal other users without an explicit personal-mailbox listing grant. | n/a: no other-users namespace is offered, and the host supplies the authorized mailbox view. See IMAP2-CMD-120. |
| IMAP2-SEC-243 | RFC 9051 §11.7 | SHOULD NOT | Reveal the reason an AUTHENTICATE credential is invalid. | TestAuthenticationFailureDetailsStayPrivate |
| IMAP2-SEC-244 | RFC 9051 §11.7 | MUST | Offer a configuration that protects credentials with TLS or rejects plaintext LOGIN/PLAIN and withholds their advertisement before protection. | TestStartTLSExample, TestStartTLSCleartextCredentialsRefused |
| IMAP2-SEC-245 | RFC 9051 §11.7 | SHOULD NOT | Distinguish an invalid LOGIN user name from an invalid password in the wire error. | TestAuthenticationFailureDetailsStayPrivate |
| IMAP2-SEC-246 | RFC 9051 §11.7 | SHOULD | Provide a mechanism that limits or delays failed LOGIN and AUTHENTICATE attempts. | TestAuthenticationFailureBudgetAppliesToBothForms |
| IMAP2-SEC-247 | RFC 9051 §11.7 | MUST | Detect password-spraying risks, block accounts with known weak passwords, and require a strong replacement password. | n/a: credential strength, account blocking and per-account spraying analysis are host authentication policy, not provided by this engine's per-IP Limiter interface; rest-mail/rest-mail-server#374. Gap: rest-mail/rest-mail-server#1672. |

## RFC 9051: compatibility appendices

Appendix A's client ENABLE obligation is client-only. Appendices B and C give compatibility advice for folded-in BINARY and LIST-EXTENDED; the shared extension rows above remain their wire proofs. Appendix D explicitly does not require servers to support 63-bit sizes, while clients must tolerate them. The engine uses native int sizes and still validates legacy SEARCH sizes as 32-bit values; no cross-architecture large-message or dual-mode size claim is made. Appendix E's change summary is cross-checked below rather than treated as a replacement for the normative section audit.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP2-COMPAT-249 | RFC 9051 §A | MUST NOT | Generate UTF-8 quoted strings in dual-mode operation until the client enables IMAP4rev2. | n/a: dual rev1/rev2 capability negotiation is not implemented. The existing UTF8=ACCEPT extension is separate; rest-mail/rest-mail-server#718. |
| IMAP2-COMPAT-250 | RFC 9051 §A | MUST | Preserve the rev1 international mailbox-name convention when offering rev1 compatibility. | TestMailboxNamesAreWrittenInModifiedUTF7, TestMailboxNamesAreDecodedFromModifiedUTF7 |
| IMAP2-COMPAT-251 | RFC 9051 §A.1 | MUST NOT | Encode printable ASCII unnecessarily in modified base64 or accept characters outside its alphabet. | TestInvalidModifiedUTF7NamesAreRefused |
| IMAP2-COMPAT-252 | RFC 9051 §A.1 | MUST | Terminate modified-base64 shifts explicitly and reject superfluous/null shifts. | TestInvalidModifiedUTF7NamesAreRefused |
| IMAP2-COMPAT-253 | RFC 9051 §A.1 | MUST | Preserve the case-sensitive modified-base64 portion of a mailbox name. | TestModifiedUTF7ShiftCaseIsSignificant |
| IMAP2-COMPAT-254 | RFC 9051 §A.1 | SHOULD | Validate CREATE names against canonical modified UTF-7 without superfluous shifts or encoded printable ASCII. | TestInvalidModifiedUTF7NamesAreRefused |
| IMAP2-COMPAT-255 | RFC 9051 §A.1 | MUST | Convert host mailbox names with non-ASCII or ampersand characters to modified UTF-7 for rev1 clients. | TestMailboxNamesAreWrittenInModifiedUTF7 |

## RFC 9051: Appendix E change crosswalk

This crosswalk follows all 30 changes listed in Appendix E. It records where
shared behavior is tested and where rev2 work remains; it does not promote a
rev1 extension test to proof of a complete rev2 implementation.

1. Large sizes: Appendix D permits servers to retain 32-bit size limits;
   cross-architecture number64 parsing and formatting still need audit.
2. Folded-in extensions: the existing NAMESPACE, UNSELECT, UIDPLUS, ESEARCH,
   SEARCHRES, ENABLE, IDLE, SASL-IR, LIST-EXTENDED, LIST-STATUS, MOVE, LITERAL-,
   BINARY and SPECIAL-USE rows prove their stated slices. Rev2 baseline
   negotiation, ABNF and response-code coverage remain IMAP2-SEC-217/225.
3. STATUS SIZE has IMAP-STATUS-SIZE rows; STATUS DELETED is IMAP2-DELTA-03,
   tested by TestStatusDeleted.
4. Default ESEARCH remains unsupported (IMAP2-CMD-135-02).
5. SEARCH matching has IMAP-SEARCH rows and IMAP2-CMD-143; the full rev2
   key-by-key wording still needs non-keyword reconciliation.
6. RFC2231 continued parameters have IMAP2-RESP-210/211 and the new
   TestBodyStructureContinuedParameters; extended-name rev2 formatting remains
   IMAP2-RESP-210-02.
7. COPYUID for MOVE/UID MOVE has the existing IMAP-MOVE rows.
8. Missing COPY/MOVE destinations have IMAP2-CMD-157/163; arbitrary backend
   creation/preservation guarantees remain the explicit COPY/MOVE gaps.
9. CLOSED on replacement selection is built for rev2 sessions and waits on
   ENABLE IMAP4rev2 (IMAP2-CMD-74); rev1 sessions get none (IMAP-CLOSED-01).
10. Selected-mailbox LIST and normalized OLDNAME remain IMAP2-CMD-73.
11. Rev2 deprecation of SELECT/EXAMINE UNSEEN requires mode-specific replies;
    the current rev1 engine still emits it (IMAP2-SEC-217/225).
12. Rev2 deprecation of RECENT, Recent, STATUS RECENT and SEARCH NEW also
    requires mode-specific grammar and state (IMAP2-SEC-226).
13. No additional PERMANENTFLAGS after keyword creation is required when the
    wildcard was already advertised; this engine offers no keyword creation
    (IMAP-STORE-06, IMAP2-RESP-175).
14. Bare number64 tagged extension data remains part of IMAP2-SEC-217's ABNF
    audit, without treating optional large server sizes as mandatory.
15. Recommended predefined keywords remain unsupported (IMAP2-CORE-15).
    The normative section names $NotJunk; Appendix E spells it $NonJunk.
16. UTF-8 mailbox/header defaults and dual-mode negotiation remain
    IMAP2-CORE-29/31, IMAP2-CMD-125 and IMAP2-COMPAT-249.
17. Rev2 removal of CHECK requires mode-specific command grammar; rev1 CHECK
    remains offered (IMAP2-SEC-217).
18. Deprecated RFC822 FETCH aliases require the same mode-specific grammar;
    rev1 compatibility tests do not establish rev2 refusal/acceptance policy.
19. Deprecated LSUB is still a rev1 command; LIST SUBSCRIBED has shared
    LIST-EXTENDED proofs, not rev2 negotiation (IMAP2-SEC-217/225).
20. IDLE updates for other mailbox state are allowed, not required; the
    selected-mailbox IDLE proofs do not claim that optional behavior.
21. UID in every unsolicited FETCH remains IMAP2-CMD-133/IMAP2-RESP-215.
22. Ignoring unknown response codes is a client duty, outside this engine.
23. Empty resp-text is allowed in rev2; the current rev1 engine emits text.
    Exact mode-dependent grammar still needs IMAP2-SEC-217 reconciliation.
24. UTF-8 response text after ENABLE IMAP4rev2 remains unimplemented
    (IMAP2-COMPAT-249); optional UTF8=ACCEPT is a separate extension.
25. Modern TLS policy is distinguished from Session negotiation by
    IMAP2-SEC-233/234/236/238/244.
26. ALERT presentation is client-side; PREAUTH is not offered
    (IMAP2-RESP-178). The security inventory makes no user-interface claim.
27. Additional non-plaintext SASL, including SCRAM-SHA-256, remains
    IMAP2-CMD-57-02, with no new backend authentication design chosen here.
28. Every command resets the autologout timer (IMAP2-CORE-38); its root
    timer proofs remain outside this Session harness.
29. Registration policy and removal of the X convention are specification
    duties (IMAP2-RESP-184, IMAP2-SEC-224/232); no private capability is added.
30. Connection closure duties are distinguished in IMAP2-CORE-24 and the
    RFC 9051 response inventory, without claiming new shutdown timer proofs.

| ID | Section | Level | Requirement | Tests |
|----|---------|-------|-------------|-------|
| IMAP2-DELTA-03 | RFC 9051 §6.3.11 | Normative | Return the number of messages marked Deleted for STATUS DELETED, alongside the other requested status items. | TestStatusDeleted |
| IMAP-CLOSED-01 | RFC 3501 §7.1; RFC 7162 §3.2.11 | SHOULD | Send an IMAP4rev1 session no CLOSED response code: it is neither in RFC 3501 nor X-prefixed, and RFC 7162 requires it only of a server advertising CONDSTORE or QRESYNC, which this engine does not. Rev2 sessions get it (IMAP2-CMD-74). | TestSelectNoClosedForRev1 |

## RFC 9051 first-pass audit record

- 2026-10-26: reviewed the explicit RFC requirement-keyword paragraphs in
  §§2–7, §9, §11 and Appendices A/A.1, with the compatibility guidance in
  Appendices B–F and the Appendix E crosswalk above. Definitions, references,
  index entries and client-only obligations are separated from server duties.
  The source is [RFC 9051](https://www.rfc-editor.org/rfc/rfc9051.txt), SHA-256
  `acf52a50eb748ea21b5ffdcb31c761d3fbba6242d53d4f53c2555de039c5c3cf`.
- Added 214 rows, including 113 mappings to shared/new Session wire tests and
  101 explicit non-applicability, backend-boundary or unsupported-work reasons.
  Nine new wire tests exercise tag reuse, exact body octets, metadata-only
  LIST, repeated ESEARCH options, pending-literal EXPUNGE suppression,
  continued MIME parameters, private authentication failures, shared failure
  budgets and case-sensitive modified-base64 mailbox identities. Existing
  requirement rows are preserved. The subsequent #150 correction below changes
  typed-text refusal without weakening the MIME SEARCH assertions.
- Remaining before any full-inventory claim: reconcile every ABNF production
  and non-keyword normative rule, finish every offered extension's inventory,
  and distinguish root-package/host proofs from missing engine behavior.
  A keyword scan and the Appendix E change list are discovery aids, not a
  substitute for that complete review. This checklist stays PARTIAL and
  rest-mail/rest-mail-server#374 stays open.
- This work does not measure statement coverage or execute race/fuzz suites;
  COVERAGE.md records its historical receipt and the separate CI coverage gate.

- 2026-11-03: added the RFC 9051 complete section index: all 133 numbered
  sections and 7 appendix entries (A, A.1, B–F), each citing requirement
  rows or giving an explicit exclusion reason, guarded by
  `TestRFC9051Inventory`. Sections without keyword rows were re-read; their
  only keyword sentences are optional (`MAY`) or client duties, and their
  normative prose restates RFC 3501 behavior already in the RFC 3501
  inventory, which they cite. No row was added or reclassified, so the counts
  are unchanged. Five uncovered gaps were filed (#1668–#1672) and linked from
  their rows. This is a structural index, not a semantic proof that every
  sentence is met; the checklist stays PARTIAL.

## RFC 3501 audit record

- 2026-10-22: RFC 3501 §§6.3.3 CREATE, 6.3.4 DELETE, 6.3.5 RENAME, 6.3.6
  SUBSCRIBE, 6.3.7 UNSUBSCRIBE, 6.3.8 LIST and 6.3.9 LSUB, with the §7.2.2/§7.2.3
  LIST and LSUB response forms, read line by line. Every MUST, MUST NOT, SHOULD
  and normative error rule found there now has a row above. Two bugs fixed: LSUB
  answered with `* LIST` replies, and RENAME left inferior names behind.
- 2026-10-22: RFC 3501 §6.1 (CAPABILITY, NOOP, LOGOUT), §6.2 (its state rule,
  STARTTLS, AUTHENTICATE and LOGIN), §§6.3.1 SELECT, 6.3.2 EXAMINE, 6.3.10
  STATUS, 6.3.11 APPEND, and §§6.4.1 CHECK, 6.4.2 CLOSE, 6.4.3 EXPUNGE, 6.4.7
  COPY and 6.4.8 UID, read line by line. Every MUST, MUST NOT, SHOULD and
  SHOULD NOT found there maps to a row: new rows IMAP-CAP-02, IMAP-LOGIN-02,
  IMAP-AUTH-STATE-01, IMAP-ANON-01, IMAP-AUTHENTICATE-01..12, IMAP-CLIENT-01,
  IMAP-SELECT-13, IMAP-APPEND-31, IMAP-LIST-05 and IMAP-CHECK-01/02; the rest
  were already covered by the IMAP-STARTTLS, SELECT, EXAMINE, STATUS, APPEND,
  CLOSE, EXPUNGE, COPY, STORE-04, FETCH-09 and SEARCH-04 rows. Client-only duties
  are collected in IMAP-CLIENT-01. Bugs fixed: LIST/LSUB ignored the reference
  (IMAP-LIST-04 is now mapped), AUTHENTICATE ignored a foreign authorization
  identity, LOGIN/AUTHENTICATE re-authenticated an authenticated session,
  AUTHENTICATE with no mechanism got NO instead of BAD, and CHECK accepted
  arguments.
- 2026-10-22: RFC 3501 §6.5 and §7 apart from §§7.2.2–7.2.3 read line by line:
  §7.1 status responses and every response code, §7.1.1–7.1.5 OK/NO/BAD/
  PREAUTH/BYE, §7.2.1 CAPABILITY, §§7.2.4–7.2.6 STATUS/SEARCH/FLAGS, §§7.3.1–7.3.2
  EXISTS/RECENT, §§7.4.1–7.4.2 EXPUNGE/FETCH and §7.5 continuation. New rows are
  IMAP-RESP-01..17, IMAP-RESP-CLIENT-01 and IMAP-XCMD-01..02. Rules already proven
  elsewhere are not repeated: CAPABILITY listing IMAP4rev1 (IMAP-CAP-01), STATUS
  (IMAP-STATUS-01), SEARCH (IMAP-SEARCH-01, IMAP-SEARCH-04), FLAGS/EXISTS on
  selection (IMAP-SELECT-01), EXPUNGE renumbering (IMAP-EXPUNGE-02), FETCH data
  items (IMAP-FETCH-*, IMAP-ENV-*, IMAP-BS-*, IMAP-MSG-*). One bug fixed: a line
  with no tag and command was dropped without a reply, so the client waited for
  a completion that never came; it now gets an untagged BAD.
- 2026-10-24: RFC 3501 §6.4.6 STORE read line by line, with the §2.3.2 rule
  that \Recent is not the client's to change. Every rule maps to a row: the
  untagged FETCH with the resulting flags (IMAP-STORE-01), FLAGS/+FLAGS/-FLAGS
  (IMAP-STORE-02), .SILENT (IMAP-STORE-03), NO for a store error (IMAP-STORE-05),
  BAD for invalid arguments (IMAP-STORE-07, 09), the section's example
  (TestStoreExample) and two new rows: FLAGS leaves \Recent alone
  (IMAP-STORE-11), and another source's change is reported whatever .SILENT
  says (IMAP-STORE-12). No bug found. A session's IDLE poll interval can now
  be set (Session/Server.SetIdlePollInterval), which IMAP-STORE-12's wire test
  uses.
- 2026-10-24: RFC 3501 §6.4.4 SEARCH read line by line. Every defined key and
  rule maps to a row: the untagged SEARCH response (IMAP-SEARCH-01), AND of
  keys and parenthesized lists, OR and NOT (IMAP-SEARCH-02), CHARSET with
  BADCHARSET and decoding before comparison (IMAP-SEARCH-07, 10), sequence
  sets and UID (IMAP-SEARCH-05, 13, 14), ALL, SEEN/UNSEEN, FLAGGED and SUBJECT
  (IMAP-SEARCH-03), ANSWERED/DRAFT and their negations (IMAP-SEARCH-15, 16),
  BCC/CC/HEADER (IMAP-SEARCH-19, 21), the internal and sent dates
  (IMAP-SEARCH-06, 20, 22, 23), BODY/TEXT (IMAP-SEARCH-10), KEYWORD/UNKEYWORD
  (IMAP-SEARCH-11), LARGER/SMALLER (IMAP-SEARCH-17, 18) and the A282/A283
  examples (TestSearchExample). Three bugs fixed: FROM compared only the
  address, not the envelope's display name (IMAP-SEARCH-26); NEW and OLD were
  BAD as unknown keys (IMAP-SEARCH-27); TO compared encoded words undecoded
  (IMAP-SEARCH-28, which also gives DELETED, UNDELETED and UNFLAGGED their
  first tests). RECENT stays a recorded gap (IMAP-SEARCH-12). The MAY rule
  excluding non-TEXT/MESSAGE parts is not taken: every leaf is searched.
- 2026-10-24: RFC 3501 §6.4.5 FETCH read line by line. Every data item and
  rule maps to a row: macros (IMAP-FETCH-08, 21), BODY[section] and partials
  (IMAP-FETCH-01, 04, 06, 14, 18, 19), HEADER.FIELDS[.NOT] (IMAP-FETCH-05, 22,
  23), the implicit \Seen (IMAP-FETCH-02, 11, 12), BODY.PEEK, the RFC822 forms
  (IMAP-FETCH-03), INTERNALDATE and RFC822.SIZE (IMAP-FETCH-07), ENVELOPE and
  BODYSTRUCTURE (IMAP-ENV-*, IMAP-BS-*), NO for a fetch error (IMAP-FETCH-10)
  and BAD for invalid arguments (IMAP-FETCH-15, 20). One bug fixed: the blank
  line rule for a message with no body and no blank line, and the header of a
  message that starts with its blank line (IMAP-FETCH-24). Static items not
  changing is the host's to keep: the engine derives them from the stored
  message and metadata, which it never rewrites.
- 2026-10-24: RFC 3501 §5.1 mailbox naming (§§5.1.1–5.1.3) read line by line,
  with RFC 6855 §3's rules for names. One deviation fixed (rest-mail/go-imap#145):
  the server did no modified UTF-7 at all, so a UTF-8 store's names went out as
  8-bit octets to clients that had not enabled UTF-8, and a client's modified
  UTF-7 name reached the store undecoded. New rows IMAP-MUTF7-01..05. INBOX is
  IMAP-CREATE-01, IMAP-DELETE-01, IMAP-LIST-03 and IMAP-STATUS-02; client-only
  duties (avoid 8-bit names, atom-specials, wildcards) are not server rules.
- 2026-10-24: RFC 3501 §§5.2–5.5 read line by line. One deviation fixed: NOOP,
  "the preferred method" to poll (§6.1.2), and CHECK, "equivalent to NOOP"
  without housekeeping (§6.4.1), never re-read the mailbox, so a client that
  polls rather than idles saw no new mail, expunge or flag change until it
  selected again (§5.2 MUST/SHOULD). New rows IMAP-UPDATE-01..04 and
  IMAP-AUTOLOGOUT-01; no EXPUNGE during FETCH, STORE or SEARCH was already
  IMAP-RESP-12. Client duties (record size updates, do not pipeline ambiguous
  commands) are not server rules.
- 2026-10-24: RFC 3501 §§2–3 read line by line. One deviation fixed here: a
  stopping Server closed every connection without the BYE §3.4 requires
  (IMAP-STATE-01); Shutdown now has each session say BYE, at once when it is
  waiting or idling. Two found in the host's part, and filed for REST Mail:
  UIDNEXT falls when the newest message is expunged, which needs the store's
  UID history (rest-mail/rest-mail-server#804, IMAP-SELECT-09 and
  IMAP-STATUS-08 stay n/a), and REST Mail listed messages by date rather than
  UID (rest-mail/rest-mail-server#805). §2.3.1.2's ascending order, §2.3.2's
  flags and \Recent, and §3's state rules are the rows already above.
- 2026-10-24: RFC 3501 §4 read line by line. Literals and their continuation
  are the IMAP-LITERAL and grammar rows; binary strings (§4.3.1) get NO rather than
  raw NUL octets (IMAP-ENV-12, IMAP-BS-10); NIL as an nstring is the ENVELOPE and
  BODYSTRUCTURE rows. One new row, a mailbox named NIL (IMAP-NIL-01); no bug.
- Every section of RFC 3501 from §2 to §7 has now been read line by line.
- 2026-10-24: RFC 3501 §9, the rules every line shares: its three notes (case,
  SP, NUL), tag, resp-text, and the commands that take no arguments, with
  LOGIN's arguments. Five deviations fixed (IMAP-SYNTAX-01..05 above): SELECT,
  EXAMINE and MOVE sent response codes with no text after them, which the
  grammar requires; tags with `*`, `%`, `+`, DQUOTE or backslash were accepted;
  CAPABILITY, NOOP, LOGOUT and CLOSE accepted a trailing space or arguments;
  LOGIN took any number of arguments, invalid escapes and atom-specials.
  Probed and already right: doubled SP and TAB, NUL, case, sequence-set and
  number limits, partial ranges, section parts, dates, empty lists, flag
  atoms, STATUS items and FETCH macros. The discovered typed 8-bit text
  deviation was tracked in rest-mail/go-imap#150 and corrected below following
  the owner's 2026-10-26 instruction. Next: RFC 9051 (IMAP4rev2).
- 2026-10-26: corrected #150 (IMAP-SYNTAX-06..07). Backends without
  UTF8=ACCEPT now refuse typed 8-bit command text, including pre-authentication
  LOGIN and both APPEND literal boundaries. A refused unread non-synchronizing
  literal closes with BYE; synchronizing refusals preserve the next command.
  Literal contents and supported UTF8=ACCEPT behavior remain available. MIME
  and charset SEARCH proofs now send their 8-bit operands as legal literals,
  retaining their original decoding and match assertions. Three new wire
  proofs include UTF-8 credentials through AUTHENTICATE PLAIN. The checklist
  mutation fixture now removes exact IDs across every annotation line in one
  function's parsed doc group, with a regression for prefix collisions and
  neighboring functions; a supposed missing-annotation fixture cannot silently
  retain the annotation.
