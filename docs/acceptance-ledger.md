# Acceptance ledger

Every open issue's acceptance criteria, classified by **what it would take to prove it**, so
the train can be scheduled by "distance to closing" instead of by theme order.

Buckets:

| | Meaning |
|---|---|
| **P** | proven by a merged PR; the proof is named |
| **R** | provable in this repository now: a test, a contract fix or a document |
| **H** | needs a live/owned topology, but the repository can **ship the harness and receipt**; the block is running one command, not thinking |
| **D** | needs an owner decision before any code can be correct |
| **X** | only an external service or third-party client can prove it |

`H` and `X` are not the same claim. `H` belongs in the repo; `X` is why an issue can
honestly carry `blocked:live-infrastructure` or `blocked:external-service`.

Snapshot: after #2181 merged (`dab81142d`) and before the export/ops cars landed. Rows are
rolled forward as cars merge.

## Distance to closing

| Issue | ACs | P | R | H | D | X | First car |
|---|---|---|---|---|---|---|---|
| #2120 | 5 | 3 | 0 | 2 | 0 | 0 | live runs: diagrams, publication, the walk |
| #2108 | 6 | 2 | 2 | 1 | 1 | 0 | finish it |
| #2117 | 5 | 1 | 3 | 1 | 0 | 0 | finish it |
| #2113 | 9 | 2 | 3 | 3 | 1 | 0 | finish it |
| #2116 | 11 | 2 | 2 | 4 | 3 | 0 | harness + decisions |

The four smallest issues are finishable with one or two cars each, which is the only path
that moves the count. The rest need harnesses and decisions first.

## #2120 — published guides, diagrams, readiness inventory

| AC | Bucket | What is missing |
|---|---|---|
| AC01 deployed diagrams match approved artifacts | H | **Repo half proven; live half is one command, and it is red today.** `diagrams/approved.json` records each diagram's source, served page and image hashes; the `AC01:` tests (`scripts/email-explained/approved.test.mjs`, run by `npm test`) and `node approved.mjs verify` (the guide job) fail a drifted diagram by name. What only the live site confirms: `CHROME=<chrome> node approved.mjs deployed --measure --receipt deployed-diagrams.json`. Read-only run on 2026-11-20 against rest-mail.com: 23 of 27 pages are the approved ones; `spf`, `rest-mail`, `open-relay`, `open-relay-cost` are the pre-#1495 pages, and titles measure 8.2-8.6px against the 9.5px floor. The bucket stays H until a run passes. |
| AC02 five guides published, links resolve, each followed end to end | R + H | **Repo half proven; two live halves stay open, and the first is a finding.** The guides exist, are in the front page, the docs index and the bundle (the `AC02:` tests, `scripts/email-explained/walk.test.mjs`), and the outside links are AC02b. **Published:** the newest bundle (1.0.1393) holds all five, but rest-mail.com answers 404 for each (read-only run of `node published.mjs`, 2026-11-20): the website repository's last bundle commit is 1.0.1150, and its frames still say `v=19995f199586`, which predates the guides. **Followed end to end:** `walk-spec.json` has 41 steps on the disposable topology `guidewalk`, with a deliberate failure in each guide, and `node walk.mjs check` says `NOT WALKED` until an owner's receipt has every step. No walk has run. |
| AC02b outside-link check fails on the four named cases | **P** | #2117/#2120 car: publish + scheduled, never the PR gate |
| AC03 every retained finding has a verified tracker home | **P** | **Proven by this car, once merged.** `docs/findings-homes.json` keeps all 270 rows the reconciliation recorded (147 on #1563, 6 minor, 117 on #1658), each with a typed home: 203 issue or pull request numbers, 38 code citations, 7 files and headings, 6 comments, 3 commits, and 13 dispositions with the reason verbatim. `scripts/findings_homes.py verify` asked GitHub about all 138 references on 2026-11-20 (0 missing); `check`, run by the `test_ac03_*` tests in `scripts/tests/test_findings_homes.py`, fails naming any finding without a home, an unverified or missing number, a stale code citation, a vanished README heading, or a changed row count, and the test fails on any new markdown file of findings. What it does not prove: that a finding is still true in current code beyond its citations. |
| AC04 historical readiness evidence bounded and current | **P** | **Proven by this car, once merged.** `docs/readiness/evidence.json` holds the current claims (state pass, fail, blocked or unknown, observed date, commit, result, limits, receipts), bounded to 21 days and 30 claims; `scripts/readiness_register.py archive` moves what is stale or over the count to `evidence-archive.json` with the date and reason, and `check --base REV` fails on any id that vanished. The `test_ac04_*` tests in `scripts/tests/test_readiness_register.py` prove: a claim with no receipt, or a receipt pointing at nothing, fails by name; a stale claim fails until archived; archiving keeps every field and gives a reason; deleting fails. The #1635 audit's limits are kept verbatim as claims. Not wired to a schedule: `check --now` (against today's date) is for the owner or a scheduled job; the pull request gate checks against the register's own `as_of`. |

## #2108 — rules and Sieve

| AC | Bucket | What is missing |
|---|---|---|
| AC01 one rule identity and lifecycle | R | model + migration work; no infrastructure |
| AC02 clients attribute and undo rule effects | R | same surface as AC01 |
| AC03 templates keep installation/domain/account boundaries | R | shared-template semantics |
| AC04 fixed enabled-extension list, shared with ManageSieve | **P** | v0.9.0 car: extension gate, vacation tracking, `--repair` parity |
| AC04b reject/ereject answer 250 and tell the sender nothing | D | owner decided 250 on #1468; the disposition policy (spam folder vs discard) is not recorded |
| AC05 custom filters cannot escape the account sandbox | R + H | sandbox code + resource limits are repo work; hostile-script exhaustion wants a real install |

## #2117 — metrics, diagnostics, operator APIs

| AC | Bucket | What is missing |
|---|---|---|
| AC01 required metrics and alerts, promtool-tested in CI | R | the queue alerts are **P**; the remaining missing-metric inventory is repo work |
| AC02 correlation and optional tracing across boundaries | R | propagation + redaction tests |
| AC03 public status exposes only approved basic data | R | route + owner's approved field list is on the issue |
| AC04 queue CLI and slow-query diagnostics via scoped APIs | R | CLI through the API, no direct DB |
| AC04b saturation distinguishable from backlog | **P** | three gauges, age-based alert, runbook section |

## #2113 — CI and tests

| AC | Bucket | What is missing |
|---|---|---|
| AC01 pinned reproducible substrate | R + H | image pinning is repo work; parity proof needs a hosted-vs-pool run |
| AC02 docker/service dependency order | R | `--oom-score-adj 1000` guard landed; the dependency-order proof is repo work |
| AC03 cleanup affects only run-owned resources | R | sweep scoping tests |
| AC04 failures have diagnostics and root-cause regressions | **P** | retry bot, `tsvector` 500, promtool-never-ran |
| AC05 trusted access restored without bypasses | H | certificate renewal + SSH host key on the real host |
| AC06 required checks and concurrency enforced mechanically | **P** | retry bot changes no required check, cancels nothing; the full required-check inventory is R |
| AC07 final integrated validation attributable | H | one full run on the final SHA |
| AC08 contract-verdict trust and metadata-edit policy | D | policy question |
| AC09 multi-arch images without emulated exposure | H | needs a native arm64 builder |

## #2116 — data, export, storage immutability

| AC | Bucket | What is missing |
|---|---|---|
| AC01 migration preserves real-source data | H | reference Maildir/Dovecot source |
| AC02 import behaviour and permissions observable | R + H | grant/scoped-credential tests are R; IDLE and audit volume want an install |
| AC03 export complete, streamed, scoped | **P** | page-limit streaming, `--skip-folder`, bounded-memory measurement |
| AC03b every export records an attributable audit event | **P** | `mail:export`, sessions, sweeper, per-format tests |
| AC04 public read access and lifecycle contract | D | retention/erasure/hold decisions |
| AC05 migration tooling and docs end to end | R + H | runbook is R; the actual source migration is H |
| AC01 fork/base and upstream relationship reproducible | H | S3 implementation choice |
| AC02 version and lock APIs with independent clients | X | real S3 client |
| AC03 retention survives overwrite/delete/lifecycle/crash | H | object store |
| AC04 audit anchor uses the supported target | H | deployment |
| AC05 docs and claims match executed semantics | R | after the above |

## #2107 — bytes, streaming, MIME

| AC | Bucket | What is missing |
|---|---|---|
| AC01 file-carrying paths inventoried and spooled | R | inventory + streaming work |
| AC02 hostile shapes bounded per path | R | ceilings and refusal tests |
| AC03 cross-platform allocation failures explained | R | Darwin + Linux comparator; CI can run both |
| AC04 boundedness survives semantic and cleanup controls | **P** | exact DATA octets, dot-stuffing, signature survival |
| AC05 hidden recipients cannot escape either route | **P** | 7 line-end mixes, one head reader |
| AC06 converted metadata truthful | **P** | FETCH/POP3 sizes vs bytes sent |
| AC01 one interpretation preserves charset/address/parameters | R | parser work |
| AC02 malformed parameter policy consistent | R | corpus + linear scanner |
| AC03 filters and scanners cannot miss content | R | shared parse or documented difference |
| AC04 wire normalisation cannot smuggle or corrupt | **P** | large-parameter 500 root-caused (`tsvector`) |

## #2105 — transport and queue

| AC | Bucket | What is missing |
|---|---|---|
| AC01 retry cannot duplicate durable effects | **P** | replay after restart on three routes |
| AC02 local and remote mail share truthful outcomes | R + H | worker paths are R; real receiver is H |
| AC03 slow destination cannot monopolise the queue | R | worker tests with stalled destinations |
| AC03b in-flight limit configurable and distinguishable | R | chart exposure + docs; the metric is **P** |
| AC04 receiver backoff policy explicit and isolated | D | domain-vs-MX decision not recorded |
| AC05 DSN actions and provenance cannot mislead | **P** | delivering-row races fenced |
| AC01 transport defaults enforced | R | defaults + chart |
| AC02 real deployment sends and receives authenticated mail | X | live install |
| AC03 receiving server verifies our authentication | X | third-party receiver |
| AC04 complaint ingestion attributable and safe | D | ingestion provider decision |
| AC05 reputation controls isolate a domain | R + H | logic is R; real reputation signal is H |

## #2104 — TLS and releases

| AC | Bucket | What is missing |
|---|---|---|
| AC01 integration/ownership model documented and upheld | **P** | uploaded certificate keeps its issuer; renewal selects only its own |
| AC02 renewed material consumed by every service | H | live reload |
| AC03 real ACME issuance and deliberate renewal failure | X | ACME staging on rest-mail.com |
| AC01 Pulumi adoption with no replacement | H | Pulumi preview on the stack |
| AC02 staging and preview isolation | D + H | isolation contract decision, then a real install |
| AC03 backup demonstrated by a restore drill | H | RPO/RTO decision, then the drill |
| AC04 release artifacts have SBOM and fresh-start evidence | H | image build + scan policy |
| AC05 installed cluster stages one to three run | H | cluster |
| AC06 promotion gated on fresh matching receipts | **P** | gate + 21 refusal tests; not yet wired to a release workflow |

## #2101 — personal and assistant

| AC | Bucket | What is missing |
|---|---|---|
| AC01 mail operations preserve state and truthful outcomes | R | contract surface |
| AC02 scheduling and due dates explicit and reversible | D + R | 7 due-date decisions, then send-later/undo tests |
| AC03 preferences and notifications end to end | R + H | time zone is R; real push needs a device |
| AC04 previews and unsubscribe cannot execute or SSRF | R + D | sandbox tests are R; Office conversion is a decision |
| AC05 contacts and calendar round-trip and isolation | **P** | vCard trust gate, `N` names, quoted-printable |
| AC01 ledger and trust enforce accountability | R | endpoint-level tests |
| AC02 reviewed proposals use draft/send and policy | R | workflow tests |
| AC03 inbox triage reports traceable, reversible results | R | counts vs persisted state |
| AC04 meeting workflow uses real calendar facts | R | conflict/availability tests |
| AC05 foundations and client acceptance proven together | H | final integrated run |

## #2100 — protocols and gateways

| AC | Bucket | What is missing |
|---|---|---|
| AC01 HTTPS delivery reachable in supported topology | H | two installs, DNS, test certs |
| AC02 native relay scope stays a truthful lane | D | approve or defer |
| AC03 delivery and import contracts separated | R | schema separation |
| AC04 gateway status translation preserves meanings | **P** | per-domain inbound pipeline + status translation |
| AC05 every public/gateway operation has an executable proof | H | full surface, live |
| AC01 requirement inventory complete | R | RFC reconciliation |
| AC02 rev1/rev2 session and mailbox wire proof | H | real clients |
| AC03 dates and resynchronisation honour account state | R | zone/DST tests |
| AC04 gateway security tied to RFC rows | H | listener configuration |
| AC05 FETCH and POP3 accounting matches client bytes | **P** | metadata vs octets |
| AC06 new protocol support decided or demonstrated | D | JMAP decision |

## #2099 — contracts and fixtures

| AC | Bucket | What is missing |
|---|---|---|
| AC01 typed registration and generated clients cannot drift | R | coverage assertion |
| AC02 conventions decided and enforced | R | decision matrix + tests |
| AC03 named pairs, including deliberate schema violations | **P** | strict areas for all 234 operations |
| AC04 examples depend on the state they claim | R | setup + setup-removed controls |
| AC05 audit and forward successes survive full compilation | R + H | **Proven locally by this car:** both descriptions compile the audit reads and forward refusals; the ordinary forward is selected without a fault and persists through the production router and PostgreSQL. The live audit chain and scoped fresh-MFA topology exchange were not rerun here. |
| AC06 final proof records exact executed coverage | **P** | `coverage()` counts each population |
| AC01 (#2112) each exchange isolated prerequisites | R + H | **Local proof and live harness supplied by this car.** `test_own_data_isolated_schedule` selects `getDomain`, `createFolder` and `releaseHeldMessage` alone, then twice shuffled, on one endpoint. `fixture_isolation.py` requires a passing nonempty report, a matching reset/seed trace for every named exchange, and stable per-operation counts. `test_stale_cache_and_counter_control_turns_isolation_red` rejects retained sender caches and an unreset counter; disabling cache clearing or removing reset/seed makes it fail. `TestTestDataResetRegisteredPipelineBudgets` pins the real router's minute/hour counter reset, including failed-reset and production controls. `topology:ci` runs the selections and full own-data phase; receipts live under `.cache/contract/isolation/`. PR CI remains the integration receipt. |
| AC02 setup preserves identity, scopes, MFA | H | real sessions |
| AC03 (#2112) held-message release is a persisted transition | R + H | **Proven locally through PostgreSQL by `TestHeldReleasePersistsThroughRouter`.** Real API enrollment/sign-in supplies MFA; after reset/seed an API-created sender spends its own budget, retaining the actual 202 ID. Release returns 200 and the same row is stored as sent with a real Sent message and queued delivery. Invalid, absent, anonymous, password-only, unrelated MFA and non-held controls leave the record/queue unchanged. Removing `sent_message_id` persistence makes the proof red. The own-data `releaseHeldMessage/released` hook makes the same real hold, checks its ID/status, independently reads the Sent copy, checks the held list, and requires a subsequent 409 `not_held`; `test_release_spends_the_disposable_senders_own_budget_and_keeps_the_actual_202_id`, `test_release_requires_the_same_row_to_transition_and_a_persisted_sent_copy` and `test_schema_valid_200_without_setup_or_persistence_is_red` pin its setup and missing-state controls. Only own-data provides `held_message`; ordinary refusals keep their credentials and targets. PR CI remains the integration receipt. |
| AC04 pacing covers minute/hour and session lifetime | H | deterministic clocks on a live server |
| #2156 lost-probes baseline | R | **Measured by the whole-API Vertrag 0.47.0 gate on topology `fixture2099cx`, 2026-11-21:** `1938 total, 1938 passing, 0 failing, 0 errors, 0 skipped`; `Coverage phase: 86 probes ended by a hook, unsent and in no report; the baseline holds 89`; `Probes ended by a hook decreased: setAccountTimeZone: 0 < 3; shrink tests/contract/lost-probes-baseline.txt`. Its allowance is removed, which enforces zero without changing probe selection. `test_set_account_time_zone_has_no_lost_probe_allowance` accepts zero and rejects a returned probe. |

**AC01 selection control.** `test_explicit_operation_selection_really_runs_alone` and `test_cli_keeps_explicit_selection_and_rejects_unknown_extra_arguments` prevent Vertrag's union of configured/CLI operation IDs from silently undoing an alone selection. The native 0.47.0 dry-run control, on identical served-description bytes, changes from `627 transaction(s)` across 201 operations to `3 transaction(s)` for `getDomain` alone. The full phase retains every MOVED operation; only an explicit operation selection intersects that list. The finding is recorded on #2099.

**AC05 local evidence (#2084, #2085, #2086).** `TestAuditVerificationCompilesSuccessWithVertrag` pins `verifyAuditLog/found` (200), `verifyAuditLog/invalid_anchor` (422), and `listAuditEntries/available` (200) in the untouched full production and test descriptions. Removing the historical valid-anchor illustration reproduces `OK=0, invalid anchor Unprocessable Entity=1`; restoring the illustration returns one of each. Removing the named ordinary forward path reproduces `ordinary forward success incorrectly requires map[fault:database]` and selects zero 201s on Vertrag 0.45.0. `TestForwardSuccessRetainsIndependentRequirementsWithVertrag` selects one 201 with the current path on both 0.45.0 and the 0.47.0 pin; `TestComposeNamedExamples/forwardMessage/example` sends it to the published source ID and checks persisted Sent/queue results.

- `TestForwardRequestSchemaMatchesRequiredEnvelope` and `TestForwardHistoricalExamplesCompileWithVertrag` pin the six 422 pairs by name, request body, and status. Removing the description-only forward request override reproduces `forward schema accepts …: got true, want false` for all six in both registrations. The repaired description rejects all six via `required`, also `minItems` for an empty recipient list and `format: idn-email` for an empty sender (`minLength: 1` is declared and checked separately).
- `TestForwardRequiredEnvelopePreservesSourceAuthorizationOrder` sends the actual pair bodies without schema prevalidation: anonymous 401, inaccessible source 404, accessible source 422 `validation_failed` with both `details.fields.from` and `details.fields.to` equal to `required`, and no forward side effects. Removing the handler rule changes all six accessible-source responses to a downstream sender-only error while retaining status 422; the response assertions catch it. The historical defect was schema acceptance, not a handler that forwarded those requests.
- `TestForwardAttachmentsCanonicalExamples/attachment_write_failed` reaches an injected attachment-write error and requires a 500 `internal_error` with no committed Sent, attachment, queue or quota state. The same valid request returns 201 without injection. `TestForwardSuccessRetainsIndependentRequirementsWithVertrag` pins `fault: database` to the 500, not to the ordinary 201 or the six 422s.
| AC05 the ordinary contract genuinely executes | H | isolated Linux/mTLS topology |

## #2098 — identity and domains

| AC | Bucket | What is missing |
|---|---|---|
| AC01 provisioning and recovery safeguards | R | invitation/recovery tests |
| AC02 scoped identity and grant ceilings | R | independent accounts + chains |
| AC03 gateway sessions identify the real caller | **P** | **Proven by this car, once merged,** in `internal/api/gateway_sessions_ac03_db_test.go`: the API's internal listener is a real TLS listener requiring a certificate from the internal CA (`mtls.ServerTLSConfig`), each gateway presents its own (`mtls.ClientTLSConfig`), and mail clients connect from a loopback address other than the gateways' 127.0.0.1. **SRC-1586:** `TestAC03MailSessionsEndWithTheCredentialOrAccount` signs in over real IMAP, POP3 and submission with an app password, checks the one open `gateway_sessions` row (account, credential, service, protocol), then revokes the app password (`DELETE /api/v1/accounts/{id}/credentials/{cid}`) or disables the account (`PATCH /api/v1/admin/mailboxes/{id}` `{"active":false}`). The connection's next command is refused (IMAP `NO`, POP3 `-ERR`, submission `530`), its session row is closed, another account's connection through the same gateway keeps working, and the ended credential cannot sign in again. **SRC-1957 identity:** `TestAC03OnlyAnAuthenticatedGatewayStatesTheClientAddress` records real sign-ins and refusals from the mail client's address, never the gateway's. A claimed `client_ip` plus `X-Forwarded-For`/`X-Real-IP`/`Forwarded`/`True-Client-IP` is refused from a certificate naming no gateway and from the delivery certificate (403 `gateway_required`, audited from the caller's own address). It fails the handshake with a gateway name signed by another CA or with no certificate, and is not found on the public listener; no session opens. An authenticated gateway's body statement wins over proxy headers, and its session token is refused with any other certificate. **SRC-1957 budgets:** `TestAC03EachClientAddressHasItsOwnAllowanceOverMTLS` gives 20 stated addresses through one gateway certificate their own allowance, refuses one busy address past the burst of 15 without affecting a new one, and lets a caller that is not a gateway gain nothing by stating addresses. The same with real sockets from 20 source addresses is `TestSignInsFromManyClientsDoNotShareAnAllowance`, proven on Linux CI only (macOS has no 127.0.1.x). **SRC-1935:** `TestAC03RemovedSignInRoutesStayRemoved` answers `POST /api/v1/auth/protocol-sign-in` 404 on both listeners. HTTP Basic gets 401 on the public listener, 404 on the internal one and 400 `credential_required` on session open, never with a `WWW-Authenticate` challenge. The description and examples name no `protocolSignIn` or Basic scheme, and `openGatewaySession` (security `[]`) signs the same app password in. Mutation controls go red: dropping the revoked/active checks in `CredentialRepository.Usable` (revocation), plus the disable's `endSessions` (disablement); ignoring the stated `client_ip`; believing `client_ip` from any caller. |
| AC04 credential hardening without lockout | **P** | **Proven by this car, once merged.** `TestAC04CredentialHardeningWithoutLockout` checks the persisted cost-12 password, 401 without upgrade for a wrong guess against a cost-8 hash, and successful sign-in with a persisted cost-12 upgrade. Controlled-clock HTTP and gateway-session calls prove per-client and cross-IP delay, no wait on correct credentials, account isolation and decay. IMAP, POP3 and submission client identities at the internal router have independent gateway failure allowances. Recovery and session-gated password policy (#1798) is unchanged and not inferred from these results. |
| AC05 identity descriptions match behaviour | R | named-pair + ownership cases |
| AC01 domain naming and ownership | D | `TestAC01DomainNamingAcrossRoutes` and the AC01 cases in `DomainSetupAcceptance.test.tsx` prove typed empty-label refusals and a single trailing ASCII dot accepted and normalized on creation, alias sources and discovery/autoconfig; the admin form applies the server answer. **Ownership policy (#1584 / #2102): undecided.** Domain creation requires installation-wide authority; a servable certificate and receiving postmaster gate activation. The DNS screen is diagnostic, not a TXT ownership challenge. There is no ownership-based send/receive gate to prove; the full criterion remains blocked on that decision. |
| AC02 delegated administrators finish setup | **P** | **Proven by this car, once merged, for the authorized setup contract.** `TestAC02DelegatedDomainAdministratorSetup` proves global provisioning with a receiving postmaster, then delegated DNS inspection, domain settings, mailbox and MTA-STS writes, and report-list access. Foreign and global-only calls return 403; a missing domain returns 403 to the delegate and 404 to a global administrator. The AC02 case in `DomainSetupAcceptance.test.tsx` renders the permitted DNS screen and denies global creation. `TestPostmasterSMTPReportDelivery` proves report reception for both a mailbox and an alias; `TestDomainMailPolicyDomainGrant` covers inherited route boundaries. Domain creation, certificate administration, whole-server queue and DKIM lists, and relay writes are not delegated; DNS ownership challenges remain the open AC01 decision. |
| AC03 soft delete/restore + mixed retired/live batch | **P** | automation lock and batch refusal |
| AC04 purge and automation locks human-only | **P** | unlock-before-delete |
| AC05 mail-policy choices are not accidental | D | greylisting default, catch-all, billing |

## The decisions this ledger needs from the owner

1. #2108 AC04b — when a Sieve `reject` fires, is the message filed to spam or discarded?
2. #2105 AC04 — receiver backoff keyed on domain or MX?
3. #2104 AC02' — staging/preview isolation contract.
4. #2101 AC02 — the seven due-date decisions; AC04 Office conversion.
5. #2098 AC05 — greylisting default and catch-all precedence.
6. #2113 AC08 — contract-verdict trust policy.
7. #2051 — whether the invitation 409 row is `ignored`, since the server is right and the tool cannot express it.
