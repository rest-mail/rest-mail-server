# Search and message extension audit

Reviewed 2026-10-26 against canonical RFC Editor text for RFC 3516, 4315, 4731,
5182, 6851 and 6855. Every numbered source section has a disposition, including
non-keyword rules and host/client/unsupported boundaries. Narrow checklist
references are not proof of every clause, grammar alternative or cross-extension
combination. File/section hashes and independent section pins guard the review
structure; they are not an independent conformance oracle.

## Findings and proofs

- Five baseline BINARY text fixtures reproduce bare LF/CR returned unchanged.
  Supported ASCII-compatible text leaves now gain CRLF after CTE decoding and
  before size/partial octet calculations. Already canonical CRLF and the absence
  of a final newline are preserved. BODY storage bytes and flags do not change.
- Opaque application bytes, NUL-bearing payloads, UTF-16 and unknown charsets
  remain unchanged. Whole-message/container and other-charset canonicalization
  still require the host representation; no unsafe byte rewrite is applied.
- RFC2047 encoded header words remain encoded in a whole-message BINARY reply.
- A dependent SAVE/COPY/STORE/UID EXPUNGE transcript proves ordered identities,
  corresponding COPYUID values, correct removals and unrelated Deleted marks.
  MOVE proves no synthesized STORE/Deleted action and COPYUID before EXPUNGE.
- A NO search without SAVE preserves the saved variable; NO with SAVE clears it.
  The printed RFC5182 example 5 omits SAVE while describing clearing. The explicit
  normative section 2.1 rule is followed and independently proved; this is not an
  unverified claim about published errata.
- UTF8 mailbox output stays modified UTF7 until ENABLE, then uses counted UTF8
  literals. ACCEPT, ONLY and obsolete capability boundaries remain explicit.
  Initial COPY metadata and UTF8 quoted-response fixture assumptions were fixed
  before the passing run; neither was a discovered production defect.

## Boundaries

CONDSTORE/QRESYNC, persistent/live UIDVALIDITY, destination-readability privacy,
MULTIAPPEND, IMAP Sieve events, global saved-search budgets, host atomic movement,
quota/ACL/scanning policy, CATENATE, NFC naming and legacy header/body downgrading
are not silently added or marked complete. The backend and deliberate owner
exclusions remain separate from narrow protocol proofs. The overall checklist
and core non-keyword/alternative audit remain PARTIAL.

## Source crosswalk

### RFC3516

Source: [RFC3516](https://www.rfc-editor.org/rfc/rfc3516.txt); SHA256 `53b70a42caba0863cdd4e054a097acd6a073261eef5c2f0585670e318c35bc00`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC3516 §1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC3516 §2 | `IMAP-BINARY-01` | CTE-decoding optimization scope. |
| RFC3516 §3 | `IMAP-BINARY-01`, `IMAP-BINARY-02`, `IMAP-BINARY-09` | Decode then select the output octet domain; opaque octets are not text-rewritten. |
| RFC3516 §4 | `IMAP-BINARY-01` | Extension framework. |
| RFC3516 §4.1 | `IMAP-BINARY-01` | BINARY capability discovery. |
| RFC3516 §4.2 | `IMAP-BINARY-03`, `IMAP-BINARY-04`, `IMAP-BINARY-08` | Decoded partial octets, PEEK/Seen and SIZE are applied to the actual returned representation. |
| RFC3516 §4.3 | `IMAP-BINARY-02`, `IMAP-BINARY-03`, `IMAP-BINARY-05`, `IMAP-BINARY-09` | Literal/literal8 selection, exact sizes and UNKNOWN-CTE refusal. |
| RFC3516 §4.4 | `IMAP-BINARY-07`, `IMAP-BINARY-13` | Unsupported binary APPEND is refused; no accepted rewrite/storage claim. |
| RFC3516 §5 | `IMAP-BINARY-10` | RFC2047 encoded headers are excluded from CTE decoding. |
| RFC3516 §6 | `IMAP-BINARY-08`, `IMAP-BINARY-11`, `IMAP-BINARY-12`, `IMAP-BINARY-13`, `IMAP-BINARY-14` | Supported textual leaves gain CRLF before sizes/partials. Container/other-charset/base-client representation, rewriting and client fallback remain explicit. |
| RFC3516 §7 | `IMAP-BINARY-06`, `IMAP-BINARY-07` | BINARY item, partial, literal8 and UNKNOWN-CTE grammar; old section 6 grammar citation corrected. |
| RFC3516 §8 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC3516 §9 | `IMAP-BINARY-11` | No additional security protocol beyond base requirements; canonical-storage boundaries are not hidden. |
| RFC3516 §10 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC3516 §11 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC3516 §12 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

### RFC4315

Source: [RFC4315](https://www.rfc-editor.org/rfc/rfc4315.txt); SHA256 `4ce974eeaa9110752c7f803ee16bb1c39bd84a2f836a68e413cecbb4c61fa805`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC4315 §1 | `IMAP-UIDPLUS-CAP-01` | UIDPLUS discovery and optional optimization scope. |
| RFC4315 §1.1 | `IMAP-APPENDUID-02`, `IMAP-COPY-03` | Response UID sets exclude star, unlike command sequence sets. |
| RFC4315 §2 | `IMAP-UIDEXPUNGE-01` | Selected-state command extension. |
| RFC4315 §2.1 | `IMAP-UIDEXPUNGE-01`, `IMAP-UIDEXPUNGE-02`, `IMAP-UIDEXPUNGE-03`, `IMAP-UIDEXPUNGE-04`, `IMAP-UIDEXPUNGE-05`, `IMAP-SEARCHRES-07`, `IMAP-UIDPLUS-AUDIT-01` | Only requested Deleted UIDs are removed; state/failure/grammar and saved-result pipeline proofs; clients own fallback. |
| RFC4315 §3 | `IMAP-APPENDUID-01`, `IMAP-APPENDUID-02`, `IMAP-APPENDUID-03`, `IMAP-APPENDUID-04`, `IMAP-APPENDUID-05`, `IMAP-APPENDUID-06`, `IMAP-COPY-03`, `IMAP-COPY-04`, `IMAP-COPY-15`, `IMAP-UIDPLUS-AUDIT-01`, `IMAP-UIDPLUS-AUDIT-02`, `IMAP-UIDPLUS-AUDIT-03` | APPENDUID/COPYUID ordering and valid values; unreadable/nonpersistent destination and multiappend examples remain explicit boundaries. |
| RFC4315 §4 | `IMAP-APPENDUID-02`, `IMAP-UIDEXPUNGE-05`, `IMAP-MOVE-14`, `IMAP-UIDPLUS-AUDIT-02` | Nonzero UID sets, ordered corresponding members and single-append form; MULTIAPPEND not offered. |
| RFC4315 §5 | `IMAP-APPENDUID-05`, `IMAP-COPY-15`, `IMAP-UIDPLUS-AUDIT-03` | Destination-readability privacy gap is retained, not counted as enforced. |
| RFC4315 §6 | `IMAP-UIDPLUS-CAP-01` | UIDPLUS registration. |
| RFC4315 §7 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC4315 §8 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC4315 §9 | `IMAP-UIDPLUS-AUDIT-03` | UIDNOTSTICKY replacement semantics, not arbitrary fabricated identities. |

### RFC4731

Source: [RFC4731](https://www.rfc-editor.org/rfc/rfc4731.txt); SHA256 `e5fad166cf955ab1420a57db02cef70eefe15e6db55f80f8fb99067bfb55facd`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC4731 §1 | `IMAP-ESEARCH-01` | Result-option and ESEARCH discovery scope. |
| RFC4731 §2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC4731 §3 | `IMAP-ESEARCH-02` | Search protocol-change overview. |
| RFC4731 §3.1 | `IMAP-ESEARCH-01`, `IMAP-ESEARCH-02`, `IMAP-ESEARCH-03`, `IMAP-ESEARCH-04`, `IMAP-ESEARCH-07` | MIN/MAX/ALL/COUNT, empty result/list, single response, UID indicator and examples. Clients cannot assume ALL ordering. |
| RFC4731 §3.2 | `IMAP-ESEARCH-06` | Conditional CONDSTORE/MODSEQ examples remain unsupported. |
| RFC4731 §4 | `IMAP-ESEARCH-01`, `IMAP-ESEARCH-05`, `IMAP-ESEARCH-06` | Case-insensitive option/response syntax and unsupported conditional MODSEQ. |
| RFC4731 §5 | `IMAP-ESEARCH-08` | Search work/resource and optional optimization boundary. |
| RFC4731 §6 | `IMAP-ESEARCH-01` | ESEARCH capability registration. |
| RFC4731 §7 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC4731 §8 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

### RFC5182

Source: [RFC5182](https://www.rfc-editor.org/rfc/rfc5182.txt); SHA256 `449a1a747d75a37cbcf01e099e32357ed24d13bdbeb91f1df7ca667717afff6a`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC5182 §1 | `IMAP-SEARCHRES-01`, `IMAP-SEARCHRES-02` | SAVE, result variable, dollar substitution and SAVE-only suppression. |
| RFC5182 §1.1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5182 §2 | `IMAP-SEARCHRES-01` | Extension overview. |
| RFC5182 §2.1 | `IMAP-SEARCHRES-01`, `IMAP-SEARCHRES-04`, `IMAP-SEARCHRES-05`, `IMAP-SEARCHRES-08`, `IMAP-SEARCHRES-10` | Reset/retain/clear rules, expunges, identity-based substitution and empty matching; live UIDVALIDITY changes remain a boundary. |
| RFC5182 §2.2 | `IMAP-SEARCHRES-02`, `IMAP-SEARCHRES-05`, `IMAP-SEARCHRES-10` | Examples 1–6 cover substitution, OR, pipeline and empty set. Printed example 5 omits SAVE but describes clearing; the normative section 2.1 no-SAVE NO-preservation rule is explicitly proved instead. |
| RFC5182 §2.3 | `IMAP-SEARCHRES-02`, `IMAP-SEARCHRES-07` | Dependency order and examples 7–9; the new SAVE/COPY/STORE/UID EXPUNGE transcript also preserves unrelated Deleted marks. Storage semantics are not inferred from dispatch. |
| RFC5182 §2.4 | `IMAP-SEARCHRES-03` | MIN/MAX selection versus ALL/COUNT saved identities and example 10 combinations. |
| RFC5182 §2.5 | `IMAP-SEARCHRES-09` | Optional global save-limit/NOTSAVED refusal is not implemented. |
| RFC5182 §3 | `IMAP-SEARCHRES-06`, `IMAP-SEARCHRES-09` | Case-insensitive SAVE, whole-set dollar marker and optional NOTSAVED response. |
| RFC5182 §4 | `IMAP-SEARCHRES-09` | Saved-state resource budget is an explicit boundary. |
| RFC5182 §5 | `IMAP-SEARCHRES-01` | SEARCHRES registration and ESEARCH prerequisite. |
| RFC5182 §6 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5182 §7 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5182 §7.1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC5182 §7.2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

### RFC6851

Source: [RFC6851](https://www.rfc-editor.org/rfc/rfc6851.txt); SHA256 `c4d5c0a7297ae1d23982ceb8422ddae8275483c4fef7e5db56edc46e55608290`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC6851 §1 | `IMAP-MOVE-01` | MOVE capability and relocation scope. |
| RFC6851 §2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6851 §3 | `IMAP-MOVE-01` | Command overview. |
| RFC6851 §3.1 | `IMAP-MOVE-01`, `IMAP-MOVE-06`, `IMAP-MOVE-07`, `IMAP-MOVE-12` | Sequence-set/mailbox command, selection and read-only gates. |
| RFC6851 §3.2 | `IMAP-MOVE-03`, `IMAP-MOVE-12` | UID MOVE dispatch and grammar. |
| RFC6851 §3.3 | `IMAP-MOVE-02`, `IMAP-MOVE-04`, `IMAP-MOVE-05`, `IMAP-MOVE-08`, `IMAP-MOVE-10`, `IMAP-MOVE-11`, `IMAP-MOVE-15` | No STORE/Deleted side effects, mapping/expunge order, partial failure, TRYCREATE and backend per-message atomicity. Same-mailbox storage semantics and client pipelining caution remain host/client duties. |
| RFC6851 §4 | `IMAP-MOVE-16`, `IMAP-MOVE-17`, `IMAP-MOVE-18`, `IMAP-MOVE-19` | Cross-extension integration scope. |
| RFC6851 §4.1 | `IMAP-MOVE-16` | Quota policy stays with storage. |
| RFC6851 §4.2 | `IMAP-MOVE-17` | ACL union remains outside the backend API. |
| RFC6851 §4.3 | `IMAP-MOVE-02`, `IMAP-MOVE-09`, `IMAP-MOVE-14` | UIDPLUS corresponding identities and COPYUID before EXPUNGE; missing/invalid metadata is omitted. |
| RFC6851 §4.4 | `IMAP-MOVE-13`, `IMAP-MOVE-18` | QRESYNC/VANISHED and mod-sequences remain unsupported. |
| RFC6851 §4.5 | `IMAP-MOVE-19` | IMAP Sieve COPY-versus-FLAG event hooks are not offered. |
| RFC6851 §5 | `IMAP-MOVE-12` | Case-insensitive MOVE/UID MOVE grammar. |
| RFC6851 §6 | `IMAP-MOVE-10`, `IMAP-MOVE-16` | Storage atomicity, quota and target scanning/auditing boundaries. |
| RFC6851 §7 | `IMAP-MOVE-01` | MOVE capability registration. |
| RFC6851 §8 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6851 §9 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6851 §9.1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6851 §9.2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

### RFC6855

Source: [RFC6855](https://www.rfc-editor.org/rfc/rfc6855.txt); SHA256 `d9b7eba828f5ec10b3d1d89a4ced437fbc4dd7813aacde08b8addbf8d351a1ed`.

| Source section | Checklist references | Review disposition |
|---|---|---|
| RFC6855 §1 | `IMAP-UTF8-06`, `IMAP-UTF8-08` | Optional ACCEPT scope and legacy-environment limitations. |
| RFC6855 §2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6855 §3 | `IMAP-UTF8-01`, `IMAP-UTF8-02`, `IMAP-UTF8-03`, `IMAP-UTF8-06`, `IMAP-UTF8-09`, `IMAP-UTF8-11` | Quoted syntax validation, opt-in output, mailbox names and forbidden controls, no CHARSET after ENABLE. Full normalization remains a boundary. |
| RFC6855 §4 | `IMAP-UTF8-04`, `IMAP-UTF8-05`, `IMAP-UTF8-10` | APPEND UTF8 framing and pre-enable header refusal; CATENATE is not offered. |
| RFC6855 §5 | `IMAP-SYNTAX-07`, `IMAP-UTF8-11` | LOGIN not extended for typed UTF8 credentials; AUTHENTICATE carries them and the host owns provisioning. |
| RFC6855 §6 | `IMAP-UTF8-06`, `IMAP-UTF8-07`, `IMAP-UTF8-11` | UTF8=ONLY is not offered; clients enable ACCEPT, never ONLY. |
| RFC6855 §7 | `IMAP-UTF8-08`, `IMAP-UTF8-11` | Legacy surrogates/downgrading/signatures/UIDVALIDITY and client cache reset remain explicit host/client boundaries. |
| RFC6855 §8 | `IMAP-UTF8-08` | No claim of base-compatible legacy headers from a UTF8 mailstore without a host representation policy. |
| RFC6855 §9 | `IMAP-UTF8-06` | Current ACCEPT capability and obsolete capability exclusions. |
| RFC6855 §10 | `IMAP-UTF8-08`, `IMAP-UTF8-11` | UTF8/SASL preparation and legacy-client security boundaries. |
| RFC6855 §11 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6855 §11.1 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |
| RFC6855 §11.2 | — | Document context, conventions, references, acknowledgements or legal metadata; no independent server protocol requirement. |

## Remaining server issue374 work

This completes the first section-level source reconciliation for the six
search/message specifications selected here. Earlier independent ready handoffs
cover core grammar/QUOTA, session negotiation and mailbox discovery/status.
Together they provide source indexes for the advertised-extension review, not
an all-clause or fully implemented conformance claim. Core non-keyword/grammar
alternatives, cross-extension cases, recorded backend/implementation gaps and
wider acceptance remain. Full suites, new measured coverage, race and fuzz stay
with GitHub; no deployment or complete-issue closure is claimed.
