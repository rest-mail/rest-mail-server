# Mailbox discovery and status extension audit

Reviewed 2026-10-26 against canonical RFC Editor text for RFC 2342, 8438, 5819,
6154 and 5258. Every numbered source section has a disposition, including rules
without RFC2119 keywords and explicit client/host/unsupported boundaries.
The source skips section 10 in RFC2342; the index preserves that source numbering.
References are narrow CHECKLIST.md requirements, not proof of every clause or
ABNF alternative. Source hashes and the independent section pins guard review
structure; they are not a full protocol conformance oracle.

## Findings

- RFC8438 requires an unsigned 63-bit SIZE. Negative host sizes and overflow
  previously emitted negative values. Invalid sums now refuse direct STATUS
  without an untagged response; LIST may omit that STATUS while still succeeding.
  Metadata requests that do not ask for SIZE stay usable. Wide totals and the
  inclusive 63-bit maximum have focused proofs.
- RFC5258 requires repeated options to act as one. Repeated STATUS return options
  previously received BAD. The first parameter list now wins, with every later
  occurrence still syntax-checked and only one lookup performed per mailbox.
  The old refusal fixture is retained with its corrected expected result.
- RFC5819 requires no STATUS for a parent returned only through RECURSIVEMATCH,
  and a vanished/unselectable mailbox must gain Noselect before its LIST reply.
  Metadata is now resolved before LIST, only for folders meeting selection.
  Typed ErrNoSuchMailbox (including wrapped errors) gains Noselect; unexpected
  errors omit STATUS. Untyped permission denial remains an explicit host gap.
- All seven standardized special-use attributes are proved, including repeated
  roles on different folders and filtering unknown attributes. The backend still
  represents at most one role per folder; assignment, METADATA, remote folders,
  persistent subscriptions and other/shared namespace policy are not added.

## Source crosswalk

### RFC2342

Source: [RFC2342](https://www.rfc-editor.org/rfc/rfc2342.txt); SHA256 `6023278a88e94a0b1a530b1e7a14a37e89699b07d7ba65e3bfe45dd276d56aab`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC2342 §1 | `IMAP-NAMESPACE-01` | Namespace discovery scope; the offered model is one personal namespace. |
| RFC2342 §2 | `IMAP-NAMESPACE-01`, `IMAP-NAMESPACE-03` | INBOX is personal; other-users grants and optional multiple/per-user namespaces remain boundaries. |
| RFC2342 §3 | `IMAP-NAMESPACE-05` | Discovery overview and manual client-prefix fallback. |
| RFC2342 §4 | `IMAP-NAMESPACE-01` | Capability and authenticated/selected command availability. |
| RFC2342 §5 | `IMAP-NAMESPACE-01`, `IMAP-NAMESPACE-02`, `IMAP-NAMESPACE-03`, `IMAP-NAMESPACE-04`, `IMAP-NAMESPACE-05` | Example 5.1 is the fixed personal exchange; other/shared/anonymous/multiple-prefix examples are outside the offered backend. Other-user visibility and client target selection remain explicit. |
| RFC2342 §6 | `IMAP-NAMESPACE-01`, `IMAP-NAMESPACE-02`, `IMAP-NAMESPACE-04` | Three ordered namespace classes, quoted prefix/delimiter, NIL absent classes and no arguments; international prefixes/extensions are not offered. |
| RFC2342 §7 | `IMAP-NAMESPACE-03` | Other-user visibility is a host ACL boundary; no other-users namespace is advertised. |
| RFC2342 §8 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC2342 §9 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC2342 §11 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC2342 §12 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

### RFC8438

Source: [RFC8438](https://www.rfc-editor.org/rfc/rfc8438.txt); SHA256 `36432e42934250b81f19e2c7f529decc66508dcbfad19a3225654c4a375673f1`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC8438 §1 | `IMAP-STATUSSIZE-01` | STATUS=SIZE discovery and mailbox-size intent. |
| RFC8438 §2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC8438 §3 | `IMAP-STATUSSIZE-01`, `IMAP-STATUSSIZE-02`, `IMAP-STATUSSIZE-03`, `IMAP-STATUSSIZE-04` | Octet sum, quota consistency boundary, wide values and direct/LIST-STATUS examples. Existing TestStatusSize follows the eight-message 44421-octet example. |
| RFC8438 §4 | `IMAP-STATUSSIZE-03` | SIZE grammar and inclusive unsigned 63-bit range; invalid host sums cannot wrap. |
| RFC8438 §5 | `IMAP-LISTSTATUS-07` | No added security protocol beyond base/LIST-STATUS resource handling. |
| RFC8438 §6 | `IMAP-STATUSSIZE-01` | STATUS=SIZE capability registration. |
| RFC8438 §7 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

### RFC5819

Source: [RFC5819](https://www.rfc-editor.org/rfc/rfc5819.txt); SHA256 `7cecc3c6f63e70e872a2cc62888ecfdb5f701f11b339bb4c81e8ad40759de63a`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC5819 §1 | `IMAP-LISTSTATUS-01` | Combined listing/status intent. |
| RFC5819 §1.1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5819 §2 | `IMAP-LISTSTATUS-01`, `IMAP-LISTSTATUS-02`, `IMAP-LISTSTATUS-04`, `IMAP-LISTSTATUS-05`, `IMAP-LISTSTATUS-06` | Buffer LIST before metadata lookup; emit STATUS only for selected matching mailboxes. Typed disappearance gets Noselect, unexpected errors may omit STATUS; untyped ACL failures remain a boundary. |
| RFC5819 §3 | `IMAP-LISTSTATUS-01`, `IMAP-LISTSTATUS-05` | LIST/STATUS ordering and no STATUS for a recursive-only parent. Selective subscription examples are adapted to the explicit all-folders-subscribed backend. |
| RFC5819 §4 | `IMAP-LISTSTATUS-03`, `IMAP-LISTEXT-07` | Nonempty STATUS item-list grammar, bad syntax and repeated-option deduplication per RFC5258. |
| RFC5819 §5 | `IMAP-LISTSTATUS-07` | Cross-connection fairness and host scheduling boundary. |
| RFC5819 §6 | `IMAP-LISTSTATUS-01` | LIST-STATUS capability and STATUS return-option registration. |
| RFC5819 §7 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5819 §8 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

### RFC6154

Source: [RFC6154](https://www.rfc-editor.org/rfc/rfc6154.txt); SHA256 `c5908ee88071617d19fc847b947b149a58fb2d65366f6c800683c5b8bc3cfc0f`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC6154 §1 | `IMAP-SPECIALUSE-05`, `IMAP-SPECIALUSE-08` | Role discovery is offered; role assignment on CREATE is optional and not offered. |
| RFC6154 §1.1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6154 §2 | `IMAP-SPECIALUSE-01`, `IMAP-SPECIALUSE-02`, `IMAP-SPECIALUSE-03`, `IMAP-SPECIALUSE-04`, `IMAP-SPECIALUSE-05`, `IMAP-SPECIALUSE-06`, `IMAP-SPECIALUSE-07`, `IMAP-SPECIALUSE-10`, `IMAP-SPECIALUSE-11` | All seven roles, optional ordinary LIST attributes, implied return-on-selection, no invented roles, duplicate roles and the one-role-per-Folder representation boundary. |
| RFC6154 §3 | `IMAP-SPECIALUSE-08`, `IMAP-SPECIALUSE-12` | Optional CREATE USE/USEATTR, conflict rules and dual capability requirements are outside the offered implementation. |
| RFC6154 §4 | `IMAP-SPECIALUSE-09`, `IMAP-SPECIALUSE-12` | METADATA tie-in and role-assignment validation are conditional on an unsupported, deliberately excluded extension. |
| RFC6154 §5 | `IMAP-SPECIALUSE-10`, `IMAP-SPECIALUSE-14` | Example scope is split between offered LIST discovery and unsupported assignment operations. |
| RFC6154 §5.1 | `IMAP-SPECIALUSE-01`, `IMAP-SPECIALUSE-02`, `IMAP-SPECIALUSE-03`, `IMAP-SPECIALUSE-04` | Ordinary LIST role discovery; optional Marked/child flags are separate from role identity. |
| RFC6154 §5.2 | `IMAP-SPECIALUSE-07`, `IMAP-SPECIALUSE-10` | Extended return and selection examples, including nested role-bearing folders. |
| RFC6154 §5.3 | `IMAP-SPECIALUSE-14` | CREATE USE example is not implemented. |
| RFC6154 §5.4 | `IMAP-SPECIALUSE-14` | METADATA assignment example is not implemented. |
| RFC6154 §6 | `IMAP-SPECIALUSE-07`, `IMAP-SPECIALUSE-10`, `IMAP-SPECIALUSE-12`, `IMAP-SPECIALUSE-13` | Registered role grammar, selection/return options and unknown-attribute client duty. Assignment grammar remains unsupported. |
| RFC6154 §7 | `IMAP-LISTEXT-11`, `IMAP-SPECIALUSE-12` | Host visibility/TLS privacy and unsupported assignment side effects; no shared-folder role policy is invented. |
| RFC6154 §8 | `IMAP-SPECIALUSE-05` | Registration overview. |
| RFC6154 §8.1 | `IMAP-SPECIALUSE-12` | USEATTR is tied to unsupported assignment. |
| RFC6154 §8.2 | `IMAP-SPECIALUSE-05` | CREATE-SPECIAL-USE remains unadvertised. |
| RFC6154 §8.3 | `IMAP-SPECIALUSE-05` | SPECIAL-USE is advertised. |
| RFC6154 §8.4 | `IMAP-SPECIALUSE-07` | SPECIAL-USE selection registration and implied return. |
| RFC6154 §8.5 | `IMAP-SPECIALUSE-07` | SPECIAL-USE return registration. |
| RFC6154 §8.6 | `IMAP-SPECIALUSE-09` | METADATA entry registration does not imply implementation. |
| RFC6154 §9 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6154 §9.1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6154 §9.2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

### RFC5258

Source: [RFC5258](https://www.rfc-editor.org/rfc/rfc5258.txt); SHA256 `9ec84900ac85244a0ec0d530fc704349737570ffaaada7b20e78725714bccb02`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC5258 §1 | `IMAP-LISTEXT-01`, `IMAP-LISTEXT-05`, `IMAP-LISTEXT-08` | Extended syntax discovery, capability and client use of LSUB. |
| RFC5258 §2 | `IMAP-LISTEXT-05` | Canonical pattern definition; RFC2119 conventions add no independent engine rule. |
| RFC5258 §3 | `IMAP-LISTEXT-01`, `IMAP-LISTEXT-02`, `IMAP-LISTEXT-03`, `IMAP-LISTEXT-05`, `IMAP-LISTEXT-06`, `IMAP-LISTEXT-07`, `IMAP-LISTEXT-08` | Multiple patterns, empty-pattern special case, conjunction of selection, no extra names from return options, unknown/duplicate options and unsolicited extended-data boundaries. |
| RFC5258 §3.1 | `IMAP-LISTEXT-03`, `IMAP-LISTEXT-04`, `IMAP-LISTEXT-06`, `IMAP-LISTEXT-09`, `IMAP-LISTEXT-10` | SUBSCRIBED/REMOTE/RECURSIVEMATCH; only supported host models are claimed. |
| RFC5258 §3.2 | `IMAP-LISTEXT-01`, `IMAP-LISTEXT-03`, `IMAP-LISTEXT-09` | SUBSCRIBED and mandatory CHILDREN return options. |
| RFC5258 §3.3 | `IMAP-LISTEXT-02`, `IMAP-LISTEXT-04`, `IMAP-LISTEXT-05` | One reply per name, selection/recursive inclusion and additive attributes. |
| RFC5258 §3.4 | `IMAP-LISTEXT-08` | Client inference of stronger attributes. |
| RFC5258 §3.5 | `IMAP-LISTEXT-02`, `IMAP-LISTEXT-04`, `IMAP-LISTEXT-08`, `IMAP-LISTEXT-09` | CHILDINFO only on recursive selection, criteria strings, missing ancestors and redundant-parent suppression; selective subscriptions remain unsupported. |
| RFC5258 §4 | `IMAP-LISTEXT-01`, `IMAP-LISTEXT-11` | Exactly one child attribute and visibility filtering by the host. |
| RFC5258 §5 | `IMAP-LISTEXT-01`, `IMAP-LISTEXT-02`, `IMAP-LISTEXT-03`, `IMAP-LISTEXT-04`, `IMAP-LISTEXT-05`, `IMAP-LISTEXT-12` | All eleven examples reviewed: supported hierarchy slices have tests; selective/missing subscriptions, remote and ACL variants are not claimed verbatim. |
| RFC5258 §6 | `IMAP-LISTEXT-05`, `IMAP-LISTEXT-06`, `IMAP-LISTEXT-07`, `IMAP-LISTSTATUS-03` | LIST selection/pattern/return and CHILDINFO grammar. This crosswalk is not a claim to test every ABNF alternative or generic future extension value. |
| RFC5258 §7 | `IMAP-LISTEXT-08` | Future extension/comparator specification duty; no COMPARATOR extension is offered. |
| RFC5258 §8 | `IMAP-LISTEXT-11` | Child visibility and disclosure boundary. |
| RFC5258 §9 | `IMAP-LISTEXT-01` | Registry overview. |
| RFC5258 §9.1 | `IMAP-LISTEXT-01` | Registry creation, not additional runtime behavior. |
| RFC5258 §9.2 | `IMAP-LISTEXT-06`, `IMAP-LISTEXT-08` | Vendor/standard extension registration and ownership procedure; no unregistered extension is emitted. |
| RFC5258 §9.3 | `IMAP-LISTEXT-01` | Option registration template. |
| RFC5258 §9.4 | `IMAP-LISTEXT-01`, `IMAP-LISTEXT-03`, `IMAP-LISTEXT-04` | Registered SUBSCRIBED/REMOTE/RECURSIVEMATCH/CHILDREN meanings, subject to documented host boundaries. |
| RFC5258 §9.5 | `IMAP-LISTEXT-04` | Extended-data registration template. |
| RFC5258 §9.6 | `IMAP-LISTEXT-04` | CHILDINFO registration. |
| RFC5258 §10 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5258 §11 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5258 §11.1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5258 §11.2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

## Remaining issue374 scope

The checklist remains PARTIAL. Core non-keyword/grammar alternatives, remaining
search/message extensions, cross-extension cases and recorded implementation or
backend gaps are not completed by this five-spec section review. Earlier core
and session audit handoffs remain separate. Full suites, new measured coverage,
race and fuzz are left to GitHub; no deployment or complete-conformance claim.
