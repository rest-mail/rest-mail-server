# Session extension audit

Reviewed 2026-10-26 against the seven canonical RFC Editor text files below.
This groups related security negotiation, session state and literal framing in
one review. Every numbered source section and both appendices have a
disposition, including rules without RFC2119 keywords. References name narrow CHECKLIST.md requirements;
several rows can share one section. A reference is not proof that every clause
or grammar alternative in that section is implemented.

`internal/checklist/testdata/session-extensions.json` pins the canonical file
and section hashes and the reviewed section index. CI checks section omissions,
duplicates, source pins, crosswalk drift and unknown requirement references.
The fixture and this crosswalk contain paraphrases and hashes, not RFC text.
The guard is a structural review guard, not an independent conformance oracle.

## Findings and boundaries

- PLAIN previously let a permissive backend authenticate empty identity/password,
  additional NULs in the password, or malformed UTF-8. The new wire regressions
  fail on the baseline. These are now refused before the backend, in both the
  initial-response and continuation forms; a valid retry still succeeds.
- Go's general base64 decoder ignores embedded CR/LF. A CR embedded in either
  PLAIN response form previously passed authentication. SASL now rejects those
  non-alphabet octets. Required 255-octet fields still work without truncation.
- Credential preparation, stored hashing and canonical identity derivation stay
  with Backend.Authenticate. No normalization or delegated-identity design is
  chosen here. A host must fail a preparation failure or empty prepared value.
- TLS-configured sessions gate every cleartext credential form. A nil TLS config
  still permits development/legacy cleartext authentication. Old DHE-DSS/3DES
  advice is not re-enabled; modern versions, ciphers and certificates are host
  policy. Client hostname/cache/downgrade duties and POP3/ACAP are excluded.
- ENABLE pipelining, the exact RFC7888 counted-space LOGIN example, server FETCH
  literal framing and a live IDLE new-mail/expunge/flag transcript have dedicated
  proofs. Existing command/state proofs remain intact. IDLE's current NO policy
  requires a selected mailbox; no account-wide notification support is implied.
- Binary APPEND storage, MULTIAPPEND, EXTERNAL and CONDSTORE are not added. Their
  absence must not be hidden by mapping the shared negotiation machinery.

## Source crosswalk

### RFC2595

Source: [https://www.rfc-editor.org/rfc/rfc2595.txt](https://www.rfc-editor.org/rfc/rfc2595.txt); SHA256 `5d644cd1053214b5f2a75584efedb16acac07472b099496562fbfe574c2d775b`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC2595 §1 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC2595 §1.1 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC2595 §2 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC2595 §2.1 | `IMAP-TLS-AUDIT-01` | Historical cipher requirement is explicitly unsupported; modern host TLS policy is separate. |
| RFC2595 §2.2 | `IMAP-TLS-AUDIT-02`, `IMAP-STARTTLS-08` | Protected and legacy operational modes; no per-user policy hook. |
| RFC2595 §2.3 | `IMAP-STARTTLS-01`, `IMAP-STARTTLS-08`, `IMAP-TLS-AUDIT-02` | All cleartext credential forms are gated when TLS is configured; per-user advice remains a boundary. |
| RFC2595 §2.4 | `IMAP-TLS-AUDIT-03` | All hostname, SAN, wildcard and confirmation checks belong to the connecting client. |
| RFC2595 §2.5 | `IMAP-STARTTLS-07`, `IMAP-TLS-AUDIT-04` | Handshake failure terminates the connection; the host chooses acceptable privacy/authentication policy. |
| RFC2595 §3 | `IMAP-STARTTLS-01` | STARTTLS capability registration and IMAP command context. |
| RFC2595 §3.1 | `IMAP-STARTTLS-02`, `IMAP-STARTTLS-03`, `IMAP-STARTTLS-04`, `IMAP-STARTTLS-05`, `IMAP-STARTTLS-06`, `IMAP-STARTTLS-09`, `IMAP-TLS-AUDIT-03` | Negotiation boundary, state, optional EXTERNAL and client cache/pipeline duties; example covered by TestStartTLSExample. |
| RFC2595 §3.2 | `IMAP-STARTTLS-01`, `IMAP-STARTTLS-08`, `IMAP-TLS-AUDIT-03` | LOGINDISABLED advertisement/refusal and the client prohibition. |
| RFC2595 §4 | `IMAP-TLS-AUDIT-05` | POP3-only STLS and capability rules, not IMAP. |
| RFC2595 §5 | `IMAP-TLS-AUDIT-05` | ACAP-only extension, not IMAP. |
| RFC2595 §5.1 | `IMAP-TLS-AUDIT-05` | ACAP greeting and command rules, not IMAP. |
| RFC2595 §6 | `IMAP-PLAIN-01`, `IMAP-PLAIN-02`, `IMAP-PLAIN-03`, `IMAP-PLAIN-04`, `IMAP-PLAIN-05`, `IMAP-PLAIN-06` | Superseded in full by RFC4616; do not implement obsolete 5/6-octet UTF-8 grammar. |
| RFC2595 §7 | `IMAP-TLS-AUDIT-06` | Historical port guidance; modern deployment is outside Session. |
| RFC2595 §8 | `IMAP-STARTTLS-01`, `IMAP-TLS-AUDIT-05` | IMAP capability registration only; POP3/ACAP registrations are excluded. |
| RFC2595 §9 | `IMAP-TLS-AUDIT-03`, `IMAP-TLS-AUDIT-04`, `IMAP-PLAIN-06` | TLS policy, downgrade and cleartext advice; TLS is not end-to-end message security. |
| RFC2595 §10 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC2595 §11 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC2595 §A | `IMAP-TLS-AUDIT-01`, `IMAP-TLS-AUDIT-02`, `IMAP-TLS-AUDIT-03`, `IMAP-TLS-AUDIT-04`, `IMAP-TLS-AUDIT-05`, `IMAP-STARTTLS-01`, `IMAP-STARTTLS-08` | Summary of the same privacy, cipher, client and other-protocol requirements; no additional implementation claim. |

### RFC4616

Source: [https://www.rfc-editor.org/rfc/rfc4616.txt](https://www.rfc-editor.org/rfc/rfc4616.txt); SHA256 `33aa677ab7b8efe52e86147c63b38dd8dd5de96438c63e10ba8a04504bbdbca4`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC4616 §1 | `IMAP-PLAIN-06`, `IMAP-AUTHENTICATE-11` | PLAIN has no security layer; protected default and legacy exception are explicit. |
| RFC4616 §2 | `IMAP-PLAIN-01`, `IMAP-PLAIN-02`, `IMAP-PLAIN-03`, `IMAP-PLAIN-04`, `IMAP-PLAIN-05` | UTF-8/NUL grammar, 255-octet minima, authentication/authorization and preparation responsibilities. Optional credential-database initialization is not offered. |
| RFC4616 §3 | `IMAP-PLAIN-04`, `IMAP-PLAIN-05` | Illustrative non-definitive pseudocode, not an additional engine algorithm requirement. |
| RFC4616 §4 | `IMAP-PLAIN-02`, `IMAP-AUTHENTICATE-06`, `IMAP-TLS-AUDIT-05` | Mechanism framing/unauthorized identity are proved through IMAP; ACAP transcripts are not claimed verbatim. |
| RFC4616 §5 | `IMAP-PLAIN-06`, `IMAP-TLS-AUDIT-03` | Protected-default recommendation and client password-revealing mechanism policy. |
| RFC4616 §6 | `IMAP-SASLIR-01` | PLAIN is advertised as AUTH=PLAIN; registration metadata needs no runtime behavior. |
| RFC4616 §7 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC4616 §8 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC4616 §9 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC4616 §A | `IMAP-PLAIN-01`, `IMAP-PLAIN-04` | Explicitly non-normative changes: modern UTF-8 grammar replaces the old 5/6-octet form; preparation remains a backend duty. |

### RFC4959

Source: [https://www.rfc-editor.org/rfc/rfc4959.txt](https://www.rfc-editor.org/rfc/rfc4959.txt); SHA256 `22a39f041118aec0cb7b5f879ccba9cc146081c5c138768b79bbd4e798ac44b8`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC4959 §1 | `IMAP-SASLIR-01` | SASL-IR discovery and optional initial response. |
| RFC4959 §2 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC4959 §3 | `IMAP-SASLIR-02`, `IMAP-SASLIR-03`, `IMAP-SASLIR-04`, `IMAP-SASLIR-06`, `IMAP-SASLIR-07`, `IMAP-SASLIR-08` | Base64, zero-length marker, unquoted token, omitted-response fallback and mechanism applicability. |
| RFC4959 §4 | `IMAP-SASLIR-02`, `IMAP-SASLIR-03`, `IMAP-SASLIR-08` | PLAIN initial/continuation examples are tested; optional EXTERNAL is not supported. |
| RFC4959 §5 | `IMAP-SASLIR-01` | Capability registration, no extra wire command. |
| RFC4959 §6 | `IMAP-SASLIR-03`, `IMAP-SASLIR-05`, `IMAP-LITERAL-10` | Omission fallback and line budget for the required PLAIN size; overlong replies preserve framing. |
| RFC4959 §7 | `IMAP-SASLIR-01`, `IMAP-SASLIR-04`, `IMAP-SASLIR-06` | AUTHENTICATE grammar, unquoted base64/equals and continuation forms. |
| RFC4959 §8 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC4959 §9 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC4959 §9.1 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC4959 §9.2 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |

### RFC5161

Source: [https://www.rfc-editor.org/rfc/rfc5161.txt](https://www.rfc-editor.org/rfc/rfc5161.txt); SHA256 `a8a34b3442e20ac21336e32a418828c21e24c253ca5d1706d9a0019e8f1301e3`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC5161 §1 | `IMAP-EXT-CAP-01` | ENABLE advertisement; descriptive overview introduces no extra enable-able extension. |
| RFC5161 §2 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC5161 §3 | `IMAP-ENABLE-01` | Protocol-change overview. |
| RFC5161 §3.1 | `IMAP-ENABLE-01`, `IMAP-ENABLE-02`, `IMAP-ENABLE-03`, `IMAP-ENABLE-04`, `IMAP-ENABLE-05`, `IMAP-ENABLE-06`, `IMAP-ENABLE-07`, `IMAP-ENABLE-08` | Ignore unknown/non-enable-able names; persistent additive mode, response/capability invariants, state, pipelining and client duties. CONDSTORE example is unsupported and ignored. |
| RFC5161 §3.2 | `IMAP-ENABLE-02` | ENABLED lists only enabled names and can be empty. |
| RFC5161 §3.3 | `IMAP-ENABLE-08` | Extension-designer advice, not engine behavior. |
| RFC5161 §4 | `IMAP-ENABLE-04`, `IMAP-ENABLE-05` | Case-insensitive capability atoms, required arguments and response syntax. |
| RFC5161 §5 | `IMAP-ENABLE-06` | No added security requirement beyond authenticated state. |
| RFC5161 §6 | `IMAP-EXT-CAP-01` | ENABLE capability registration. |
| RFC5161 §7 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC5161 §8 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC5161 §9 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |

### RFC3691

Source: [https://www.rfc-editor.org/rfc/rfc3691.txt](https://www.rfc-editor.org/rfc/rfc3691.txt); SHA256 `2d3527e0d7cb70a3877f77685d487eed5044671197fd5f437be4f7a540dd958b`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC3691 §1 | `IMAP-EXT-CAP-01` | UNSELECT advertisement and distinction from CLOSE. |
| RFC3691 §2 | `IMAP-UNSELECT-01`, `IMAP-UNSELECT-02`, `IMAP-UNSELECT-03`, `IMAP-UNSELECT-04` | No expunge, selection release, BAD without selection/with arguments; RFC example is covered by the public exchange. |
| RFC3691 §3 | `IMAP-UNSELECT-02` | No added security requirement beyond the base protocol. |
| RFC3691 §4 | `IMAP-UNSELECT-01`, `IMAP-UNSELECT-04` | Case-insensitive selected-state no-argument command. |
| RFC3691 §5 | `IMAP-EXT-CAP-01` | UNSELECT capability registration. |
| RFC3691 §6 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC3691 §7 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC3691 §8 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC3691 §9 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |

### RFC2177

Source: [https://www.rfc-editor.org/rfc/rfc2177.txt](https://www.rfc-editor.org/rfc/rfc2177.txt); SHA256 `600c02aeddb4dc47618700797b2293115d077404ce1781990fdad1a88597d8d2`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC2177 §1 | `IMAP-EXT-CAP-01` | IDLE capability and motivation. |
| RFC2177 §2 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC2177 §3 | `IMAP-IDLE-01`, `IMAP-IDLE-02`, `IMAP-IDLE-03`, `IMAP-IDLE-04`, `IMAP-IDLE-05`, `IMAP-LITERAL-11`, `IMAP-AUTOLOGOUT-01` | Continuation/DONE, update transcript, completion ordering, NO policy, client obligations and timeout boundary. |
| RFC2177 §4 | `IMAP-IDLE-01`, `IMAP-IDLE-02`, `IMAP-IDLE-04` | Authenticated/selected-state command grammar and DONE continuation. |
| RFC2177 §5 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC2177 §6 | `IMAP-IDLE-05` | No new security requirements; base-protocol timeout policy remains explicit. |
| RFC2177 §7 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |

### RFC7888

Source: [https://www.rfc-editor.org/rfc/rfc7888.txt](https://www.rfc-editor.org/rfc/rfc7888.txt); SHA256 `62e676eeece48d072ab1e2049613357ef47e7a9f3fb52163f27eece0d7d95b15`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC7888 §1 | `IMAP-LITMINUS-01`, `IMAP-LITMINUS-03` | LITERAL- is the offered subset; LITERAL+ is not advertised. |
| RFC7888 §2 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC7888 §3 | `IMAP-LITERAL-01`, `IMAP-LITERAL-02`, `IMAP-LITERAL-03`, `IMAP-LITERAL-04`, `IMAP-LITERAL-05`, `IMAP-LITERAL-06`, `IMAP-LITERAL-07`, `IMAP-LITERAL-08`, `IMAP-LITERAL-09`, `IMAP-LITMINUS-02`, `IMAP-LITMINUS-05`, `IMAP-LITMINUS-06` | Count each literal and continuation segment, preserve spaces, omit client-data continuation, never emit plus literals to clients; exact LOGIN example has a dedicated proof. |
| RFC7888 §4 | `IMAP-LITERAL-02`, `IMAP-LITERAL-03`, `IMAP-LITMINUS-04` | Reject by closing with BYE when unread payload is in flight; APPENDLIMIT is optional and is not advertised. |
| RFC7888 §5 | `IMAP-LITMINUS-01`, `IMAP-LITMINUS-03`, `IMAP-LITMINUS-04` | 4096 boundary, synchronizing fallback, TOOBIG and mutual exclusion of LITERAL-/LITERAL+. |
| RFC7888 §6 | `IMAP-LITMINUS-07`, `IMAP-BINARY-07`, `IMAP-UTF8-04` | literal8 grammar is recognized, but binary APPEND storage is explicitly unsupported. |
| RFC7888 §7 | `IMAP-LITMINUS-08` | MULTIAPPEND is not supported. |
| RFC7888 §8 | `IMAP-LITMINUS-02`, `IMAP-LITMINUS-03`, `IMAP-LITMINUS-07` | Literal octet counts and optional plus; literal8 storage boundary remains explicit. |
| RFC7888 §9 | `IMAP-LITERAL-01`, `IMAP-LITERAL-02`, `IMAP-LITMINUS-04` | Bound command/literal allocations and close unread payload paths; no blanket denial-of-service guarantee. |
| RFC7888 §10 | `IMAP-LITMINUS-01` | Capability registration. |
| RFC7888 §11 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC7888 §11.1 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |
| RFC7888 §11.2 | — | Document context, conventions, acknowledgements, references or legal metadata; no independent server protocol requirement. |

## Still outstanding in server issue374

The checklist remains PARTIAL. This is a section-level audit of seven related
extensions, not a complete clause-by-clause inventory of core RFC3501/9051 or
the other advertised extensions. The independent core-grammar/QUOTA handoff is
separate. Remaining specifications, cross-extension alternatives, backend gaps,
new measured coverage and full conformance acceptance are not declared done.
