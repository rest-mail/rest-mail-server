# Contract tests

[vertrag](https://github.com/antimatter-studios/vertrag) sends the selected
contract requests to a running API and checks each answer against the spec's
status, headers and body schema (#376). Every phase uses the generated
`/api/docs/openapi.json`, saved with that run’s reports. The handwritten
`openapi.yaml` remains the independent comparison oracle, not the runtime input.

The additional [live requirement proofs](live-requirements-findings.md) exercise
anonymous callers, real over-cap bodies and a bounded real-principal matrix
with the pinned Vertrag. Their findings distinguish local evidence from the
in-process proof union and the still-outstanding full-topology acceptance.

Bad input follows the owner's decision on #672 (2026-10-28). A body that is not
valid JSON, or is not the declared media type, gets 400 `bad_request`. Parsed
input that breaks a shape, field or combination rule gets 422
`validation_failed`, with `details.fields` naming the failing fields. This
includes arrays/null where an object is required, invalid Sieve, unsupported
filters and recipient limits. State preconditions retain their existing status
pending a separate decision. The 403/404 decision remains open.

## Where it runs

In the `E2E (full topology)` job (`.github/workflows/e2e-suite.yml`), after the
14 stages pass, against the topology they ran on, as the fixture admin
(`admin@restmail.test`, from `cmd/seed`):

| Phase | What it checks | In CI |
| --- | --- | --- |
| example pairs | Each registered `*WithVertrag` test in `internal/api` sends its documented example pairs and their boundary cases through Vertrag to the API in the test process (`chore contract:pairs`). It needs no topology. | Gate, first |
| `examples` | Each documented request gets its documented answer. Ordered by the spec's links (`--sequence`), so a read, an update or a delete addresses what its create made. | Gate |
| `coverage` | Every boundary each schema implies (the maximum, one past it, a required property missing) gets a 4xx, never a 2xx or a 5xx. | Gate, as a ratchet (below) |
| `ignored-auth` check | Every authenticated request, sent again with no credential, is refused. | Gate |
| `authorization` | Every selected operation as each configured principal, checked against the spec's security roles. A signed-in caller refused an object the path names may get the 404 that hides it (#672); `authorization_refusals.py` names any 404 outside that, so an admin route stays 403 and no credential stays 401. | Report only, after the gates |
| `ownership` | Each account tenant's principals sent the other tenant's account id, on the operations they are entitled to; every answer must be 403 or 404. | Report only, after authorization |
| `fuzz` | Values drawn from each schema, shrunk to the smallest failing input, with a replayable seed. | Nightly (`contract-fuzz.yml`), never blocks |
| `own-data` | The operations moved to a clean start, each exchange after a reset and seed, with what follows `createDomain`'s links ([Moved to a clean start](#moved-to-a-clean-start-part-of-1385)). | Gate, before the teardown |
| faults | The API built with each fault in `faults.yml`, and the phase the fault names run on its operation: it must turn red ([Fault catalogue](#fault-catalogue-654)). | Nightly (`contract-faults.yml`), fails on a fault not caught |

### Skipped valid coverage probes (#1443)

The newest completed merge-group E2E run available when this work started was
[41458365703](https://github.com/rest-mail/rest-mail-server/actions/runs/41458365703)
(2026-11-02, main merge group for #1478). Its log identifies Vertrag 0.42.0
and names all 29 operations behind **101 skipped valid probes**. The additional
six login probes are expected: generated credentials cannot authenticate.

| Group | Operations and skipped probe counts | This change |
| --- | --- | --- |
| Alias and sender-rule targets consumed/replaced before coverage | `getAlias` 1, `updateAlias` 5, `removeFromAllowlist` 2, `removeFromBlocklist` 2 | Repaired and removed from the skipped-probe baseline |
| Credential targets absent | `revokeAPIKey` 2, `revokeCredential` 2 | Repaired and removed from the skipped-probe baseline |
| Contact/folder targets absent | `deleteContact` 2, `updateContact` 9, `deleteFolder` 2 | Repaired and removed from the skipped-probe baseline |
| Filter/pipeline targets absent | `deleteCustomFilter` 1, `getCustomFilter` 1, `updateCustomFilter` 15, `testCustomFilter` 1, `deletePipeline` 1, `updatePipeline` 7 | Repaired and removed from the skipped-probe baseline |
| Certificate target absent | `getCertificate` 1 | Repaired and removed from the skipped-probe baseline |
| Domain/policy targets absent | `deleteMtaStsPolicy` 1, `getMtaStsPolicy` 1, `setMtaStsPolicy` 11 | Repaired and removed from the skipped-probe baseline |
| Held-mail/queue/scheduled fixtures | `deleteQuarantine` 2, `clearSuppression` 2, `bounceQueueEntry` 1, `cancelScheduledMessage` 1, `deleteQueueEntry` 1, `getQueueEntry` 1, `retryQueueEntry` 1 | Quarantine/suppression, `cancelScheduledMessage` and single-entry queue operations repaired; remaining: 0 |
| Draft/send requests | `updateDraft` 10, `sendDraft` 1, `sendMessage` 14 | Repaired and removed from the skipped-probe baseline |

The log says 404 for absent targets, 422 for the draft/send examples, and
"got no answer" for quarantine/suppression/bounce/scheduled cancellation.
Those remaining groups still need per-operation diagnosis; the grouping is
not a claim that every underlying cause has been proven.

Vertrag sequences examples with linked resource IDs, but coverage starts from
the description's original placeholder IDs. Its before hooks receive the
original transaction name, **not** the reporter's `coverage:` prefix.
`run.sh` therefore scopes `CONTRACT_COVERAGE=1` to the coverage process (and its
retry). The sender-rule and alias hooks send placeholder `1`, like any positive
id other than missing's that no entry or alias has, to one of their own, made
through the admin API with an empty process stash, independently for each list.
Other IDs,
invalid paths, request bodies and transaction credentials are unchanged.
Other phases do not acquire this placeholder substitution.

Credential revocation and certificate reads also receive a fresh target for
each coverage success request aimed at placeholder `1`. Revocation creates a
disposable credential through the API using a separate real MFA setup session;
it never picks an existing credential that another phase may need. Certificate
reads create a real self-signed certificate on a new disposable domain, without
automatic renewal. Only the request path changes.

Quarantine deletion and suppression clearing consumed the single fixture in
coverage's initial examples, before its example preflight and valid probes.
Their coverage hooks replenish held mail before each success request and use
the exact newly created row IDs. `test:contract-held-mail-fixture` invokes the
guarded Go fixture helper with `--held-mail-only`; it does not replenish or
change queue entries. Original example IDs and schema-minimum IDs are handled;
malformed paths, refusal examples, bodies and principals stay unchanged.
Pinned Vertrag 0.42.0 reproduced nine skipped probes across these five
operations before this setup and none afterward. No description or server
validation change was needed. This is Part of #1443.

`cancelScheduledMessage`'s historical "got no answer" did not reproduce on
current main in the narrowed local run: its existing hook successfully creates
a scheduled message. Its baseline entry is retained rather than claiming that
the historical topology failure has been repaired.

MTA-STS creation's documented domain `2` is not a topology fixture. Its hook
creates a private domain through the admin API, then removes any policy before
each 201 request. Read, update and delete success requests get an existing
policy immediately before sending, including after a preceding delete consumed
it. This setup works in both processes without after-hook state. Request bodies,
credentials, malformed IDs and missing-domain refusals remain unchanged. A
narrowed local replay against the real API and PostgreSQL reproduced all 17
MTA-STS/sender-rule skips before these hooks and none afterward. This is
Part of #1443, not proof of the whole-topology result.

Custom filter and pipeline examples name placeholder `1`, which nothing holds
once the examples process has gone. In the coverage process each success request
aimed at `1` gets a new row, disabled or inactive, on a disposable domain, and
only its path changes. A new row each time means a delete or update before it
cannot consume the target or remove the script `testCustomFilter` runs. A
narrowed local replay reproduced all 26 skips before these hooks and none
afterward. This is Part of #1443, not proof of the whole-topology result.

The probes this reached exposed two description defects. `updateCustomFilter`'s
`name` allowed an empty string, and the `domain_id` both updates accept and ignore
allowed a negative number, while the server refused both with 422. They now
declare `minLength: 1` and `minimum: 0`, keeping the same field messages. An empty
name is now refused before the lookup, so aimed at a missing filter it answers
422 rather than 404.

Contact update's `1`, contact deletion's `41`, draft update's `12` and folder
deletion's `Work` are literal example targets, not fixtures. In the coverage
process only, their success requests get a fresh row through the API and only
the path changes. The minimum-id probes (`1`) get their own rows too; malformed
ids, other accounts, refusal examples, bodies and credentials stay untouched.
The hooks live in `contact_draft_hooks.py`, registered by `entrypoint.py`, so
they do not depend on the examples process's stash or on an after hook.

A local replay with pinned Vertrag 0.42.0 and PostgreSQL reproduced the four
404 examples and their 23 skipped valid probes. `sendDraft` already succeeded
alone through its existing fresh-draft hook; the failed draft-update link
prevented it from running in the sequence. With the new setup the five
operations pass together: 59 probes, no findings and no skips. No description
or server behaviour changes were needed. This removes their 24 ratcheted
probes as Part of #1443; it does not establish the whole-topology count.
A fresh 16-operation sequence, including draft saving, contact creation and
listing, folder creation and renaming, message listing and `deleteMessage`,
passed 17 success examples in a separate process, then all 170 coverage probes
with `--fail-on-skipped-probes`. The setup used real API-created rows for the
neighbours' literal example targets. This is not the full examples gate.

The newly reached alias update probe exposed a real description defect:
`destination_address` allowed an empty string, but the handler refused it with
422. It now declares `format: idn-email`, like alias creation, without changing
the handler's address policy or its support for omitted/null patch fields.

Single-entry queue coverage also gets a fresh deferred row before every success
request, including the example preflight and minimum-ID probe. After a real
empty-filter bulk deletion, pinned Vertrag reproduced all four skipped probes:
delete, get and retry addressed absent placeholder `1` and answered 404; bounce's
old hook found no dedicated bounce fixture and raised before sending. The retry
hook could replace a consumed row, but not an absent one. The new
`queue_entry_hooks.py` uses `CONTRACT_COVERAGE=1`, the original success status,
method, valid queue ID and fixture-admin session. It also replaces linked IDs:
a wider queue sequence showed that bulk bounce can consume a linked retry
target before coverage's initial example. Only the path changes; bodies,
queries, credentials, malformed IDs, other routes and refusal targets survive.
Other phases retain their existing queue hooks. The guarded
`test:contract-queue-entry-fixture` command calls `--queue-entry-only` and returns
the exact new row ID without adding held mail or changing other fixtures.
Two consecutive focused runs on the same database each reported 16 probes,
zero findings and zero skips with `--fail-on-skipped-probes`. The wider queue
sequence, including all three bulk actions, passed 91/91 transactions with
79 probes, zero findings and zero skips. Queue setup does not send a message
or depend on `sendDraft` or its separate #1529 repair.
Only the merge-queue topology proves the whole gate: a focused local run has
previously passed while that gate failed because `sendDraft` needed #1529.

`skipped-probes-baseline.txt` holds the **0 remaining probes across 0
operations**, individually, not as a shared budget. `gate.py` reads 0.42.0's
JUnit skipped-case reason and fails on a new operation, an increased count,
an unknown skip format, or a decrease not removed from the baseline. Skipped
cases never count as passing boundary probes. This file does not exclude
anything: the requests and valid probes remain selected. The actual whole-API
count still requires the merge-queue topology; narrowed local proofs cannot
establish it.

Vertrag 0.42.0's `--fail-on-skipped-probes` fails coverage with exit 2 when
non-login valid probes are skipped; it deliberately exempts login. Once this
baseline is empty, use that flag directly instead of retaining skip debt.

### A hook cannot change a generated body

Vertrag 0.42.0 ends a generated probe whose body a before hook changed
(`runner.ErrChangedByHook`). `fuzz.Cover` marks it not sent, and the coverage
command then drops it: it is not in the report, the skip count or
`skipped-probes-baseline.txt`. A hook that always rewrites a body therefore
leaves that operation's valid probes unsent and invisible. A hook also never
sees a `coverage: ` name: the prefix is added to the result, not the transaction.
`CONTRACT_COVERAGE=1` is what tells a hook it is in the coverage process, whose
examples phase and working check it also sees.

So `lost_probes.py` counts them from inside the hook worker, as the last hook of
every request, and `gate.py` holds the counts to `lost-probes-baseline.txt`, per
operation. A new operation or a larger count fails. A probe whose body a hook
changes, or that a hook skips, is a line in `lost-probes.txt`, and a passing gate
prints how many there were. The first two requests of each transaction are the
documented ones, which a hook may rewrite: only a generated body is compared. The
file says why each operation is there. The counter also registers after the
quiet-hours hooks, so replacing the generated `time_zone` placeholder `x` with
`UTC` cannot silently discard probes.

There are three ways to keep a probe:

- **`fuzz.pin`** holds a value the schema cannot describe, before the probe is
  rendered. `snoozed_until` and `expires_at` are held in the future because the
  generator's only date-time is in 2020, and the server rightly refuses that
  (`probes.py`). `permissions` is held at a real capability the probe
  administrator has, because the generator's stand-in is capability `x` at scope
  `x`. A pin is global by field name, so it does not suit `password`, which eleven
  operations have. Vertrag does not look through a body's nested `$ref` to see
  what a pin names, so `apiKeyPermission` is an alias that the description holds in
  the body.
- **The fixture matches the body.** `createAPIKey` and `updateAPIKey` check the
  caller's current password, and the coverage process sends them as
  `contract-probe-admin@restmail.test`, whose two passwords are plainly fake and
  are what the requests carry: `x`, which the generator draws, and
  `current-password`, which the documented examples say. The handler's comparison
  is the real one, and it signs in with a real second factor (`mfa_admin.py`). A
  wrong password is still refused: `updateAPIKey`'s `wrong_password` example, and
  `createAPIKey`'s in `api_keys_live.py`.
- **Only the path changes.** The same two hooks put these requests on that
  administrator's own account, so a key is within both grants, and `updateAPIKey`
  edits a key made for it.

A pin takes away the probes that vary its own field, so the named examples prove
those refusals in the gated examples phase, which is not pinned. For snooze:
`past`, `malformed_time`, `snoozed_until_missing` and `snoozed_until_wrong_type`.
For `createAPIKey` and `updateAPIKey`: `permissions_missing`, `permissions_empty`,
`permissions_too_many`, `permissions_wrong_type`, `permission_scope_missing`,
`permission_capability_wrong_type` and `permission_unknown_field`. For
`createAPIKey` too: `expires_at_malformed`, `expires_at_wrong_type` and
`expires_at_past`.

`createAppPassword` cannot be held this way. Its description lets a request leave
out `password`, and the handler answers that with the documented 403
`reauth_required`, which Vertrag calls a disagreement. So its probes the schema
forbids reach the handler as drawn, and its well-formed ones are still given the
password, and counted, until that disagreement is decided.

### Coverage keeps restmail.test unchanged

`off_the_admin_domain` moves both DKIM and `updateDomain` coverage probes to a
disposable domain. Domain updates therefore cannot leave the fixture admin's
domain at `sending_policy` bulk or `dmarc_policy` reject and skip the send probes.
Examples and authorization/ownership requests retain their intended targets.

### A delivery's recipient in the fuzz phase

Fuzz draws a `mailbox_id` or an `address` that names nothing, and Vertrag holds a
valid body's 404 against the description (its exception covers path parameters
only). Every generated delivery stopped at the lookup, so the shrunk finding was
always `{"mailbox_id":1000}` or `{"address":"a"}` and the rest of delivery was never
reached. A hook cannot supply the recipient, for the reason above.

`probes.py` pins `mailbox_id` in the fuzz run to the mailbox `run.sh` makes first
(`chore test:contract-delivery-recipient`, declared in `fixtureplan/defaults.json`).
Mail goes to it, not to the fixture admin. Its domain stores an inbound pipeline with
no filters, because a generated body has no Date or Message-ID and the default
pipeline rightly answers 550 to that. A pipeline with filters is proven by the
`rejected` and `processed` examples, not here. Its rows name their ids, so no
sequence moves.

The coverage run is not pinned and does not need the recipient. `deliverMessage`
has no valid boundary probe, only 28 that a schema refuses before any lookup, so
coverage makes no extra mail and no literal id in an example moves. A pin there
would only stop its two `mailbox_id` type probes. The fuzz run makes its mail in a
report that blocks nothing, and later fuzz operations see message ids moved by it.

The pin takes away the refusals fuzz drew by varying the recipient. The named
examples prove those in the gated examples phase: `address_empty`,
`mailbox_id_zero`, `recipient_missing` and `no_mailbox_by_id`. `no_mailbox` already
holds the 404 by address.

### Message ids in examples

deleteMessage's `primary` example names message 51, and the coverage phase sends
it with that id before the valid probes. 51 names whichever row the sequence
reached, so any change in how many messages the run makes moves it: with one more
import it became the draft `sendDraft` removes, the example answered 404, and both
valid probes were skipped. `delete_a_message_we_made` gives the fixture admin's
request a message of its own, by changing only the path.

### Bulk queue fixture isolation

The published `empty_filter` exchanges intentionally act on every eligible queue
entry. In particular, bulk bounce consumes the held retry fixture before the
linked single-entry retry runs. Its success hook checks the linked row first:
only a non-retryable entry belonging to the declared retry fixture is replaced
by a fresh deferred entry from `chore test:contract-retry-fixture`. The original
row keeps the bulk action's result. Eligible entries, unrelated IDs, malformed
IDs, documented refusals, request bodies and caller credentials are unchanged.
No exchange or probe is excluded, and neither coverage baseline is increased.

The hook replenishes that one row and nothing else. `chore test:contract-fixtures`
also adds a quarantined message and a suppression, and those counts decide what
coverage reaches: `clearSuppression` and `deleteQuarantine` hold two skipped
probes each because coverage's example finds no fixture left. A spare row lets
that example pass, so the probes run and the skipped-probe baseline no longer
matches. For `clearSuppression`, the next probe with the example's path then
finds nothing to clear.

### Attachment download contract

The topology examples send an ordinary download, `Range: bytes=0-0` for a
single-byte 206, and a range past the end for a plain-text 416. Named path/header
examples pair those requests with their responses. Content bytes depend on the
topology's seeded attachment; the selected media types and required headers
remain checked. Vertrag 0.31.3 and later keep optional response headers optional and
does not send invented empty conditional headers.

The bodyless 304 and 412 are told apart by name too: each carries `x-example`
on its Response Object, so `If-None-Match` is sent only with the 304 and
`If-Match` only with the 412. Their requests go to the seeded attachment like
the rest. The 500 is a dependency failure, proven in process
(`TestAttachmentDownloadExamplesWithVertrag`) because a healthy topology cannot
provoke it.

### Own-account authenticator contract

The normal examples run includes `getTwoFactorStatus` and `enrollTwoFactor`,
with their 200 responses and missing-session 401s counted. Enrollment has no
request body, leaves a pending secret and does not change the fixture admin's
sign-in requirements until it is confirmed. The shared authentication pacing
hook covers enrollment; it leaves the transaction's principal untouched.
Confirm runs as the two-factor administrator (below), whose authenticator is
already on, so it is refused and the fixture admin's sign-in requirements are
left alone. Disable is sent with only its refusals: the fixture admin has no
authenticator, and a disable that succeeded would revoke the session.

### Operations whose setup the run makes, or whose refusals are all it sends

The contract fixtures (`prepareHeldMail`) hold a quarantined message for the
fixture admin and a suppressed recipient on `restmail.test`; the hooks point
`deleteQuarantine` and `clearSuppression`'s `found` examples at them.
`releaseQuarantine`, `respondToCalendar`, `resetAdmin`, `changeOwnPassword`,
`disableTwoFactor` and the owner operations send the examples that need no
setup (the topology lists no owner or status keys, so those answer 409); their
other examples are proven in-process (#1334).

Each `found` example spends its row, and the report-only phases send those
examples again: authorization in its examples and its admin cell, ownership in
the examples of its account and domain runs. So `run.sh authorization` and
`run.sh ownership` first run `chore test:contract-report-targets`, which tops up
exactly what that phase consumes, and leaves none behind. No gate calls it, and
it runs after the gate, so examples and coverage see the rows they always saw:
coverage's skipped-probe ratchet depends on the single rows being spent.

### The routes the mail gateways call

`checkMailboxAddress` and `deliverMessage` are served only on the API's internal
listener, which requires the gateway's client certificate. `vertrag.yml`'s
`endpoints` sends them there in the primary run, with the certificate
`chore test:contract-mtls` copies out of the SMTP gateway. A `CONTRACT_ENDPOINT`
naming another server sends them to that server instead.

### Operations that take a two-factor session

The grant operations (`createGrant`, `deleteGrant`, `activateGrant`,
`deactivateGrant`, `makeGrantEligible`), the API key operations
(`listAPIKeys`, `createAPIKey`, `updateAPIKey`, `revokeAPIKey`) and `confirmTwoFactor` are in
the primary run (#1334). A request carrying the fixture admin's session is sent
with the two-factor administrator's instead (`mfa_admin.py`: a superadmin
enrolled in TOTP and signed in with a real second factor; nothing is forged).
Requests with no credential, and the authorization phase's other principals,
keep theirs. `createAPIKey` is made with that administrator's password, except in
the coverage process ([A hook cannot change a generated body](#a-hook-cannot-change-a-generated-body)).
`deleteGrant`'s example names grant 1, a fixture principal's own, so the hook
makes an eligible grant on an account without credentials and deletes that.
`recoverAccount` is sent for the fixture admin, which has no authenticator, so
it can never succeed; both are paced like the other sign-ins.

Both new operations document internal 500 errors. Each 500's pair says it
requires a database fault, which the healthy topology cannot set up, so the run
does not send it and the in-process example tests prove it. Existing
coverage-baseline entries and existing refusal selection are unchanged.

`TestOpenAPI_AdminAndTwoFactorRouteInventory` walks the real Chi registration
table and compares every admin route (including the guarded test routes) and
every own-2FA route to OpenAPI in both directions. It also holds own-2FA to
Cookie/Bearer sessions with no Basic/API-key alternative. The fast Python
contract checks independently scan the literal registrations, check references,
response fields, auth selection and exclusions. Handler tests compare the
response schemas to the actual Go JSON fields and exercise off/pending/active
status without exposing another account's state or any setup secret.

These checks detect missing routes and contract drift. Full mobile parity also
needs request/response field, permission, UI and live integration evidence for
each operation; route counts alone do not establish that evidence.

The four API-key operations also run in a required check,
`chore test:contract-api-keys`, inside `chore contract` before the examples gate. It creates a fresh
disposable `restmail.test` account with a domain-scoped admin grant, enrolls
2FA through the public API, confirms a real TOTP and signs in with a recovery
code. The suite checks create/list/edit/revoke, metadata without secrets, password
reauthentication, validation, refusals without MFA, scope restrictions and
immediate revocation. The primary run sends them too, as the two-factor administrator (above).
The dedicated step
has no `continue-on-error`, changes no primary principal, and adds no coverage
baseline entries.

Two gates judge the primary run:

- **examples, in vertrag:** `vertrag.yml`'s surface gate (`surface: {count:
  passed, require: 100}`) counts an operation only when every documented
  transaction of it was sent and passed, none failed, none skipped (because a
  hook skipped it or the link it follows did not resolve), none answered 5xx,
  and requires every operation it does not exclude. A shortfall exits 3, a
  failed transaction 1, and the contract step fails on either. Each exclusion
  is printed with its reason: the two operations that wipe the run's data, which
  `exclude-operation-id` leaves to the teardown phase. No status is excluded:
  `status-baseline.txt`, which `run.sh` adds to the run's `surface.exclude`
  and `status_baseline.py` holds to only shrink, is empty (#1334).
- **coverage, a ratchet, in `gate.py`:** `coverage-baseline.txt` lists the
  operations whose boundary probes still fail (#388). A failing probe on any
  other operation fails the job, and so does an operation in the baseline
  whose probes all pass now. A fix takes its operation out of the baseline in
  the same change, so the baseline only shrinks after its initial seeding in
  #376. `gate.py` also fails when `exclude-operation-id` and
  `surface.exclude` disagree about which operations are left out, or name one
  the spec does not have.

The documented error responses other than 401 are excluded by pattern in
`vertrag.yml`: 403 needs a principal without the capability (the
authorization phase, below), and 400, 404, 409 and 422 each need a request
of their own that provokes them (#386), except where the spec pairs one
with its request (below).

That request is a named request example, and the response it provokes carries
an example of the same name: vertrag pairs a request only with the response
example named like it, and a side naming no example pairs with everything.
`createDomain` is the first: its `name_missing` request (`{}`) goes with its
422, and `created` with its 201. `createAlias` is the second: its
`source_missing` request (a destination address and no source) goes with its
422, and `created` with its 201, whose links the rest of the alias chain
follows; its `body_array` request (`[]`, JSON but not an object) goes with its
422, the `validation_failed` naming the root `body`; its
`source_invalid` request (the `created` one with a source that is not an
address) goes with its 422 naming `source_address`
(`contract_alias_examples_test.go`). `createPipeline` is the third: its
`direction_missing` request (the `created` one without its direction) goes with
its 422, and `created` with its 201; the hooks give the refused request the
created domain but no direction. Its `body_array` request (`[]`) goes with its
422, the `validation_failed` naming the root `body`, and is
sent as it is: the hooks have no object to put the domain in.
`internal/api/handlers/contract_examples_test.go` (and
`contract_pipeline_examples_test.go`, for `createPipeline`) sends each request
named on any of these 422s to the real handler, which has no database, and
checks it gets the documented body: the refusal comes before anything is looked
up or stored. `contract_pipeline_body400_test.go` does the same for
`body_array` and its 422.

`updatePipeline` pairs `updated` with its 200, and `filters_object` and
`filters_null` with their 422s. The existing create link supplies the stored
pipeline ID, and no hook rewrites the PATCH body. The handler test sends each
spec request to a stored pipeline, checks the exact refusal and unchanged row,
and verifies the successful update's response and persistence
(`contract_pipeline_update_examples_test.go`). Only this path's 422 exclusion
is removed; pipeline logs and analytics keep theirs.

The Sieve operations `putSieveScript` and `validateSieveScript` send
`script_missing` and `script_null` requests with matching 422 examples.
Their successful responses are named `valid`; PUT keeps its 201-before-200
order to create the script before replacing it. The account link supplies
the authorized account ID. Handler tests send the spec examples against a
database and check the exact refusals and unchanged stored script
(`contract_sieve_examples_test.go`).

`createDomain`'s 422 is paired the same way: its `body_array` request (`[]`,
JSON but not an object) goes with `validation_failed` naming `body`
(`contract_domain_body_examples_test.go`, which sends it to the real handler
with no database).

`CreateDomainRequest.sending_policy` uses an inline enum: vertrag 0.29 drops
all body probes when the request schema contains a nested `$ref`. The schema
test holds the inline values to `DomainSendingPolicy` and the real handler's
validation. `createDomain` stays in the coverage baseline: hostname constraints
and fixture state for repeated creates still need work.

`createBan` (`ip_missing`, `duration_invalid`), `createCustomFilter`
(`filter_type_invalid`), `validateCustomFilter` (`body_array` with its 422,
`script_missing` with its 422) and `sendTestEmail` (`from_missing`) are paired
the same way, each checked against the real handler with no database
(`contract_ban_examples_test.go`, `contract_customfilter_examples_test.go`,
`contract_validatejs_examples_test.go`, `contract_validatejs_body400_test.go`,
`contract_testsend_examples_test.go`).

`createAccount` (`address_missing`), `restmailDeliver` (`recipients_missing`),
`addToAllowlist` and `addToBlocklist` (`pattern_missing`) now send their paired
422s too. `bulkDeleteQueue` sends its `selection_missing` request (`{}`) with
its paired 422 and its `invalid_body` request (`[]`) with its paired 422. Their existing handler tests check these refusals before any
database access (`contract_account_examples_test.go`,
`contract_restmail_examples_test.go`, `contract_allowlist_examples_test.go`,
`contract_blocklist_examples_test.go`, `contract_bulkdelete_selection400_test.go`).
No before hook rewrites these requests. Sibling operations remain excluded.

`importContacts` pairs `body_array` with its 422 and `contacts_missing`,
`contacts_empty`, `email_missing`, `email_invalid` and `mixed_batch` with its
422. Its successful request and 200 are named `imported`. The handler tests
send every documented pair, check exact responses, and prove that refusals
preserve all contacts both before and after a successful import, including a
batch whose first entry is valid (`contract_contact_import_examples_test.go`).
No hook rewrites these requests; other account operations retain their exclusions.

`linkAccount` pairs `linked` with its 201, and `address_missing`,
`password_missing`, `address_blank` and `password_empty` with its 422. The
handler reports both required fields for each refusal, before account lookup
or grant creation (`contract_link_account_examples_test.go`). Its hook supplies
only the password placeholder; missing fields, empty values and generated
coverage mutations stay intact (`test_link_account_hook.py`). Its 409 remains
excluded until an ordered repeated link provokes it.
Just before its first generated link proof, the hook runs
`chore test:contract-link-fixture` to seed `someone@example.com` with password
`x`, matching vertrag v0.29's valid schema specimens. This happens after the
account-creation probes, which need that address absent. Before a valid fixture
proof, the hook removes only the caller's previous link, so repeated probes
start unlinked. It preserves the generated request and its principal; invalid
credentials still reach the handler unchanged. A caller the route turns away for
its credential (an app password's 401, a password-only session's 403
`two_factor_required`) can have no previous link, so its refused clean-up is not
an error and its request is sent. Any other refused clean-up stays an error.

`bulkRetryQueue` and `bulkBounceQueue` send their `invalid_body` (`[]`) request
with its paired 422 and their `selection_missing` (`{}`) request with its paired
422: the request type's AnyOf refuses a body selecting neither nonempty ids nor
a filter at binding. Their original
request is named `by_ids`, with a matching 200 example, so neither refusal is
paired with success. The 422s no longer carry a `too_many_ids` example: a
request of 1001 ids is not worth a place in the spec, and the handler tests
still check that refusal (`contract_queue_bulk_pairs_test.go`).

`receiveTlsReport` sends its `organization_missing` request with its paired
422 and its `body_array` request (`[]`) with its paired 422, the `validation_failed`
naming the root `body`. Both are refused
before any domain is looked up or report stored
(`contract_tlsrpt_examples_test.go`). The other `/.well-known` paths keep
their exclusions.

`microsoftAutodiscover` pairs its `malformed` XML request with its JSON
400 (`bad_request`, message `invalid request`), and `valid` with its XML 200. Vertrag v0.29.0
pairs XML examples by name but does not serialize their request bodies, so
the hook supplies the matching XML for each status. The hook test checks both
bodies against the spec; `contract_autodiscover_body400_test.go` sends the
spec's malformed request to the real handler with no database. The 200's
named example declares no fixed response body: the XML contains the requested
mailbox's settings. This route's 400 is now sent and judged.

`updateMailbox` sends its `password_short` request (a five-character
password) with its paired 422 naming `password`, and `updated` with its 200,
both to the account `createAccount`'s link names. The refusal comes before any
password is hashed or stored: the account keeps its fields, credential epoch
and password (`contract_mailbox_update_examples_test.go`, which sends both
requests to the real handler on a scratch database). The other
`/api/v1/admin/mailboxes` refusals (`listMailboxes`' 422) keep their
exclusion.

`probeServices` sends its `service_unknown` request (`ftp`, a service it
cannot probe) with its paired 422 naming `services`, and `probed` (only the
database) with its 200. The refusal comes before any service is reached
(`contract_probe_examples_test.go`, which sends both requests to the real
handler). The other `/api/v1/admin/test` refusals (`verifyDelivery`'s 422)
keep their exclusion.

`setMtaStsPolicy` sends its `mode_invalid` request (mode `strict`) and its
`max_age_negative` request (`max_age` -1), each with its paired 422 naming the
field, and `policy` with its 201. Both are refused before the domain is looked
up (`contract_mtasts_examples_test.go`, which sends each to the real handler
with no database). The other `/api/v1/admin/domains/{id}` subpaths keep their
exclusions.

`testFilter` sends its `invalid_body` request (`[]`) with its paired 422 and
`filter_name_missing` with its paired 422. Both are refused before any filter
is made; `valid` still runs `header_validate` and gets the documented 200
(`contract_testfilter_examples_test.go`). No hook rewrites these requests.

`testPipeline` sends its `invalid_body` request (`[]`) with its paired 422 and
`email_missing` (a pipeline id and no email) with its paired 422. Both are
refused before any pipeline is looked up; the hooks give only `valid` the
created pipeline's id, and it gets the documented 200
(`contract_testpipeline_examples_test.go`). Other pipeline operations keep
their exclusions.

`testCustomFilter` sends its `invalid_body` request (`[]`) with its paired
422, the `validation_failed` naming the root `body`,
before the filter is looked up. Its `valid` request (`{}`, the built-in sample
email) runs a filter made for that exchange and gets the 200
(`contract_testcustomfilter_examples_test.go`).

The preview hooks pace `testPipeline`, `testFilter` and `testCustomFilter`
without rewriting requests or expectations, so the shared 2 requests/second
preview bucket does not turn valid contract probes into 429s. The healthy
topology cannot provoke the pipeline 500s or the custom-filter sidecar's
408/413/500 responses. The 413 and 500 pairs say what they require (an
oversized body, a database fault or a cancelled request), so the run does not
send them and the in-process example tests prove them. The 408 has no such
pair: that one transaction is excluded with a matching surface reason, its
transport regression tests cover it, and #1166 retains topology fault-injection
acceptance. A 5xx returned for a normal success or client-refusal transaction
still fails the gate.

`updateCustomFilter` pairs `updated` with 200 and five invalid requests with
422: `name_empty`, `filter_type_invalid`, `direction_invalid`, `config_array`
and `config_null`. The existing create link supplies the stored filter's ID;
no hook rewrites these requests. The handler tests send each spec request,
compare the exact refusal and verify the stored filter is unchanged, then
check the successful update (`contract_customfilter_update_examples_test.go`).
The child path's 400 and 404 remain excluded.

`createCertificate` sends `invalid_body` (`[]`) and `fields_missing` (`{}`)
with paired 400 and 422 responses respectively, both refused before the
domain lookup. Its `valid`
request pairs only with 201; the hook supplies a fresh certificate and key on
the created domain, leaving malformed and incomplete requests unchanged
(`contract_certificate_examples_test.go`, `test_contract_certificate_hook.py`).

`registerPushDevice` names its `registered` request with its 201, and
`platform_unsupported`, `token_invalid`, `environment_invalid` and
`app_id_mismatch` (each `registered` with that one field wrong) with their
paired 422s. Its 200 is named `already_registered` so it pairs with no
request. The real handler refuses each for a password session before anything
is looked up or stored (`contract_device_examples_test.go`). The devices
routes stay excluded in the E2E run: it has no APNs credentials, so they
answer 404.
Certificate field validation and invalid PEM return 422 with details.fields;
Malformed JSON or a wrong media type returns 400; parsed non-object bodies return 422.

`sendMessage` sends `from_missing` (`valid` without its from) with its paired
422, refused before the sender is looked up, and `unsubscribe_not_allowed`
(`valid` with a `list_unsubscribe`) with its paired 422. That 422 needs a
transactional sender domain, so the `sendMessage` hook makes restmail.test
transactional through the admin API for that request and the probes built
from it, and takes the policy off again for every other send and after the
refusal (`contract_sendmessage_examples_test.go`). The other
`/api/v1/messages` operations keep their exclusions.

`createContact` names its `created` request with its 201, and
`email_missing`, `email_invalid` (a display name and address) and
`trust_level_invalid` with their paired 422s. The refusals use another address
than `created`'s, because the `createContact` hook removes a contact an earlier
request made at the same address, and `created`'s links still need it. The
real handler refuses each for the account's own session and stores nothing
(`contract_contact_examples_test.go`). The other `/api/v1/accounts/{id}`
operations keep their exclusions.

`protocolSignIn` uses the E2E fixture's app password with its named requests:
`signed_in` (`{ "protocol": "imap" }`) gets 200, `invalid_body` (`[]`)
gets 400, and `protocol_missing` (`{}`) gets 422. The hook supplies only
the Basic credential, so each request body reaches the handler unchanged.

`getDomain` also sends its 404. Its `missing` path-parameter example names
an unused positive ID in the disposable fixture (2147483647), and the 404
names the matching `domain_not_found` body. Since Vertrag 0.30 a path
parameter example a response is named after is a request of its own
(antimatter-studios/vertrag#85): `missing` is sent with 2147483647 against
the 404 alone, and the create link, which fills the 200's id with the new
domain's, leaves it as it is. The handler test
checks both lookups against PostgreSQL and verifies the existing domain
is unchanged. Other 404s keep their exclusions; `test_gate.py` holds
the enabled set to exactly `getDomain`, `getCertificate`, `getAlias`,
`getCustomFilter`, `getMailbox`, `getDKIMRotation`, `domainDNSCheck`,
`getMtaStsPolicy`, `getDomainRelay`, `getQueueEntry` and `getMessageTrace`.

`getCertificate` sends its documented 404 with the `missing` path parameter
example, paired with the 404 alone as `getDomain`'s is, the create link
filling only the 200's id. `contract_certificate_get_examples_test.go` checks
the exact refusal, successful detail response, and unchanged stored
certificate against PostgreSQL.

`getMailbox` sends its documented 404 with the `missing` parameter example,
2147483647, and the exact `mailbox_not_found` response. The successful lookup
still follows the account created by the run; the account hook leaves the
missing ID unchanged. `contract_mailbox_get_examples_test.go` verifies both
responses against PostgreSQL and checks that neither lookup changes the account
or creates the missing one. Mailbox update, delete and session-ending 404s keep
their exclusions until each has its own request pair. This does not change the
resource authorization checks or the coverage baseline.

`getDKIMRotation`, `domainDNSCheck`, `getMtaStsPolicy` and `getDomainRelay`
also send their missing-domain 404s. Each names the same absent positive ID,
2147483647, and pairs it with its exact error body: `domain_not_found` for
DNS, `not_found` for the other three. Existing links supply only the successful
lookups. `contract_domain_lookup_examples_test.go` checks the registered routes
against PostgreSQL, both the refusal and a successful lookup of the created
domain, and verifies the domain stays unchanged. DNS uses deterministic test
resolvers. DKIM, MTA-STS and relay writes keep their 404 exclusions.

`getQueueEntry` and `getMessageTrace` send their named `missing` IDs,
2147483647, with the exact `not_found` responses: `Queue entry not found`
and `No pipeline trace for this message`. Existing links fill only the
successful lookups from the held queue fixture and delivered message. The
trace pacing hook leaves both path examples unchanged.
`contract_message_lookup_examples_test.go` checks complete success and refusal
bodies through the registered routes against PostgreSQL, preserves the stored
queue entry and trace, and verifies that the missing IDs remain absent. Queue
delete, retry and bounce 404s retain their exclusions; the coverage baseline
and resource authorization checks are unchanged.

`login` pairs `wrong_password` with 401 and `email_missing`,
`password_missing` and `client_invalid` with their 422s. Its 200 is named
`signed_in`. `refreshToken` pairs `invalid_token` and `token_missing` with
their 401s and `refreshed` with 200. The hooks replace only the explicit
`fixture-password` and `fixture-refresh-token` placeholders; wrong credentials,
missing fields and coverage mutations survive. Refusals keep the saved refresh
token intact. The real handler tests check the exact refusal bodies, that no
session is created or rotated by a refusal, and that the valid requests still
sign in and rotate (`contract_auth_examples_test.go`). `test_auth_hooks.py`
checks the hooks against the spec examples and the exclusions. Both operations'
401s are now counted by the surface gate.

Refresh success probes with a missing or empty body token exercise the browser
channel. Their before hook obtains a fresh `restmail_refresh` cookie for each
probe, because coverage does not run the after hook that records rotated tokens.
The generated body bytes and existing caller headers stay intact. Named 401
examples, malformed bodies and explicit caller cookies receive no setup cookie;
their refusals stay visible. Setup login failures stop the run.

`setVacation` pairs `configured` with 200, `body_array` with 422, and
`enabled_missing`, `subject_missing`, `body_missing`, `reply_missing` and
`dates_reversed` with their 422s. No hook changes these requests. The handler
checks each refusal after authorizing the account and before changing its
stored vacation setting; the tests send the spec examples to the real handler
and verify that refused updates preserve a previously configured reply.

`createAppPassword` sends `name_missing`, `name_empty`, `name_too_long` and
`name_control` with their paired 422s; `created` pairs only with 201. The
hook supplies the fixture admin's current password without changing the name.
The real handler tests check every pair on a managed account and verify that
refusals create no credential (`contract_credential_examples_test.go`). Other
account operations keep their exclusions.

`setDomainRelay` pairs `relay` and `direct` with 200; `body_array` and
`port_wrong_type` with 422; and missing/invalid transport, invalid host,
port, TLS mode, and mismatched credentials with 422. A createDomain link
supplies the domain id. Refusals leave both absent and saved relay settings
unchanged (`contract_relay_examples_test.go`). Missing-domain 404 and the
409 that needs a server without MASTER_KEY remain excluded.

`updateAlias` pairs its `destination_invalid` request (`not-an-email`) with
its 422 naming `destination_address`. Its valid `updated` request pairs only
with the 200. The alias ID comes from `createAlias`'s existing link. A handler
test sends the examples to a stored alias, checks the documented refusal body,
and verifies that the refusal leaves the stored alias unchanged.

`updateDomain` sends `sending_policy_invalid` (`newsletter`) with its paired
422 and `updated` (`transactional`) with its 200. The existing `createDomain`
link supplies the stored domain ID. The handler tests send the spec's requests,
check the exact refusal and unchanged stored domain, and verify that the success
persists its policy (`contract_domain_update_examples_test.go`). Its 404 and 409
remain excluded until they have requests of their own.

Mozilla discovery's generated email needs an active hosted domain. Its hooks
prepare that domain through the admin API, adding a self-signed fixture
certificate when activation requires one. The query and caller's credential
stay unchanged. Discovery therefore runs independently of `updateDomain`'s
example, which updates only the sending policy.

`setDKIMKey` pairs `installed` with 200 and `body_array` with 422. Missing,
empty or wrong-type selectors and private keys, `selector_invalid`, and
`private_key_invalid` pair with 422. The hook replaces only the explicit
`fixture-signing-key` placeholder, preserving malformed bodies, missing
fields and invalid selectors or keys. The existing createDomain link supplies
the domain ID. Refusals leave the stored domain unchanged
(`contract_dkim_key_examples_test.go`, `test_contract_dkim_hook.py`). DKIM
rotation writes retain their own 404 exclusion.

Generated DKIM requests hold a public test-only Ed25519 key through
`probes.py`'s `fuzz.pin.private_key`: schema generation cannot construct a signing key.
Selector and body probes still run, with nonempty fields and DNS selector
syntax described in the schema. Generated private-key mutations are held
fixed; the named examples above exercise their refusals instead, including
missing, empty, wrong-type and non-PEM keys. The placeholder hook leaves
generated request bytes unchanged, and no coverage baseline or accepted-status
exception is added.

vertrag 0.29 applies pins to named examples too. CI first runs the complete,
unpinned examples with the 100% surface gate. Then `probes.py` writes a separate
configuration with the test key and no 4xx source transactions, whose probes
the ratchet already ignores. It retains every success transaction and all
checks. This second run writes `coverage.xml`, judged by the unchanged ratchet;
the full example report remains `examples.xml`. The generated configuration
also serves the report-only fuzz run.

Before the second process repeats examples, `repeat_fixtures.py` removes only
the fixture admin's `Receipts` folder and `spam@example.org` contact, and
`chore test:contract-fixtures` supplies a new held queue entry. The first run's
complete example report is already enforced; probe requests stay unchanged.

`setDKIMRotation` pairs `configured` with 200 and `body_array` with 422.
`interval_wrong_type`, `field_unknown`, `interval_missing`, `interval_null`,
`interval_negative` and `interval_too_large` pair with 422 after typed binding.
The domain ID comes from `createDomain`'s link. The handler tests send each
refusal before and after saving a valid interval and verify the entire domain
is preserved, including its signing material. Other DKIM operations keep
their exclusions.

`createFolder` pairs `created` with 201 and `name_missing`, `name_empty`, and
`name_wrong_type` with 422. The real handler tests use an authorized account
on a scratch database and compare each example's status and full response
body. The folder hook leaves the invalid bodies unchanged and only removes
previously created valid probe folders.

Every documented status with an example is sent and judged (#1334). A
transaction sent and refused other than as documented fails the examples
phase like any other. `vertrag.yml` holds no status back: `test_gate.py`
(`NothingHeldBack`) fails on an `exclude-matching` pattern, a status in
`surface.exclude`, or a line in `status-baseline.txt`.

The coverage phase probes every transaction sent, and judges valid-mode probes
whenever the transaction's own request gets the status it documents. For a
refusal that request is the refused one, so every valid probe built from it
(`{}` with a `server_type`, a destination address with no source, or a
pipeline with no direction, say) is refused as well, and vertrag reports each
as a failed probe. `gate.py` does not judge a probe built from a refusal (a
transaction whose status is 4xx), so those failures do not count against the
coverage ratchet (`test_gate.py`).

`saveDraft` includes an `empty` request (`{}`) as well as `with_sender`.
Every draft field is optional: omitting `from` saves in the signed-in
account's mailbox with an empty sender. An explicit sender still needs
mail:write on that mailbox (`contract_draft_test.go`). The operation remains
in the coverage baseline because generated sender addresses can name
mailboxes the principal cannot write to.

Vertrag is a released binary selected by the checksum pin in
`.github/workflows/e2e-suite.yml`; it is installed independently of this
module's Go dependencies.

## Fault catalogue (#654)

A green contract run only means something if a broken server turns it red.
`faults.yml` lists faults the run must catch. Each entry names the fault, the
Go build tags that apply it, the operation it breaks and the phase expected to
catch it. Its switch is `internal/faultinject/fault_<name>.go`, a file built
only with the tags `faultinject` and `fault_<name>`. The whole package and
`cmd/api/faults.go` need the `faultinject` tag, so a normal build holds no fault
code; the normal build's `injectFaults` (`cmd/api/nofaults.go`) returns the
router as it is.

`chore contract:faults` runs it against a running E2E topology, and the
nightly `Contract faults` workflow (`contract-faults.yml`) runs it after the
stages pass. For each operation in the catalogue it first runs the phase,
narrowed to that operation, on the normal API, which must be green. Then, for
each fault, it restarts the API built with the fault's tags (`GOFLAGS=-tags=`,
through `RESTMAIL_API_BUILD_TAGS`), checks that the API logged `fault injection
active` for it, and runs the phase again. The fault is caught only if that
phase's report fails a transaction of the operation's path for its expected
reason (the limit phases require a typed missing-guard failure). A phase that never
ran, or a failure elsewhere, does not count. A fault with `known_gap` names the
issue saying why the run cannot catch it yet: it passes uncaught and fails once
caught, so the entry goes when the gap closes. The normal API is restored at the end.
`.cache/contract/faults.md` has the verdicts, and `faults-<name>-<phase>.xml`
each run's report.

The shape and status faults are caught by `examples`:

| Fault | What a client sees | Vertrag's failure |
|---|---|---|
| `discover_endpoint_renamed` | Discovery's required `endpoint` field sent as `url` | |
| `discover_wrong_media_type` | Discovery's JSON answered as `text/plain` | |
| `health_data_as_list` | Health's `data` object answered as a list | |
| `health_storage_ref_leaked` | Health's `data` carries a `storage_ref` its type does not declare | `body: /data: additional properties 'storage_ref' not allowed` |
| `accounts_list_null` | The account list's `data` answered as `null` | `body: /data: got null, want array` |
| `health_status_created` | Health answers 201 instead of 200 | |
| `discover_missing_status_ok` | An unhosted domain's 404 refusal answered with 200 | `statusCode: Expected status code '404', but got '200'.` |
| `login_validation_removed` | A login validation refusal answered with 200 instead of 422 | |

The status switches keep the response body, so the status check must notice.
An entry's `expected_failure` holds Vertrag's failure text. With it, a red run
for any other reason is not a caught fault. An undeclared field is only caught
where the served type is closed (`closed-schemas.md`), which health's `data` is.

The limit switches disable the real API body cap or shared authentication
throttle before constructing the router; login is their witness operation,
not their only affected route. The `body_cap` and `auth_throttle` phases send
narrow login probes directly, using `CONTRACT_ENDPOINT` or `vertrag.yml`'s
endpoint. This is smaller than invoking the independent E2E limits suite:
it needs no mailbox/MFA provisioning and can distinguish missing guards from
transport, setup, or unrelated route failures. No workflow dependency changes
are needed. Authorization and ownership remain report-only.

Each limit baseline restarts the normal API to give it fresh buckets. The body
probe streams a space-padded `{}` with a 32 MiB Content-Length (above the
topology's default 21 MiB cap): the real middleware must refuse it with 413
`request_too_large`. It may reject the declaration before the upload completes.
A 422 `validation_failed` instead proves the body reached validation without
that guard; a timeout or any other answer is an error, not a caught fault.
This checks the declared-length guard, not the separate streaming cap for
requests without a Content-Length. A topology configured with a larger cap
fails the baseline rather than silently claiming proof.
The throttle probe requires ordinary 401 `unauthorized` refusals followed by
429 `rate_limited` with a positive Retry-After; only 200 consecutive ordinary
refusals prove the throttle missing. Neither phase claims to send Vertrag's
unproven 413/429 examples.

Both faults were proven live by **Contract faults** run
[41537491645](https://github.com/rest-mail/rest-mail-server/actions/runs/41537491645):
the normal-API baselines were green and both fault reports held the expected
missing-guard failures. They no longer carry `known_gap`, so the workflow now
requires both to be caught. Local mocked HTTP tests and builds alone do not
justify removing a `known_gap` marker: a caught fault with one deliberately
fails with **now caught**, which is the signal to review the proof.

A secret in a free string fits its schema, so the `examples` phase passes it. The
`secret_scan` phase is what catches `change_password_refusal_echoes_password`, a
wrong current password's 403 refusal repeating that password in `error.message`.
It runs the same narrowed gate, then gives the recording (`examples.har`) to
`secret_scan.py`, as `chore contract` does for the whole run. The fault is caught
only by a finding on its own route, for its expected reason. A finding on another
route, a missing recording, or a route the recording shows no secret sent to is
an error, not a verdict. It reads the recording only, not the audit log, so it
needs no audit reader's sign-in; the whole run's scan keeps reading both.

The witness has to send a value the recording keeps. Vertrag redacts the values
of its own login body, so login cannot be it: the only password login's examples
record is the fixture admin's. Any operation sending another secret can be, and
`faults-<name>-secret_scan.xml` says how many secrets were sent to the route.

Authorization and ownership faults still wait until those phases stop being
report-only.

### Adding a fault

1. Write `internal/faultinject/fault_<name>.go`, starting
   `//go:build faultinject && fault_<name>`. Its `init` registers a `Fault`:
   the route's method and path, a `Break` that rewrites the recorded answer
   (`renameField`, `replaceField`, `addDataField` and `setMediaType` are in
   `faultinject.go`), and an `Example` of the route's normal answer for the
   package test. Set `ExampleStatus` when that answer is not 200. A fault that
   repeats what the client sent uses `BreakSeeing`, which is given the request
   body as well, with `ExampleRequest` for the package test. A switch
   disabling a real guard uses `Configure` instead of `Break`; only tagged
   builds apply it to the configuration before constructing the router. The
   switch check exercises the configured limit middleware as well as the
   response-rewriting faults. A normal build's configuration hook is a no-op.
2. Add its entry to `faults.yml`: `name`, `build_tags`
   (`faultinject,fault_<name>`), `operation`, `path`, `phase` and `breaks`,
   and `expected_failure` once you have seen the failure the phase reports.

`go test ./internal/faultinject` (no tags) holds the catalogue and the
switches to each other, and requires the `faultinject` tag on every source
file in the package. CI's `Test the contract fault switch` step builds the API
with every fault's tags and checks that each one changes its route's answer.
To try one by hand: `chore api:up RESTMAIL_API_BUILD_TAGS=faultinject,fault_<name>
RESTMAIL_INTERNAL_MTLS=true`, then `tests/contract/run.sh gate --operation-id
<operation>`.

## Files

- `../../vertrag.yml`: the run: endpoint, sign-in, exclusions, and the
  surface gate with each exclusion's reason.
- `hooks.py`: only what the spec cannot hold: the fixture's passwords, the
  refresh token a sign-in hands out, a fresh key pair, the created domain's id
  where a request carries it in its body, and its name in the Host header the
  MTA-STS policy file is served by, cleanup of folders previously created by
  create probes (so repeated valid names do not conflict), and a fresh source
  folder for each rename probe (a successful rename consumes the previous one),
  and, for `addToAllowlist`, `addToBlocklist` and `createAlias`, the entry a
  previous probe added removed when generated bodies repeat it (the domain
  stays for the whole run, so a repeat is otherwise a 409), with an alias
  probe's domain hosted first. `removeFromAllowlist` and
  `removeFromBlocklist` keep a linked id the list still holds, and otherwise
  get an entry of their own (`on_a_sender_rule_of_its_own`). `getAlias`, `updateAlias` and
  `deleteAlias` keep a linked id that still has an alias, and otherwise get
  an alias of their own (`on_an_alias_of_its_own`).
  TLS-RPT reports are sent a second apart: the route is rate-limited. Their
  schema requires a policy domain and caps the array at 100 policies, matching
  the storage limit. The hook hosts generated policy domains before sending
  each report, leaving its generated request unchanged.
  The valid relay transport enum probe gets the host, port and TLS mode that
  its `if/then` branch requires; vertrag v0.29 does not generate those
  conditional companions. Other relay probes and documented refusals keep
  their bodies.
a hosted domain and mailbox for each address a test send probe uses, and,
  for `createAccount`'s probes, their address's domain hosted and the
  account a previous probe created removed (a generated address such as
  `someone@example.com` is otherwise refused as on a domain this server does
  not host, or as existing). A probe repeating the example's address removes
  the account `getMailbox`, `updateMailbox` and `deleteMailbox` are linked to,
  so those follow it to its current id, recreating it if a refused probe left
  it removed.
  Folder create probes keep their generated bodies and account IDs unchanged.
  Folder and contact setup signs in as the fixture admin with a separate bearer
  session, cached per endpoint. It leaves the request's own credentials intact,
  including when the principal has no credentials; Basic auth is reserved for
  app passwords, not the fixture admin's account password.
  Login, refresh, account linking, app-password creation and protocol sign-in
  share the API's default auth bucket (1 request/second, burst 15). Their before
  hooks wait 1.1 seconds per request, including coverage probes and setup
  logins, so a dense run cannot turn valid probes into 429s. The limiter stays
  enabled; requests and the coverage baseline stay unchanged.
  Every other id comes from the spec's `links`.
  `deleteDomain` deletes only the domain the run created: a probe naming any
  other domain (coverage's in-range id 1 is the fixture admin's) is skipped,
  since the server refuses a domain that still has accounts with 409
  (`test_hooks_delete_domain.py`).
- `gate.py`: the contract coverage gate. `gate.py --failing-probes openapi.yaml <report>` prints
  the operations with a failing probe, in the baseline's form.
- `probes.py`: prepares the coverage/fuzz configuration after the full examples
  gate, holding only the public `fixtures/dkim-key.pem` signing key and a snooze
  date in the future. Its tests
  keep pins out of the examples gate and preserve all success probes.
- `authorization.py`: builds a private configuration from the E2E principals
  for the report-only authorization phase. Its JUnit report and log are
  included in `e2e-container-logs`; the generated credential-bearing YAML is
  removed after the run and is never uploaded.
- `ownership.py`: builds the private configuration of the report-only
  ownership phase (#657) from the same principals, as two account tenants.
  Its reports are uploaded like the authorization phase's; its generated YAML
  holds the app passwords and is never uploaded.
- `secret_scan.py`: the secret scan (#673, continued under #1165/#658). The
  examples phase records traffic as `.cache/contract/examples.har` (Vertrag's
  `har` reporter). The scan collects JSON passwords/private keys/recovery codes
  and refresh tokens, Basic/Bearer authorization, and the server's access/refresh
  cookies from headers or HAR cookie arrays. It also collects credentials issued
  in successful JSON or session-cookie responses, allowing their creation answer.
  It fails when a collected value appears in another response body, header or
  cookie, or in the audit log read through the API after the run. JSON strings
  and keys, base64 HAR bodies, Basic credentials, cookie quoting and redirect URL
  escaping are decoded before matching. Push device tokens and CSRF/preferences
  are not collected as credentials; push responses are still scanned for known
  credentials. API-document example bodies are neither issuance nor leak evidence;
  their response headers and cookies remain checked. Findings identify the route,
  transport and JSON path while redacting all collected values, including prefixes
  and metadata, and omit URL userinfo/query/fragment values.
  Vertrag redacts its own login values as `<redacted>`, which cannot distinguish
  the fixture email from a credential and is not judged. The configured fixture
  password is searched separately in the unredacted audit log. This limitation
  remains; a redacted recording does not prove that password was never answered.
  Audit retrieval refuses login and page redirects, checks each page's data and
  explicit pagination, URL-encodes cursors and rejects cycles or missing cursors.
  It scans credentials newly issued by its own login, including session cookies.
  Safety limits are 1,000 pages, 16 MiB per HTTP answer and 64 MiB of serialized
  audit entries. Exceeding a limit, malformed input or a failed read exits 2 with
  a sanitized error and no clean verdict; a finding exits 1 and a verified clean
  run exits 0. Controls are in `test_secret_scan.py`, `test_secret_transports.py`
  and `test_secret_audit_read.py`. The HAR contains fixture credentials and is
  not uploaded with the logs.
- `coverage-baseline.txt`: the coverage ratchet's baseline.
- `test_gate.py`: `gate.py`'s own tests (`python3 -m unittest discover -s tests/contract -p test_gate.py`).
- `test_hooks_save_draft.py`: the saveDraft sender hook's tests, against a stub admin API.
- `test_repeat_probe_hooks.py`: the sender rule and alias hooks' tests, against
  a stub admin API (`python3 -m unittest discover -s tests/contract -p test_repeat_probe_hooks.py`).
- `test_hooks.py`: the `createAccount` hooks' tests, against a stub admin API
  (`python3 -m unittest discover -s tests/contract -p test_hooks.py`).
- `test_hooks_send_message.py`: the sendMessage sender hook's tests, against a stub admin API.
- `test_hooks_update_domain.py`: the updateDomain certificate hook's tests, against a stub admin API.
- `test_hooks_test_send.py`: the sendTestEmail hook's tests, against a stub admin API.
- `fixtures/`: prepares a deferred queue entry on the disposable `restmail.test`
  topology before the contract run. Its next attempt is held for 24 hours so
  the background worker cannot deliver it before the retry example. This does
  not change the product's retry rules or any coverage exclusion.
- Scheduled-message hooks prepare their own messages through the send API.
  If send-body probes exhaust the admin's submission budget, fixture setup
  retries once after `Retry-After` (at most 60 seconds). The actual contract
  requests are never retried, so their rate-limit responses remain visible.
  The hook worker deadline allows for that wait and the setup API requests.
- `../e2e/contract_principals_test.go`: the principals, written to
  `.cache/contract-principals.json`.

## Reset and seed helper (Part of #1385)

`reset_seed.py` exposes `reset_and_seed(origin, admin_token,
api_container="...")` for later hook migrations. It registers no hooks and
does not change the current sequence. Call it serially, only on the disposable
`restmail.test` instance, with its surviving fixture administrator's JWT and
the container serving that origin. It calls `resetTestData`, then the
idempotent `seedTestData`, then the existing `fixtures/` Go command in that
container. The default `fixtures=("queue",)` recreates deferred retry/bounce
entries, held quarantine mail and a suppression. Optional modes are
`link-account`, `domain-tenants`, `mfa-admin` and `api-keys`, matching that
command's existing flags; the command reads the shared `fixtureplan`.
The result contains per-step/total seconds and each fixture command's output.
HTTP errors (including 429), unexpected status envelopes, fixture exits and
timeouts raise errors, never skips. After failure, do not send the exchange.

To measure repeated resets on the running topology, supply its admin JWT in
`CONTRACT_RESET_TOKEN` (not a command-line argument), then run:

```sh
python3 tests/contract/reset_seed.py --origin http://localhost:8080 \
  --api-container my-disposable-api --runs 7
```

This reports every sample, median/worst and their incremental cost across 600
exchanges, including both API calls and `docker exec`/`go run` fixture startup.
Image builds and initial server startup are outside those timings.

Initialize the reset **before creating test principals**. Its first successful
call pins the existing installation administrators in `test_reset_baseline`.
Administrators created afterward (including the MFA fixture) are ordinary test
data and are removed on the next reset. Initial administrator accounts, their
domains, credentials and own grants survive, as do the server capability ceiling,
system/retained roles and their capability definitions, the installed capability
catalog, installation owner recovery keys and the append-only audit log. These
are the complete retained-table allow-list; the implementation records a reason
for each in `resetRetainedTables`. Every other table, including suppressions,
refresh-token ledgers and newly migrated tables, is emptied. Identity sequences
restart transactionally above the retained rows before seeding.

For the same fixture selection and command order, IDs are stable across resets.
The API-key fixture uses a stable address after reset initialization; its legacy
standalone mode still creates a fresh account. A JWT issued after initialization
keeps naming the same recreated account if its credential epoch matches the
default fixture epoch. The first normalization may change IDs allocated by older
seed commands; discard pre-initialization tokens and cached principal files.
Reauthenticate after changing fixture selections or credential epochs, and never
reuse refresh tokens across resets. MFA enrollment and nondefault passwords on
ordinary fixtures are deliberately removed, so later hooks must recreate them.

Reset refills all API token buckets and recovery budgets, invalidates authorization
and session caches, closes old SSE subscriptions and clears stream caps, counters
and replay. The non-production delivery worker holds a cross-process database
barrier from before claiming mail through all delivery/DSN/suppression effects.
Reset waits for that barrier with a **30-second bound**, excludes new claims while
cleaning, and fails rather than reporting success when the drain times out. A
failed SQL reset rolls back fixture rows and sequence restarts and does not clear
memory. Run API and gateway with the same non-production environment; production
has neither reset routes nor worker-barrier behavior. Exchanges and fixture
subprocesses must still be serialized; this is not a concurrency-isolation API.

`fixtures=()` runs no fixture command, and needs no container: only the seed's data.

## Moved to a clean start (Part of #1385)

`getDomain`, `setMtaStsPolicy`, `getMtaStsPolicy` and `deleteMtaStsPolicy` reset and
seed before each exchange, refusals included, and each success makes the domain it
needs, and for a read or delete the policy too. Only the path changes, so bodies and
credentials reach the handler as generated. The absent-policy 404 keeps its own hook
(`policy_lookup_hooks.py`), which runs after the reset.

`createPipeline`, `testPipeline` and `deletePipeline` reset and seed before each
exchange too. A create gets a domain of its own; a preview and a delete each get
a domain and an inactive pipeline of their own, and refusals keep their ids. No
pipeline id is carried between exchanges any more, so `hooks.stash` holds no
`pipeline_id`: without a reset, each preview makes its own pipeline on the run's
domain. `updatePipeline` follows `createPipeline`'s link and has not moved. After
a reset, an update to a positive id other than missing's gets a pipeline that
exists, on the domain that stands in for the created one
(`a_live_pipeline_for_the_link`); a run with no reset changes nothing there.

`createAlias`, `getAlias`, `updateAlias` and `deleteAlias` reset and seed before
each exchange too, refusals included. A create is sent as documented after its
reset: no earlier probe's alias is left, and the seeded `restmail.test` hosts
`sales@restmail.test`. A read, update or delete gets an alias of its own, on a
domain of its own (`contract-own-alias@contract-own-domain.test`). Missing's
`2147483647`, a non-positive or undecodable id and every documented 404 keep
their ids. `createAlias`'s link reaches only those three, so nothing that follows
it is left unmoved. No alias is carried between exchanges, so `hooks.stash`
holds no `created_alias`: without a reset, an exchange whose positive id no
longer has an alias (the documented delete or a probe removed it) gets the one
at `contract-own-alias@restmail.test`, made if absent. `created_alias_probes`
stays, because a create without a reset (the primary run and coverage) still
removes only the repeated probe aliases it made.

`createCustomFilter`, `getCustomFilter`, `updateCustomFilter`, `testCustomFilter`
and `deleteCustomFilter` reset and seed before each exchange too, refusals
included. A create is sent as documented after its reset: no earlier exchange's
filter is left, and the documented create names no domain. A read, update,
preview or delete with a positive id gets a disabled filter of its own, made
after the reset, so a 422 is refused for its body and not for a filter the reset
removed. Missing's `2147483647`, a non-positive or undecodable id and every
documented 404 keep their ids. `createCustomFilter`'s link reaches the read,
update and preview, and the preview's reaches the delete, so nothing that
follows it is left unmoved. No custom filter is carried between exchanges, so
`hooks.stash` holds no `created_custom_filter_id`: without a reset, each success
example aimed at placeholder `1` gets a new filter of its own
(`on_a_custom_filter_of_its_own`), and other ids are sent as before.

`createFolder`, `renameFolder` and `deleteFolder` reset and seed before each
exchange too, refusals included. The reset removes the mailbox their examples
name (`1`), so each exchange with a positive account id is sent to a mailbox of
its own (`contract-own-folders@contract-own-domain.test`, made after the reset
with the two-factor session, since making a mailbox takes one). A create is then
sent as documented: no earlier exchange's folder is left to remove. A rename
gets a new source folder, as before. A delete documented to succeed gets a
folder of its own, since the folder the rename's link names is gone, and the
refusals keep their folders: `Missing` is refused as absent and `Archive` as a
system folder, in a mailbox that exists. A non-positive or undecodable account
id and missing's `2147483647` keep theirs. `createFolder`'s link reaches the
rename, and the rename's reaches the delete, so nothing that follows it is left
unmoved. Rename sources are named by the time they are made, not by a count, so
`hooks.stash` holds no `rename_probe`. `created_folders` stays, because a create
without a reset (the primary run and coverage) still removes only the repeated
probe folders it made.

`updateDomain` resets and seeds before each exchange too, refusals included. It
follows `createDomain`'s link, and the reset removes the domain the link names,
so an exchange aimed at a positive id other than missing's `2147483647` is sent
to a domain of its own (`contract-own-domain.test`), the 422s included, so they
are refused for their bodies and not for a domain that is gone. A non-positive
or undecodable id and missing's keep theirs. A request that sets `active` to
true gets a self-signed certificate for that domain first, made in the hook
(the documented examples set none). No link starts from `updateDomain`, so
nothing that follows it is left unmoved, and it no longer gets
`a_live_domain_for_the_link`'s stand-in. Whether a domain is certified is read
from the API (`listCertificates`), so `hooks.stash` holds no
`certified_domains`.

`inviteAccount` and `acceptInvitation` reset and seed before each exchange too,
refusals included, and each makes the account it invites after its reset, on the
seeded `restmail.test`, with the two-factor session (making an account takes
one). `created` invites an account made without a password. `existing_credentials`
named the fixture admin's id `1`, which the reset removes, so it gets an account
of its own made with a password, and is refused with 409 for that account's
credentials. `missing` keeps `2147483647`. `accepted` redeems a new invitation
for an account of its own, as before, and the refusals (`invalid_token`,
`invalid_field`, `body_array`) reach the handler unchanged. No link starts from
or reaches either operation. The accounts are named by the time they are made,
not by a count, so `hooks.stash` holds no `invitable_accounts`. `deleteGrant`'s
hook makes its grant's holder the same way, so it reads no stash value through
that helper any more.

Every grant operation of the area map (`createGrant`, `deleteGrant`,
`activateGrant`, `deactivateGrant`, `makeGrantEligible`, `listGrants` and
`listMyGrants`) resets and seeds before each exchange as well, refusals
included, and is sent as the two-factor administrator as before, whose session
survives the reset. `deleteGrant`'s `body_boundary` names grant `1`, a seeded
principal's own, so after its reset it makes an eligible admin grant on
`restmail.test`, held by an account made without a password, and deletes that.
The other examples need nothing made: their ids are the refusals' (`0` and
`2147483647`), and the lists read the seeded grants. No link starts from or
reaches any grant operation, and none of their hooks reads a stash value.

The domain sender rule operations (`listAllowlist`, `addToAllowlist`,
`removeFromAllowlist`, `listBlocklist`, `addToBlocklist` and
`removeFromBlocklist`) reset and seed before each exchange too, refusals
included. Each follows `createDomain`'s link, which the reset empties, so an
exchange aimed at a positive domain id other than missing's `2147483647` is sent
to a domain of its own (`contract-own-domain.test`), and they no longer get
`a_live_domain_for_the_link`'s stand-in. A list reads that domain's empty list,
and an add is sent there as documented, the 422 included. A remove documented to
succeed gets an entry of its own there (`@contract-own-rule.test`), since the
entry the add's link names is gone; the 404 and a non-positive or undecodable
entry id keep theirs, on a domain that exists. Each add's link reaches only the
remove of its list, so nothing that follows it is left unmoved. No entry is
carried between exchanges, so `hooks.stash` holds no `created_sender_rules`:
without a reset, a remove whose positive entry id the list no longer holds (the
documented remove, or a probe repeating the add's pattern, removed it, or
coverage's placeholder `1` never had one) gets the entry at
`@contract-own-rule.test` on that domain, made if absent; an id the list holds
is followed unchanged. `created_sender_rule_probes` stays, because an add
without a reset (the primary run and coverage) still removes only the repeated
probe entries it made.

Every contact operation of the area map (`createContact`, `updateContact`,
`deleteContact`, `listContacts`, `blockSender`, `importContacts`,
`suggestContacts`, `getContactReplyTimeSetting` and
`setContactReplyTimeSetting`) resets and seeds before each exchange too,
refusals included. Their examples name the fixture admin's mailbox `1`, which
the reset removes and seeds again under another id, so an exchange aimed at a
positive account id other than missing's `2147483647` is sent to the seeded
`admin@restmail.test`, found by its address (`to_the_seeded_account`). A list,
a suggestion, an import and the reply-time setting need nothing made there. A
create is sent as documented, and `blockSender`'s `created` and `found` get the
state they need, as before. An update or delete aimed at a positive contact id
gets a contact of its own there (`contract-own-contact@example.test`), since the
one `createContact`'s link names is gone; missing's `2147483647`, a non-positive
id and every documented 404 keep theirs. `createContact`'s link reaches only the
update and the delete. `listAccounts`' link reaches the others but the
reply-time setting, and is not followed here, because `listAccounts` has not
moved: each of those exchanges carries its own account id. No contact is
carried between exchanges. `created_contact_probes` stays, because a create
without a reset (the primary run and coverage) still removes only the repeated
probe contacts it made; the reset clears it, so it starts empty here.

The send operations, `sendMessage` and `sendTestEmail`, whose hooks keep
`sender_mailboxes` and `test_send_mailboxes`, reset and seed before each
exchange too, refusals included. The seed gives back `restmail.test`, its admin
and its postmaster with no sending policy, so after the reset `sendMessage`'s
hook sets the transactional policy `unsubscribe_not_allowed` needs and checks
the sender's signing key and mailbox again, making what is missing, and
`sendTestEmail`'s makes any account the seed did not give back. No link reaches either.
Each send exchange runs after its own reset, a handful per run, far below the
send rate limit (20 a minute per account). Both stash values stay, because
without a reset (the primary run and coverage) they keep each address's setup
to once per run; the reset clears them, so they start empty here.

The message operations `sendMessage`'s 201 link reaches (`getMessage`,
`updateMessage`, `getRawMessage`, `forwardMessage`, `listAttachments`,
`getMessageTrace`) and the three their links reach (`deleteMessage`,
`verifyMessageSeal`, `getThread`) reset and seed before each exchange too,
refusals included. The reset removes the message the link names, so an exchange
aimed at a positive message id gets a message of its own, made after the reset
(`on_a_message_of_its_own`): imported into the seeded `admin@restmail.test`,
looked up by address, or for a trace sent from that address to itself, since
only a send writes a trace. `getThread` reads the mailbox and thread of a
message imported for it (`on_a_thread_of_its_own`). Missing's `2147483647`, a
non-positive or undecodable id and every documented 404 keep their ids. One
send per run is added, still far below the limit. No message id is carried
across a reset, so no stash value stands in for the linked one.

The draft operations, `saveDraft`, `updateDraft` and `sendDraft`, reset and
seed before each exchange too. A save makes its sender's mailbox again after
the reset, as before; an update aimed at a positive id other than missing's and
a documented 404's gets a draft of its own (`on_a_draft_of_its_own`), since the
one `saveDraft`'s link names is gone; a send already makes its own draft.
`saveDraft`'s link reaches only the update, and the update's only the send, so
nothing that follows them is left unmoved.

The reset keeps the administrators, and gives each retained account a usage row
again at nothing (`cleanResetData`): it clears `quota_usage` with the messages,
and an import into an account with no usage row is refused (500, "destination
quota usage is missing").

The DKIM operations, `setDKIMKey`, `deleteDKIMKey`, `getDKIMRotation` and
`setDKIMRotation`, reset and seed before each exchange too, refusals included
(`a_dkim_domain_of_its_own`). Each follows `createDomain`'s link, and the
domain it names is gone after the reset, so an exchange aimed at a positive id
other than missing's `2147483647` gets a domain of its own; the delete's 204
also gets the key it deletes. `setDKIMKey`'s link reaches only the delete, so
nothing that follows them is left unmoved.

The reset also puts each domain it keeps back to a new domain's settings
(`cleanResetData`, `resetDomainSettings`): its DKIM key and selector, key
installation time, rotation interval, sending and DMARC policy and default
quota. The seed finds a kept domain by name and leaves its row alone, so a key
made before a reset came back with the re-seeded `restmail.test`. The seed sets
its own domains' kind and active state again. Every column of `domains` is
either reset or kept for a stated reason (`resetDomainKept`), and
`TestCleanResetEveryDomainColumn` fails on a column that is neither.

This happens only with `CONTRACT_RESET_SEED=1`, which `tests/contract/run.sh own-data`
sets, and that phase is a gate of its own, sent by `chore test:contract-teardown`
just before the teardown. It sends the moved operations with everything that follows
`createDomain`'s, `createPipeline`'s, `createAlias`'s, `createCustomFilter`'s,
`createFolder`'s, `createContact`'s, `sendMessage`'s and `saveDraft`'s links
(`own_data.py`), and fails on a skipped
exchange as well as a failed one.

**The primary run does not reset.** It still sends these operations as before, because a
reset there removes what the unmoved operations after it depend on, not only the
domain: a local whole-API run with these four resetting in the primary run fell from
538 passes to 503, across accounts, messages, certificates, custom filters and the
queue. Coverage, authorization
and ownership stay in their own processes without a reset too, so nothing here
changes their probes. The primary run can start resetting once nothing that has not
moved is sent after a reset.

A reset removes the domain `createDomain` made, and its id is what the links carry.
So after a reset, an unmoved operation that follows one of those links gets a stand-in
domain (`a_live_domain_for_the_link`), made once per reset and kept where the created
domain was. An allowlist or blocklist entry the link names is added again. A run with
no reset changes nothing there.

What a reset does and does not touch here:

- **Rate limits:** the reset refills the buckets in the API's memory, so setup does not
  wait for them. The primary run still paces its sign-ins.
- **Sessions:** the fixture admin and the two-factor administrator must exist before
  the first reset, which keeps them with their ids and credentials. Vertrag's session,
  the setup session and the two-factor session therefore stay valid across resets.
  `.cache/contract/mfa-admin.json` stays valid for the same reason. Nothing else may
  be cached across one: `start_clean` drops everything in `hooks.stash` except
  `SURVIVES_A_RESET`. Domain ids are not stable from one reset to the next, because they
  depend on what was made since, which is why each exchange makes its own.
- **Outside the database:** delivery work in flight holds up the reset for up to 30
  seconds, and a timeout fails the exchange as an error rather than sending it. Event
  streams are closed by the reset. `.cache/contract-principals.json` names accounts
  that the first reset removes, so nothing that reads it can run after this phase,
  which is why this phase is the last before the teardown.

## Principals

`fixtureplan/defaults.json` is the shared declaration of the existing named
principals, their account/owner relationships, and the held retry queue entry.
The E2E principal proof and `fixtures/` command read it through the embedded Go
`fixtureplan` package; Vertrag hooks read the same JSON file. Live IDs,
passwords and newly issued app passwords still come from setup and are written
to `.cache/contract-principals.json`, preserving the existing hook bindings.
The plan is restricted to the disposable `restmail.test` domain and reserved
`.invalid` queue destinations. Unknown fields, missing principal references,
duplicate accounts and immediately claimable queue entries fail validation.

This is the initial consolidation for #662. Other area fixtures, named-value
placeholders and full resets between phases remain pending. The current test
snapshot does not contain credentials, grants, contacts and several other
fixture tables; restoring it between phases would delete required state, so
this change does not wire that incomplete restore into the runner.

The E2E suite makes one of each kind of caller and checks a small
authorization matrix with them (`TestContractPrincipals`):

| Principal | How |
| --- | --- |
| admin | the seeded `admin@restmail.test`, superadmin at `*` |
| ordinary account | `contract-ordinary@restmail.test`: a password, no grants |
| owned account, no password | `contract-owned@restmail.test`: reached only through its owner, the admin |
| app password | one for the ordinary account; what it holds is fixed: read, write and send on its own mailbox |
| other account | `contract-other@restmail.test`, made like the ordinary one, with an app password of its own: the second tenant of the ownership phase |
| agent | none: the account model has no agents yet (#341) |
| no credentials | every request without one, and vertrag's `ignored-auth` check |

The examples and coverage run signs in as the admin. The authorization run
uses vertrag's authorization phase (v0.29.0 and later): each operation's `security`
requirements name the capability it needs as OpenAPI 3.1 roles, for example
`security: [{BearerAuth: [domains:read]}]`. `authorization.py` configures `none`,
`admin`, `ordinary`, and `app-password` from the fresh E2E fixture. The owned
account has no separate sign-in credential; its access through the admin is
checked by `TestContractPrincipals`. Agents remain unavailable (#341).

The full matrix is **report-only** until its fixture and scope expectations
are resolved. The provisional admin expectation lists every role in the spec;
ordinary and app-password list none, since their mail rights cover their own
mailbox rather than arbitrary example IDs. These flat roles cannot express
resource ownership or grants. In particular, admin access to other mailboxes
and out-of-scope 404 versus 403 still need a policy decision. Since vertrag
0.35.0, a cell whose IDs the description's links supply addresses a resource
made for it by following those links (vertrag#81); an operation no link leads
to still sends its example IDs, so absent resources there, reused refresh
tokens and hooks that replace a credential can still cause failures or unsent
cells. This report does not establish
that every operation or principal passes. The existing examples and coverage
gates keep their configuration and exclusions.

Run the matrix locally after the examples and coverage gates. vertrag always
runs examples first, even with `--phases authorization`; this diagnostic run
repeats them with lifecycle ordering, then sends the matrix. Both can repeat
destructive requests, so run it last on the disposable topology:

```sh
python3 tests/contract/authorization.py openapi.yaml vertrag.yml \
  .cache/contract-principals.json .cache/contract/vertrag-authorization.yml \
  .cache/contract/authorization.xml
vertrag run --config .cache/contract/vertrag-authorization.yml --sequence --phases authorization
rm -f .cache/contract/vertrag-authorization.yml
```

The ownership phase (vertrag 0.37.0 and later) asks a different question: not
whether a principal may use an operation, but whether it may use it on another
tenant's objects (OWASP API1). `ownership.py` groups the principals into two
tenants: `a` is `ordinary` and `app-password`, and `b` is `other` and
`other-app-password`. Each holds `mail:read`, `mail:write` and `mail:send` on
its own mailbox, and the password sessions also hold `mailbox:manage`. No
operation makes an account, so each tenant declares its own account id, and
the phase sends tenant `b`'s principals tenant `a`'s id and the other way
round. Every answer must be 403 or 404 (#656); a success is an ownership
bypass. Resources the description's links make, such as a folder or a contact,
are made again for each cell as their owner. Only the `/api/v1/accounts/{id}`
operations are selected, because the declared id is an account's, and the
account list beside them: tenant `b`'s list of accounts must not contain tenant
`a`'s account id, or the other way round.

A second run sends the domain operations. `chore test:contract-domain-tenants`
makes two domain administrators directly in the database: one of restmail.test
(tenant `a`) and one of a second disposable domain, contract-other.test (tenant
`b`), each holding the admin role on its own domain only. Changing grants
through the API takes a two-factor session, which no contract principal has.
`ownership.py --domains` declares each tenant's domain id and selects the
`/api/v1/admin/domains/{id}` operations and the domain list, which must not
show the other tenant's domain. It also sends the DKIM routes, whose id is the
domain's, and the aliases, custom filters, pipelines and certificates a create
links to, which the victim tenant's administrator makes again for each cell.
The queue, grants and bans take ids that no link makes and that are not a
domain's, so they are not sent. The admin mailbox routes have a third run,
`ownership-mailboxes` (`ownership.py --mailboxes`): the same administrators,
each declaring its own mailbox, which the fixture prints beside its domain, are
sent on `/api/v1/admin/mailboxes/{id}` and the mailbox list. Its report is `ownership-domains`.
Tenant ids are keyed by parameter name, and the account run already declares
`path.id`, so the domains need a run of their own.

```sh
python3 tests/contract/ownership.py openapi.yaml vertrag.yml \
  .cache/contract-principals.json .cache/contract/vertrag-ownership.yml \
  .cache/contract/ownership.xml
vertrag run --config .cache/contract/vertrag-ownership.yml --sequence --phases ownership
rm -f .cache/contract/vertrag-ownership.yml
```

`python3 -m unittest discover -s tests/contract -p 'test_*.py'` checks the
configuration generator and coverage gate (requires PyYAML); CI runs it in
the lint job.

`internal/api/openapi_security_test.go` holds those roles to the router for
every admin operation: holding exactly the documented capability reaches the
handler, holding every other one is refused. The mail operations check their
capability (`mail:read`, `mail:write`, `mail:send`, `mailbox:manage`) in the
handler, on the account they name, so there is no route table to read; their
roles were taken from the handlers and the authorization phase will check
them.

## Closed schemas

`TestGeneratedSchemasAreClosedUnlessRecorded` inspects component and inline
schemas in the generated description, including nested values. Each dictionary,
opaque JSON value, or explicitly open protocol object is listed by its exact
JSON pointer in `open-schema-exceptions.yml`, with a reason and an area issue
where a more precise shape is still needed. A new open schema fails the test;
closing one requires removing its exception in the same change. Example data
is not treated as schema.

## Running it

One command does what CI does (#627), on any machine with Docker, `chore`, Go
(the version in `go.mod`, for the example-pair test binary) and Python 3. The
command installs and selects the CI-pinned Vertrag automatically.

### First run

A fresh checkout needs the test substrate before `chore contract` can bring up
the topology:

```sh
chore testbed:init       # clone rest-mail/testbed, seed config/restmail.test
chore mailserver:init    # clone rest-mail/reference-mailserver into .workspace/
```

Both clone over `git@github.com:`, so they need GitHub SSH access. The first
contract run also needs the internet: it downloads the pinned Vertrag release,
runs `pip install pyyaml` when PyYAML is missing, and pulls the topology's
container images. After that, the check talks only to the local
`restmail.test` topology.

Two differences from CI remain. CI checks out `testbed` and
`reference-mailserver` at fixed commits (`TESTBED_REF` and
`REFERENCE_MAILSERVER_REF` in `e2e-suite.yml`), while the init tasks clone each
default branch. CI also patches the reference server's `dkim:provision` for
native-Linux Docker, which Docker Desktop does not need. When a local result
differs from CI's, check those commits first.

### Running the check

```sh
chore contract                      # example pairs, the E2E topology, then every phase
CONTRACT_ENDPOINT=http://10.99.0.20:8080 chore contract   # a topology already up
chore contract:run -- --tag Domains # the gates for one area, against a running topology
chore contract:run -- --operation-id getDomain
chore contract:fuzz -- --seed 42    # the fuzz phase; replay a finding by its seed
chore contract:diff                 # the generated description against openapi.yaml, no server
chore contract:pairs                # only the registered example-pair checks, no server
```

The complete command and each live phase first run
`python3 -B scripts/vertrag_pin.py ensure .cache/bin`. A cache receipt records the
workflow pin, platform, archive checksum and installed binary hash. A matching,
executable cache is reused without downloads; stale or corrupt entries are
reinstalled from the pinned release after checksum verification. Linux x86_64's
release checksum must also match the immutable digest in the workflow. The
binary and receipt are published as complete files, and an incomplete receipt
cannot validate a changed binary. The selected executable's `version` command
must start successfully before topology or fixture setup. Its absolute path is
exported to API-key probes and phase child commands. CI uses the same installer
and cache; `chore vertrag:install` remains available for an explicit reinstall.

`chore contract` then runs these steps in order:

1. `chore contract:pairs`, the registered example-pair checks
   (`tests/contract/example_pairs.sh`). It builds the `internal/api` test binary
   once and runs the named tests in three parallel shards, each limited to 25
   minutes. A failure stops the run here, as it fails the CI job.
2. `chore e2e:up`, unless `CONTRACT_ENDPOINT` names a running topology.
3. The principal fixture, when it is absent (below).
4. `chore test:contract-fixtures` and `chore test:contract-api-keys`: the
   fixtures and the API key lifecycle proof. A failure stops the run.
5. `tests/contract/run.sh gate`: fetches `/api/docs/openapi.json` into
   `.cache/contract/openapi.json`, then runs the examples phase and the coverage
   phase held to `coverage-baseline.txt`.
6. The secret scan of `.cache/contract/examples.har`, a gate.
7. `chore test:contract-fixtures` again, because the gate consumed the held
   queue entries, then `tests/contract/run.sh authorization` (a report).
8. `chore test:contract-domain-tenants`, then `tests/contract/run.sh ownership`
   (a report of the account, domain and mailbox ownership runs).

The command fails when the example pairs, the setup, the gate or the secret
scan fail. The authorization and ownership reports never fail it. CI's E2E job
calls `chore contract`, so a run here gives the verdict CI gives for the same
commit, apart from the substrate differences under "First run".

The example-pair test list lives in the `contract:pairs` task in `chores.yml`.
A new `*WithVertrag` test in `internal/api` must be added there, or no run sends
it; `test_run.py` fails until it is. Without `RESTMAIL_VERTRAG_BINARY` those tests skip, so `go test` alone does
not run them.

A narrowed run (`--tag`, `--operation-id`, `--only-matching`)
reports without the surface gate and the baseline, which are about the whole API.
Its coverage verdict comes directly from Vertrag: exit 0 for passing selected
probes, exit 2 for findings. A narrowed finding is not excused by the whole-API
baseline. The complete command preserves that failure while still running the
secret scan and producing the authorization report.

The secret scan also runs locally, including after a failing examples gate.
The API key proof and every Vertrag phase honor `CONTRACT_ENDPOINT`. A fetch
failure stops the run. Each phase, including standalone authorization, fetches
fresh generated JSON; a failed fetch removes the old snapshot. Before API calls,
the pinned Vertrag compiles both the live URL and the exact saved JSON. Different
bytes, transactions, schemas or annotations fail the gate. The successful
`description-equivalence.json` records hashes and the transaction count; it proves
input/compilation equivalence, not successful API exchanges. Every invocation
removes an earlier equivalence verdict before tools can fail.

The default `CONTRACT_DESCRIPTION_MODE=saved` sends Vertrag the saved JSON path.
`CONTRACT_DESCRIPTION_MODE=live chore contract` instead supplies the live URL to
examples, coverage, authorization and fuzz. The saved bytes remain the inventory
for roles and the coverage ratchet in both modes. No API credential is sent to
the description fetch. Vertrag’s own live/file CLI tests additionally compare
selected requests and verdicts, with wrong-status and wrong-shape negative controls.
`openapi.yaml` remains the independent oracle for `chore contract:diff`.

This switch does not relax exclusions, surface requirements or coverage baselines.
An existing fixture that disagrees with the generated description must be fixed
and verified against the topology before the migration can be declared complete.

When the shared principal fixture is absent, `chore contract` runs only
`TestContractPrincipals` from the E2E suite to create it, using
`CONTRACT_ENDPOINT` as that test's `API_BASE_URL`. CI reuses the fixture its
earlier E2E stages created. The endpoint override names the same disposable
`restmail.test` topology: fixture resets still run in its local API container;
it does not turn this into a check for arbitrary or production servers.
Lifecycle and scope gates remain #626.

Each phase writes JUnit, JSON, HTML and Markdown to `.cache/contract/` (`examples.html`,
`coverage.md`, …), and the run ends with a summary, also in
`.cache/contract/summary.md`. The summary includes Vertrag's own operation
counts and each operation/status exclusion with its reason. The complete command
clears previous reports and generated run inputs before topology or fixture setup,
and prints a fresh summary even if setup fails. Fixture credentials and other
files are preserved. When setup succeeds, the summary prints once after the
authorization report. Standalone phases clear their own previous reports and
summarize only their own phases, including when a dependency or tool fails.
Authorization keeps the preceding gate's saved inputs. An earlier run's results
cannot appear as this invocation's verdict:

```
| Phase | Transactions | Passed | Failed | Skipped |
|---|---:|---:|---:|---:|
| examples | … | … | … | … |
| coverage | … | … | … | … |
```

CI uploads the same reports with the container logs (the `e2e-container-logs`
artifact), so a run's verdict can be read in a browser.

`vertrag run --dry-run --sequence` shows what would be sent, with nothing up.

Contact create probes use a before hook to remove contacts made by earlier
probes when an email is reused. It first checks, through every page of the
account's contacts, that the email was absent,
leaves pre-existing contacts alone, and preserves generated bodies, account IDs
and headers. Coverage does not call after hooks, so ownership is recorded before
the first create attempt.

`getCustomFilter` sends its documented `missing` 404 using the missing-ID path
parameter example, paired with the 404 alone by Vertrag 0.30; the create link
fills only the successful lookup's id. The handler test proves the missing row is
absent, checks the exact refusal and successful lookup, and verifies no stored
filter changes. Other missing-resource responses remain excluded (#386).


Draft probes (saveDraft) save from a generated address (`someone@example.com`)
that no mailbox holds, which the handler refuses with 403. A before hook hosts
the sender's domain and creates its mailbox through the admin API the first
time the address appears, then leaves them in place. Malformed senders and
domains the server will not host are left to the probe, and the request is
never rewritten. With `from` optional and invalid recipients refused,
`saveDraft` comes out of the coverage baseline.

Send probes (sendMessage) send from a generated address (`someone@example.com`)
that no mailbox holds, which the handler refuses with 403. A before hook hosts
the sender's domain with a DKIM key (an https unsubscribe target needs one) and
creates its mailbox through the admin API the first time the address appears,
then leaves them in place (after a reset, the first time since it), with the two-factor administrator's session where
that takes one. Malformed senders and domains the server will not host are left
to the probe, and the request is never rewritten.

Domain probes used to leave `restmail.test` at a `bulk` sending policy, so a send
from it needed `list_unsubscribe`. That one cause skipped `sendMessage`'s 14 probes
and `sendDraft`'s 1, and failed `cancelScheduledMessage`'s fixture. `off_the_admin_domain`
now moves those probes in the coverage process, so `restmail.test` keeps no policy
and an ordinary send has no setup request to make.

These probes are accepted, so they send mail. Vertrag's generator has one
address for `format: idn-email` and does not read the description's examples, so
that address is `someone@example.com` for sender and recipients alike. The hook
gives every well-formed recipient of a generated send a mailbox too, and a
recipient with a mailbox here is delivered here (`sendMessage`), never queued.
A whole-API local run wrote no queue row for it, so no mail leaves. Local delivery
of each is refused by the DMARC check on the sender's domain, and the send still
answers 201 with its Sent copy. That check is a DNS lookup of `example.com`, which
does leave the machine when the resolver forwards it: the testbed's dnsmasq
forwards to public resolvers. Only a CI run shows whether the API's lookups go there.

The description says what the handler requires: `to` required and nonempty, and
`from`, `to`, `cc` and `bcc` bare addresses. It binds nothing, so no answer
changes, and the handler still accepts an RFC 5322 display name around an
address. go-oapifly's tags cannot give a list's items a format, so
`internal/api/described_items.go` does that. The body is inline rather than a
component because Vertrag's `fuzz.pin` does not follow a `$ref` at a body's root.

`scheduled_at` is held at null in the coverage run (`probes.py`): the generator's
only date-time is in 2020, and a send's time must be a minute to a year ahead.
Null means send at once, and keeps the success example the 201 it documents, where
any time would make it a 202. No other request body has the field
(`TestSendMessageDescribesWhatTheHandlerRequires`). The named examples prove what
the pin takes away, in the gated examples phase, which is not pinned:
`scheduled_at_past`, `scheduled_at_too_far`, `scheduled_at_malformed` and
`scheduled_at_wrong_type`.

Update probes (updateDomain) that set `active` to true would meet 409
`certificate_required`: no created domain has a certificate when they run. A
before hook gives the domain in the path a self-signed certificate through the
admin API unless the API already lists one valid now for that domain, then
leaves it in place. Whether a domain has one is read from the API, not kept in
`hooks.stash`. The request is never rewritten, and a domain that does not exist
is left to the probe's 404.

Test send probes (sendTestEmail) send from and to a generated address
(`someone@example.com`) that has no mailbox. A before hook hosts its domain and
creates its account through the admin API the first time an address appears
(after a reset, the first time since it), then leaves both in place: a test
send only adds a message. Malformed addresses
and domains the server will not host are left to the probe, and the request is
never rewritten.

`getAlias` sends its `missing` path ID (`2147483647`) and exact 404 refusal,
paired with the 404 alone by Vertrag 0.30; the create link fills only the
successful lookup's id. The real-handler
test checks missing and successful lookup, domain association and unchanged
storage. `test_gate.py` pins the complete enabled 404 set listed above;
unpaired sibling operations retain their exclusions.

`updateDomain`, `updateMailbox`, `updateAlias` and `updateCustomFilter` each
pair a `missing` path ID (`2147483647`) and a valid named request body with
only their exact 404 response. Their successful and invalid-body examples
continue to use the created resource's link. Vertrag 0.30 preserves the named
missing ID through sequencing, including when the request has a body; no hook
rewrites these requests. `TestAdminUpdates_DocumentedMissingExamples` checks
both descriptions, sends the registered routes' exact requests, and verifies
that refused updates neither change existing resources nor create the missing
target.

`deleteDomain`, `deleteMailbox`, `deleteAlias` and `deleteCustomFilter` also
pair a named `missing` path ID with their exact 404 response. These requests
have no body. Successful requests retain their existing path and link
behavior, including bodyless 204 responses. The pinned Vertrag 0.30 runner
was checked with a linked create/delete fixture: it sends both the absent ID
and the created ID, with no hook rewriting either request.
`TestAdminDeletes_DocumentedMissingExamples` checks both descriptions and
verifies that each registered route leaves existing rows and counts intact
when it refuses a missing target. `test_gate.py` pins the complete enabled
operations; other unpaired refusals retain their exclusions.

`updatePipeline`, `deletePipeline` and `testPipeline` also send a named
`missing` request and the exact `not_found` / `Pipeline not found` 404. The
PATCH and DELETE use the absent positive path ID `2147483647`; the preview
places it in `pipeline_id` alongside a valid empty email. The configured
preview hook substitutes only the successful request's ID placeholder `1`,
leaving missing IDs, invalid targets, credentials and expected responses
intact. The create link continues to supply successful update and delete IDs.
`TestPipelineOperations_DocumentedMissingExamples` checks both descriptions
and unchanged stored pipeline state for all three registered routes. The
configured-entrypoint hook regressions and `test_gate.py` keep all twenty-two
missing-resource operations in the required examples gate.

### How to read a failure

1. Find the step that failed. The command's output names it, and the summary
   table at the end shows which phases ran. A run that stops at the example
   pairs or the setup prints an empty table.
2. For the example pairs, the output prints each shard as `== shard N: ok`,
   followed by each test's verdict, or `== shard N: FAILED`, followed by that
   shard's whole `go test` log. Look for the
   `--- FAIL:` line, then run that test alone:
   `tests/contract/example_pairs.sh -run '^(TestDomainCleanupExamplesWithVertrag)$' 1`
   with `RESTMAIL_VERTRAG_BINARY` set (`python3 -B scripts/vertrag_pin.py ensure .cache/bin`
   prints the path).
3. For a gate phase, open `.cache/contract/examples.html` or
   `.cache/contract/coverage.html` (or the `.md` files) and find the failed
   transactions. The "Not passed" table in `summary.md` lists their operations.
4. Rerun one operation against the running topology:
   `chore contract:run -- --operation-id getDomain`. A narrowed run checks
   only that operation, without the whole-API surface gate and baseline.
5. A secret scan failure prints one `secret echoed:` line per finding in
   `.cache/contract/examples.har`. `secret scan failed: input or audit log could
   not be verified` means the scan itself could not finish, which also fails.
6. In CI, download the `e2e-container-logs` artifact. It holds the same report
   files plus each container's log.

### API code coverage of the contract run

With `API_COVER=true`, `chore e2e:up` builds the API with `go build -cover`.
`chore contract` then clears its counters before the gate and, after the
ownership phase, writes `.cache/contract/api-coverage/summary.md` and
`api-coverage.out`. E2E runs in CI do this and show the summary on the run page.
It is a report: nothing fails on the percentage, and without a cover build both
steps do nothing.

### Browser session gate (#659)

Before examples, `chore contract` runs `tests/contract/browser_sessions` against
that run's saved served JSON and existing `restmail.test` admin fixture. It signs
in as a browser, validates all three cookie attributes and paths, checks every
cookie-protected unsafe operation refuses a missing CSRF header, a mismatched
token, and a missing CSRF cookie even with a header present, and compares
safe reads with all three invalid variants against a valid-header control.
Invalid resource IDs and malformed JSON keep negative probes away from handler
mutations. A valid-header folder request
must reach validation, and an own-account event stream must open using cookies.
Refresh must return browser cookies without body tokens; logout must expire all
three cookies and refuse another refresh with the revoked cookie. The probe revokes its own
session on failure as well.

Failures stop the gate. `browser-sessions.json` records operation, principal and
status only, with `passed` true only after the whole proof. Local HTTP fixtures
verify that removing CSRF enforcement, checking only header presence, accepting
a missing CSRF cookie, or removing a cookie's Secure attribute fails this proof.
Receipts name each CSRF variant without recording tokens. Response reads reject
oversized bodies rather than accepting a truncated prefix, and JSON responses
must be exactly one complete object. Empty HEAD/204/304 responses, non-JSON
errors and streaming event responses keep their distinct handling. Errors never
include server-supplied error-code values. Local fixtures also inject malformed,
trailing and oversized JSON and safe-method CSRF enforcement faults; these must
fail the gate. See `browser-session-findings.md` for the precise proof boundary.
Live topology and broader mutation workflows still run in GitHub.

### Contract changes in pull requests (#653)

The `API contract changes` CI job generates the description from the exact PR
head and its target base. It uses the same `TestTheGeneratedDescriptionIsServed`
export as the UI type builds, without a database. Neither document is committed.
For a stack, the comparison follows each PR's base, so each layer shows its own
changes.

The checksum-pinned oasdiff 1.34.0 compares OpenAPI 3.1 operations, parameters,
schemas, statuses, headers and security requirements, including capability roles.
The check summary classifies the changes; the `api-contract-changes` artifact
contains both generated documents and the JSON and Markdown comparison.

- Breaking changes require the `contract-change` PR label. Removing a field or
  enum value and changing a type require it even when a compatibility checker
  would allow that direction. Comparator warnings also require deliberate
  acceptance rather than being called additive.
- Security changes require `permission-change`, including a removed capability,
  anonymous access added, or a change to how credentials are sent. A breaking
  schema change together with a security change requires both labels.
- Additive changes need neither label.

Send As and Send on Behalf add security alternatives and therefore require
`permission-change`, even when the request changes are additive. Their
`on_behalf` request fields are optional; own-mailbox clients can keep composing,
forwarding and sending drafts with `mail:send` and the existing source-access
permissions. `TestOwnMailboxLegacySendRequests` proves those requests, including
a draft send with no body, without either new delegated capability.

The existing required `CI` aggregate includes this job. Adding or removing a
label reruns CI, so removing an acceptance label cannot leave the old verdict
in place. No branch-protection settings are changed. External references are
disabled while comparing the documents.

To replay a comparison locally with generated base and revision JSON files:

```sh
python3 scripts/oasdiff_pin.py install .cache/contract-diff-bin
python3 scripts/contract_diff.py base.json revision.json \
  --oasdiff .cache/contract-diff-bin/oasdiff
# Accept a deliberate change with --label contract-change or --label permission-change.
```

This is the server PR gate. The mobile client's check against the newest server
description remains part of #116; #653 stays open until its client requirements
are met as well.

### MTA-STS lookup without a policy

`getMtaStsPolicy` also pairs `policy_absent` with its distinct 404:
`not_found`, "No MTA-STS policy found for this domain". The hook entrypoint
loads the shared hooks once and reuses their setup session for an isolated,
inactive domain created through the public admin API. It substitutes that
fixture only for this GET/404 placeholder, preserving the caller credential
and complete expected response. It never deletes a policy or changes the
linked success domain. Coverage mutations, missing-domain and unauthenticated
requests retain their original inputs.

`test_policy_lookup_hooks.py` exercises the configured entrypoint, setup HTTP
requests, reuse per origin and unmodified refusals/failures.
`TestMtaStsPolicy_DocumentedAbsentExample` checks the pair and, against
PostgreSQL, the registered route, unchanged domains and policy state. Both
API descriptions carry the same request/response pair; the existing lookup
404 selection requires it in the examples gate.

### Probes of the run's own account

`deleteMailbox`, `endMailboxSessions` and `inviteAccount` act on the account
their path names, and a generated id can name the account the run signs in as
(the fuzz phase once sent `DELETE /api/v1/admin/mailboxes/4` as
admin@restmail.test). Deleting it is refused with 403 `two_factor_required`,
ending its sessions would turn the rest of the run into 401s, and inviting it
is refused with 409, so none of those answers says anything about the
description. `own_account_hooks.py` looks up the session's own account
(`listAccounts`, first entry) once per origin and skips a request to these
operations that names it, expects a 2xx and is sent as the run's principal.
It skips rather than redirects: vertrag reports the id it drew, not a path a
hook rewrote. Documented refusals, other ids and other principals are sent
unchanged. `test_own_account_hooks.py` covers it through the configured
entrypoint.

## Received DMARC reports

The fixture command seeds one typed received report for `restmail.test` with
authenticated reporter `reporter.test` and report ID `daily-1`. Repeating setup
keeps the same row through the production `(reporter, report_id)` unique index.
There is no HTTP report intake to call for setup: SMTP ingestion is proven by
the server's mail-delivery tests instead.

Domain-grant variants live in `internal/examplepairs/example-matrix.json`.
Published credential refusals declare their principal prerequisites, so an
ordinary administrator replay never expects an anonymous or no-capability answer.

`dmarc_report_hooks.py`, loaded by `entrypoint.py`, locates that existing report
for authenticated `getDmarcReport` successes whose illustrative ID is 1. It
changes only that resource ID. Refusals, absent credentials, other IDs and invalid
boundary probes remain untouched; fixture absence fails rather than adding a
skipped coverage probe. The report-list filters and pagination are sent as
written. `test_dmarc_report_hooks.py` proves preservation of the principal,
expected response and invalid probes, and `fixtures/dmarc_test.go` proves
repeat-safe setup on an isolated database. No skipped-probe baseline increase
is needed.
