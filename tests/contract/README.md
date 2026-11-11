# Contract tests

[vertrag](https://github.com/antimatter-studios/vertrag) sends the selected
contract requests to a running API and checks each answer against the spec's
status, headers and body schema (#376). Every phase uses the generated
`/api/docs/openapi.json`, saved with that run’s reports. The handwritten
`openapi.yaml` remains the independent comparison oracle, not the runtime input.

E2E runs `chore contract:pairs` beside `chore contract:topology`, on separate
topologies. Pair shards use their own test fixtures and disposable databases;
the topology contract recreates its principals, queue, API-key and MFA fixtures.
No pair-test state or reports are inputs to the topology contract. Examples,
coverage, the HAR secret scan and teardown stay together because they consume
the same live resources, saved description, run configuration and reports.
`chore contract` still runs both phases in sequence locally. The required
`E2E (full topology)` check needs both jobs; the proof union still receives
CI's four in-process proof files and the independent live-proof artifact.

The topology contract jobs set `RESTMAIL_CONTRACT_LIMITS_RAISED=true`: auth
(including owner and credential-refusal buckets), pipeline previews and TLS-RPT
remain enabled with 10,000 requests/second and a burst of 10,000. Only the
corresponding hook pacing sleeps are skipped. Trace flushing and send-budget
waits are unchanged. The Limits suite recreates the API with the contract and
load overrides off, checks the actual container environment against the real
defaults, and then spends those default limits. Teardown recreates the API
with the job's contract setting again. The coverage report is collected before
either recreation; no coverage report or fixture is transferred between jobs.

The additional [live requirement proofs](#live-requirement-proofs-1237) exercise
anonymous callers, real over-cap bodies and a bounded real-principal matrix
with the pinned Vertrag. The E2E suite runs them as a report, and what they prove
joins the in-process proof union. #1237 tracks full-topology acceptance.

Bad input follows the owner's decision on #672 (2026-10-28). A body that is not
valid JSON, or is not the declared media type, gets 400 `bad_request`. Parsed
input that breaks a shape, field or combination rule gets 422
`validation_failed`, with `details.fields` naming the failing fields. This
includes arrays/null where an object is required, invalid Sieve, unsupported
filters and recipient limits. State preconditions retain their existing status
pending a separate decision. The 403/404 decision remains open.

## Before every push: local CI

Branch freshness is enforced both by CI preflight and github-guard's default-on
`git-push-up-to-date` guard; see [CI branch freshness](../../docs/ci.md).

Run `chore ci:local` on the working tree before pushing, including after a
rebase. It fetches `origin/main` and refuses a stale base; `--allow-stale` is
an explicit diagnostic override, not proof that a change will merge safely.
Pass options after `--`, for example `chore ci:local -- --all` to test every
Go package rather than changed packages and their transitive importers. In
either mode `tests/e2e` is left to the E2E job, which starts the topology it needs.
Selection includes committed, staged, unstaged and untracked files, test-only
imports and package fixtures. Module/replacement changes select every package.

To check a committed PR head as it will merge without touching a dirty current
checkout, run `chore ci:local -- --merge-pr 1816` (substitute the PR number). It
merges the head over freshly fetched main and every merge queue entry, in order, or
only the entries ahead of it if the PR is already queued. GitHub's CLEAN and a green
run say only that the PR merges with main alone; #1880 and #1658 both looked fine
and failed only with #1900 ahead. A conflict fails and names the entry that causes
it. An entry ahead that conflicts with those before it is left out, as the queue
would, and the run then ends `WARN`. `--merge-ref COMMIT` tests an existing local
commit over main alone, unless `--entry` names what goes ahead of it. Both build the
merge in an isolated detached worktree and remove it even on failure. They test the
committed head, not uncommitted changes in the current checkout. With entries ahead,
package selection covers what they change as well as the head.

## Before enqueueing: the whole queue

`chore ci:local -- --stack` merges every merge queue entry in order over newest main
in a detached worktree, then runs `go vet ./...`, `scripts/tests` and `tests/contract`.
These three all run even when the first fails. Whatever fails is then repeated on each
prefix of the queue, from main alone upward, to name the entry where it starts, and
that entry is also tried alone over main to show whether it needs one ahead of it. A
test that does not exist yet at a prefix does not count as failing there. A failure on
main alone blames no entry, and one that does not repeat stays a failure and says so.
It does not run the other checks. If the queue cannot be read, the run could not run:
that is never taken for an empty queue.

To replay a past stack, name its parts rather than reading the queue:

    chore ci:local -- --stack --base ab33aacf6^ --entry be03c19d --entry b0c1a61c

That is main just before #1900 merged, then #1900's head, then #1880's. It fails
`test_ci_transient_failures.ArtifactUploadTests.test_a_job_downloads_only_what_is_tried_again`
and names the second entry, where neither PR fails over main alone.

The command runs all three Python suites (`scripts/tests`, `tests/contract`,
`tests/load`), Go vet/build, the exact golangci-lint version pinned in CI,
selected Go race tests and the contract fault-switch checks. The large
`internal/api` and `internal/api/handlers` packages run in eight isolated-database
race shards, with the same 20-minute limit per shard as CI's API matrix. CI's
four-way split does not finish inside that limit on a Mac at a load of 300, and
eight shards keep the limit rather than widen it.
Each shard prints its wall time against that limit. A shard that reaches it is
reported with the machine's load, and the tests it did not finish run once more
in a new process under the same limit. If they pass, the step ends `WARN`, not
`PASS`, and names them: CI may reach its own limit on those tests. If they reach
the limit again the step has no verdict (see below), and a test that failed before
the limit fails the step. Each shard also has a new
database, so a test that relied on an
earlier test's rows fails here and not in CI's single-database order; the
summary names it. `internal/api/handlers` is known to depend on test order, so a
failure in one of its shards says so: confirm it on the whole package.
The nested IMAP
module and empty-database fixtures run when affected, or with `--all`. For the IMAP
module that means the same paths as CI's `scripts/ci/go-imap-changed`, and CI's gates:
the coverage gate's fixtures, vet, its own lint, the RFC checklist, race tests with
coverage and the 95% floor. Its 32-bit wire proofs only build here for linux/386,
because a Mac cannot run 386, so only CI shows they pass. The IMAP
module has a 30-minute limit here and 10 minutes in CI: its checklist is one test
and took over 10 minutes on a Mac at a load of 300.
API changes also discover and run every
`*WithVertrag` test, in three isolated-database shards, rejecting empty
discovery or a test that skips whole; a subtest that skips itself is named. `--all` forces those examples too. It does not
change `contract:pairs` or its selection policy. Each of those shards has a 60-minute
limit: together they took about 32 minutes at a load of 50 to 85, and 20 minutes cut
them off.

The Python suites need PyYAML, which the system Python often lacks and which then
shows as import errors that say nothing about the code. So they run from the gate's
own venv, never from the Python that started it. That venv, the pinned linter and the
checksum-verified Vertrag binary are cached outside the repository, under
`${XDG_CACHE_HOME:-$HOME/Library/Caches}/rest-mail/ci-local`. If PyYAML cannot be
installed the first step ends `COULD-NOT-RUN`, names it and says how to repair the
venv. To run a suite by hand, use that venv's `python`. Missing Python/Go/
Docker tools or failed installs are errors, not successful skips. The Go
toolchain must match CI, including inside the nested IMAP module.

The task runs the gate through `scripts/scratch_run.py`, so the whole run has
`GOTMPDIR` and `TMPDIR` in a directory of its own, removed even after a SIGKILL along
with what the run left running. `RESTMAIL_SCRATCH` chooses where that directory goes,
by default `$HOME/tmp/rest-mail-ci-local`; `--cache` chooses the cache, and both must
be outside the repository and off the SD card. A killed run's worktree entry is pruned
when the next run starts. When Go tests need a database, PostgreSQL uses a unique
`restmail-1941-postgres-*` container with label `rest-mail-loop=1941` and a second
label from `RESTMAIL_LABEL`, a
Docker-assigned loopback port, data on tmpfs rather than a volume, and
per-package databases. SIGINT, SIGTERM and SIGHUP clean up: its container
(`docker rm -fv`), plus its
downloaded image if the image was not already present and no other run is using
it; existing containers are left alone. A SIGKILL cannot, so `chore clean` removes
the container by that label. Workers sharing a machine set their own
`RESTMAIL_LABEL=rest-mail-loop=<name>`, so that they clean up only their own. No limit
is placed on parallel runs: they stay out of each other's way by cleaning up.

A change under `helm/restmail/` also runs `scripts/check_chart_publish.py`, which
needs `helm`, once that script is on main.

The gate is fail-fast to avoid expensive work after an earlier failure. The
summary distinguishes `PASS`, `WARN`, `FAIL`, `COULD-NOT-RUN`, `NO VERDICT` and
`NOT REQUIRED`, and its last line gives the verdict:

| Exit | Verdict | Meaning |
|---:|---|---|
| 0 | passed | Every required check passed. Only this counts as passed for enqueue. |
| 1 | failed | A check failed or could not run. Do not push. |
| 3 | `WARN` | No test failed, but a warning stands, such as a test that needed a second process inside a shard's limit. Not passed for enqueue: CI may reach its own limit there. |
| 4 | no verdict | A time limit ended a step. That is not a pass and not a failure of the test it cut off. Run it again. |

It is not a claim that
Helm, cross-builds, frontend builds, image smoke tests, proof-union reports or
the full E2E topology ran. Those remain separate CI jobs. Named Lint/Test/
test-api artifact/sharding exceptions and their reasons live in
`scripts/ci-local-coverage.json`. The Python drift tests fail on added,
removed, renamed or changed workflow steps: implement matching coverage or
record an explicit exception rather than silently refreshing the hashes.

## Where it runs

In the `E2E (full topology)` job (`.github/workflows/e2e-suite.yml`), after the
14 stages pass, against the topology they ran on, as the fixture admin
(`admin@restmail.test`, from `cmd/seed`):

| Phase | What it checks | In CI |
| --- | --- | --- |
| example pairs | Each registered `*WithVertrag` test in `internal/api` sends its documented example pairs and their boundary cases through Vertrag to the API in the test process (`chore contract:pairs`). It needs no topology. | Gate, first |
| `examples` | Each documented request gets its documented answer. Ordered by the spec's links (`--sequence`), so a read, an update or a delete addresses what its create made. | Gate |
| `coverage` | Every boundary each schema implies (the maximum, one past it, a required property missing) gets a 4xx, never a 2xx or a 5xx. Valid probes skipped because an example failed fail it too. | Gate (below) |
| `ignored-auth` check | Every authenticated request, sent again with no credential, is refused. | Gate |
| `authorization` | Every selected operation as each configured principal, checked against the spec's security roles. A signed-in caller refused an object the path names may get the 404 that hides it (#672); `authorization_refusals.py` names any 404 outside that, so an admin route stays 403 and no credential stays 401. | Independent report job, after its E2E stages |
| `ownership` | Each account tenant's principals sent the other tenant's account id, on the operations they are entitled to; every answer must be 403 or 404. | Report only, after authorization |
| live proofs | Named `principal: anonymous` and `body: over_cap` examples, and a bounded matrix of real principals (`chore test:contract-live-proofs`). What they prove joins the in-process proofs; the union is a gate, a failing runner is not ([details](#live-requirement-proofs-1237)). | Independent report job; the proof union still gates |
| `fuzz` | Values drawn from each schema, shrunk to the smallest failing input, with a replayable seed. | Nightly and on `v*` tags (`contract-fuzz.yml`), never blocks; [one issue per finding](#the-daily-tiers-issues-626) |
| `own-data` | The operations moved to a clean start, each exchange after a reset and seed, with what follows `createDomain`'s links ([Moved to a clean start](#moved-to-a-clean-start-part-of-1385)). | Gate, before the teardown |
| faults | The API built with each fault in `faults.yml`, and the phase the fault names run on its operation: it must turn red ([Fault catalogue](#fault-catalogue-654)). | Nightly and on `v*` tags (`contract-faults.yml`), [one issue per failing fault](#the-daily-tiers-issues-626), fails on a fault not caught |

### The daily tier's issues (#626)

`contract-fuzz.yml` and `contract-faults.yml` end with `scripts/contract_tier_issues.py`,
for scheduled runs, `v*` tags and runs dispatched on `main`:

- Each fuzz finding has one issue (label `contract-fuzz`) with the seed that replays
  it: `chore contract:fuzz -- --seed N`, against a topology at the commit it names. Each
  fault that fails has one issue (label `contract-faults`).
- A failure opens the issue, reopens it, or rewrites its latest failing run, which
  notifies nobody. A later finished run in which that transaction passes, or that fault
  is caught, closes it. A fuzz transaction not sent this run leaves its issue as it is.
- A run that did not finish has an issue of its own, and closes nothing else.
- An issue closed as not planned stays closed, so that is how to stop tracking a finding.
- One run opens at most 25 issues; it warns about the rest, and the next run opens them.

Issues are public: values that look secret are scrubbed, and only the seed is read
from the log.

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

The public policy file's `found` pair (`mtaStsPolicyFile`, 200 `text/plain`)
requires `state: active_policy`, which `vertrag.yml` provides because its hook
sets it up: every phase's request for it gets a domain of the hook's own, whose
policy the hook puts (enforce, `mail.example.com`, 604800, active) before each
request, and is sent with `Host: mta-sts.<that domain>`. The policy text and
media type are the pair's, unchanged. Its other requests still ask for the
domain the run created. Part of #1161.

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

The coverage phase runs with Vertrag's `--fail-on-skipped-probes`, which fails it
with exit 2 when non-login valid probes are skipped and deliberately exempts login.
`run.sh` treats that exit as final, on a whole-API run and a narrowed one. The log
names each operation and what its example answered. A skipped case never counts as
a passing boundary probe, and `gate.py` still fails on a skipped result in any other
form.

### A hook cannot change a generated body

Vertrag 0.42.0 ends a generated probe whose body a before hook changed
(`runner.ErrChangedByHook`). `fuzz.Cover` marks it not sent, and the coverage
command then drops it: it is not in the report or the skip count. A hook that
always rewrites a body therefore leaves that operation's valid probes unsent and invisible. A hook also never
sees a `coverage: ` name: the prefix is added to the result, not the transaction.
`CONTRACT_COVERAGE=1` is what tells a hook it is in the coverage process, whose
examples phase and working check it also sees.

So `lost_probes.py` counts them from inside the hook worker, as the last hook of
every request, and `gate.py` holds the counts to `lost-probes-baseline.txt`, per
operation. A new operation or a larger count fails. A probe whose body a hook
changes, or that a hook skips, is a line in `lost-probes.txt`, and a passing gate
prints how many there were. The first two requests of each transaction are the
documented ones, which a hook may rewrite: only a generated body is compared. The
file says why each operation is there. A counted probe was not sent, so it proves
nothing: the count is a limit, not a pass.

There are three ways to keep a probe:

- **`fuzz.pin`** holds a value the schema cannot describe, before the probe is
  rendered. `snoozed_until` and `expires_at` are held in the future because the
  generator's only date-time is in 2020, and the server rightly refuses that
  (`probes.py`). `time_zone`, `script` and `raw_message` are held because the
  generator's `x` and base64 of `vertrag` are not a zone, Sieve or a message.
  `TestQuietHoursCoverageWithVertrag` has its own configuration, so it pins
  `time_zone` itself.
  `script` also reaches `validateSieveScript` and `validateCustomFilter`, and
  `raw_message` reaches `deliverMessage` and `restmailDeliver`. `restmailDeliver`'s
  field is the message itself, not base64, so the handler cannot read a recipient or a
  Date from that value. Holding it also gives that operation its first valid probes, so
  `recipients` and `date`, which only its body has, are held at a mailbox the run has
  and the documented date, and those probes are delivered. `permissions` is held
  at a real capability the probe
  administrator has, because the generator's stand-in is capability `x` at scope
  `x`. A pin is global by field name, so it does not suit `password`, which eleven
  operations have: `fuzz.pin-valid`, below, holds that one. Vertrag does not look through a body's nested `$ref` to see
  what a pin names, so `apiKeyPermission` is an alias that the description holds in
  the body.
- **`fuzz.pin-valid`** holds a value on one operation's valid requests, for a rule
  no schema can say, and the probe is still sent, judged and reported. The
  chosen-password rule refuses the generator's `xxxxxxxxxxxx`, so `vertrag.yml` holds
  a strong `password` on `createAccount` and `updateMailbox`. Invalid requests are
  left alone, so the 11-character refusal is still sent, and a `fuzz.pin` on the same
  field would win; none names `password`. The other operations the rule guards send
  no valid generated password on this topology: their success examples need state
  the run does not have, and `acceptInvitation`'s probes end in the token its hook
  makes for each one. `TestContractValidPinsPassTheRule` holds the value to the rule.
  This setting also puts the password in bodies that had none. On `updateMailbox`,
  `a_new_password_only_on_the_created_account` sends those to the run's own account.
  On `createAccount`, a password with a `password_hash` is refused, so `password_hash`
  is held at the empty string the schema permits, which means no hash. So no generated
  valid request imports a hash live; its refused shapes are still sent, and
  `TestAccountImportPasswordHashAndProtocolSignIn` proves an import in-process.
  The cost is that a valid password no longer varies in these probes, so the boundary
  is proved elsewhere: `createAccount.created` and
  `updateMailbox.updated_password_at_minimum` each send a strong password of exactly
  12 characters in the examples phase, which the surface gate requires, and
  `TestCreateAccount_ContractPasswordBoundary` and
  `TestMailboxUpdate_ContractPasswordBoundary` hold both sides.
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
  `importMessage`'s documented account is 12, and on the topology the E2E stages leave
  that one at its quota, so its new message answers 409 and skips the valid probes.
  In the coverage process its success requests go to the fixture admin's account.
  In every other process only `always_store` and `match_submission` go there: the
  rest store or skip one message, which fits, but those two would store another.

The name of `updateMailbox.updated_password_at_minimum` matters. Coverage takes an
operation's first 2xx example, by name, as the base of its probes, and the probes of
the path send that base unchanged to id 1, a fixture account (`eve@restmail.test` on
the seed), as the ownership runs' `PATCH` cells send it to their own ids. A base with
a password would land on those accounts or be moved or skipped by the hook above, and
everything after it that needs the account fails: delivery to it, its API keys,
signing in as it. So `updated` stays first, and `TestMailboxUpdate_ExamplePairs` holds
it. The body probes go to the run's own account, because `fuzz.pin-valid` gives each
valid one a password, so id 1 is not made inactive or given a quota of -1. The probe
of the path at its minimum still writes `updated`'s `display_name` to it.

A pin takes away the probes that vary its own field, so the named examples prove
those refusals in the gated examples phase, which is not pinned. For snooze:
`past`, `malformed_time`, `snoozed_until_missing` and `snoozed_until_wrong_type`.
For `createAPIKey` and `updateAPIKey`: `permissions_missing`, `permissions_empty`,
`permissions_too_many`, `permissions_wrong_type`, `permission_scope_missing`,
`permission_capability_wrong_type` and `permission_unknown_field`. For
`createAPIKey` too: `expires_at_malformed`, `expires_at_wrong_type` and
`expires_at_past`. For `time_zone`: `invalid_zone`. For `script`: `script_missing`,
`script_null`, `script_wrong_type` and `invalid_syntax` on the Sieve operations, and
`script_missing`, `script_null`, `script_empty` and `script_wrong_type` on
`validateCustomFilter`. For `raw_message`: `missing_raw`, `raw_wrong_type` and
`invalid_base64` on `importMessage`, `raw_message_invalid` and `raw_message_nul` on
`deliverMessage`, and `raw_message_wrong_type` on `restmailDeliver`. For `recipients`:
`recipients_missing`, `recipients_wrong_type` and `recipient_wrong_type`. For `date`:
`date_wrong_type`. A pin engages only
on a body with no nested reference, so `filter_name` cannot hold `testFilter`.
`deliverMessage`, and `raw_message_wrong_type` on `restmailDeliver`. For `filter_name`:
`filter_name_missing`, `filter_name_empty` and `filter_name_wrong_type`. For `cert_pem`
and `key_pem`: `cert_missing`, `cert_empty`, `cert_wrong_type`, `cert_invalid`,
`key_missing`, `key_empty` and `key_wrong_type`.

A pin engages only on a body whose schema holds its fields at the top, and a body that
is a reference to a schema with references of its own does not (Vertrag 0.42.0,
`fuzz/pin.go`). `testFilter`'s email has to stay a reference, because its parts refer to
themselves. So its description states the body in the operation
(`describedTestFilterBody`), which also says what the handler has always required of
`config`: an object. A generator that may draw any `config` draws `"x"`, which the
handler refuses, so `filter_name` alone would not have sent these probes.

`createCertificate`'s `cert_pem` and `key_pem` hold a current, matching self-signed
certificate that `probes.py` makes for each run. Its valid-only `domain_id` pin names
the inactive `contract-certificate-probes.invalid` fixture domain, not the gateway's
`restmail.test` domain. Missing, malformed and below-minimum IDs are not pinned.
Expired material is not a substitute: the upload handler rejects it (#1704).
`a_self_signed_certificate` leaves generated bodies as drawn, and the first hook
after them, `remove_the_probe_certificates`, deletes certificates with the probe
issuer. The documented sends use the run's disposable domain, or the isolated
certificate fixture domain if no run domain has been stashed, with their own issuer
so `getCertificate`'s link still finds them after probe cleanup. Their exact SAN is
the upload domain, never either gateway's hostname, so they cannot poison gateway
TLS even between creation and cleanup. A run narrowed to `createCertificate` alone
can leave probe certificates on its disposable database without affecting gateways.

`createDomain` needs no pin, because its setup makes the generated name free. Its
hook removes what earlier probes put at `example.com`, and the first hook after its
probes hosts that domain again through the fixture, so what follows finds it live.

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

### Discovery of the run's hosted domain

The report-only fuzz configuration holds `restmailDiscover`'s query `domain` at
`fixtureplan/defaults.json`'s `domain` (`restmail.test`), which the run hosts.
Discovery correctly returns 404 for an unhosted domain. Vertrag 0.42's `fuzz.pin`
holds body fields and GraphQL arguments, not query parameters, so `probes.py`
uses its equivalent for this parameter: a fuzz-only copy of the description,
`fuzz-openapi.json`, with this one query schema's `enum` and parameter example
values held at the hosted domain, just as a body pin also holds example values.
Valid generation cannot draw an unhosted name; invalid generation still probes
refusals. It does not rewrite requests, exempt findings or change other `domain`
parameters. The published description, examples gate (including the unhosted 404),
coverage, gates and baselines are unchanged. `test_probes.py` proves that scope
and that a missing parameter fails instead of silently losing the hold.
Live-description mode derives this fuzz-only copy from the identical saved
snapshot. The fuzz report writer and unmatched-body-pin retry preserve the copy
instead of restoring `CONTRACT_RUN_SPEC`; examples and coverage still use their
original descriptions.

### Missing account settings

The Sieve read and delete return 404 when no script is stored; vacation disable
returns 404 when no reply is stored. Their named absence pairs use an isolated
account created through the public API by `account_settings_hooks.py`, loaded
from `entrypoint.py`. It changes only the account path for the fixture admin's
explicit setting-absent 404; account-not-found, other principals, success exchanges
and validation probes are untouched. The private fixture is reused per origin
and forgotten on a clean-start reset. Success setup must still install the
script or set the vacation reply before reading/deleting it. The database-backed
`TestAccountSettingsAbsentLifecycle` proves that setup, successful removal and
subsequent absence, including stored empty/inactive scripts and disabled replies.
The same file stores the setting before the fixture admin's `getSieveScript` and
`deleteSieveScript` 200 and `disableVacation` 204, because the example and coverage
phases can reach them with nothing stored, and they now answer 404 without it.
`test_account_settings_hooks.py` proves registration, scope and setup failures.

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

The single bounce has the same need, because a bounced entry is refused (#1567).
Its success takes the held bounce entry, or a new one from
`chore test:contract-queue-entry-fixture` when a bulk bounce took it. Its 400
gets a new entry bounced first, so the refusal never takes the held one.

The hook replenishes that one row and nothing else. `chore test:contract-fixtures`
also adds a quarantined message and a suppression, and those counts decide what
coverage reaches: `clearSuppression` and `deleteQuarantine` hold two skipped
probes each because coverage's example finds no fixture left. A spare row lets
that example pass, so the probes run and the skipped-probe baseline no longer
matches. For `clearSuppression`, the next probe with the example's path then
finds nothing to clear.

### Reserved alias refusal

`checkMailboxAddress`'s `alias_refused` example needs an inactive alias, not an
absent address. `alias_refusal_hooks.py`, registered by `entrypoint.py`, creates
a unique alias on the existing `restmail.test` domain through the public admin
API and disables it before changing only that example's address query. Setup
uses `CONTRACT_ENDPOINT` (the topology's public listener by default), not the
gateway's internal mTLS listener. No body, credential or expected answer changes.
The after hook deletes exactly that alias, including after a failed exchange,
so later alias lists/counts, ownership cells and domain-delete fixtures retain
their state. No domain or mailbox is created or modified. Coverage's generated
queries do not match the named refusal's query and expected status; they are
sent unchanged, with no probe hidden by a body rewrite or added requirement.

The refusal is a 403, not a 5xx. The API uses REST semantics and the SMTP gateway
turns them into SMTP (the owner's decision on #1533), and the surface gate never
counts a 5xx as passed. The gateway answers its SMTP peer 550 for this 403.

The #1539 local regression used the rebased PR's API build, disposable
`postgres:16-alpine`, the seeded `restmail.test` domain, these hooks and
checksum-verified Vertrag 0.42.0, with the description narrowed to this route and
`surface: {count: passed, require: 100}`. It passed 1 of 1 operations and exited 0:
the 200, 403 and 422 examples passed, and the 500 example needs a database fault,
so it is unproven there and proven by `TestMailboxLookupExamplesWithVertrag`. The
same surface rule against a loopback fixture answering HTTP 550 exited 3 (#1533).
The inactive alias returned the fixed public sentence, the detailed reason appeared
only in the API log, and no temporary refusal alias remained afterwards.

An earlier local run, with the refusal still at HTTP 550, also covered the
neighbouring operations. It passed all 54 selected alias, domain and mailbox
examples, and left the ownership runs' cells unchanged: domains and aliases 9 of 16
passing with the same 7 unsent, mailboxes 10 of 10. These counts were not repeated
for the 403, and neither run is a claim about the whole-API merge-queue gate.

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
coverage's skipped-probe check depends on the single rows being spent.

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
keep theirs. `linkAccount` refuses a password-only session 403
`two_factor_required`, as its 403 and its `two_factor_required` example
document. The authorization phase's `ordinary` principal is one, and with no
roles where the operation names no capability the phase would expect its 201,
so `hooks.py` skips that cell (`no_link_from_a_password_only_session`, #1942):
it is reported unsent, and the refusal is proven in-process. `createAPIKey` is made with that administrator's password, except in
keep theirs. That phase leaves a request unsent when a hook changes its
credential, so its `admin` principal is not swapped: it signs in as the
two-factor administrator from the start (`authorization.py`, #1162). `createAPIKey` is made with that administrator's password, except in
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
- **coverage, Vertrag's verdict, then `gate.py`:** any failed probe or skipped
  valid probe exits 2, and `run.sh` treats that as final. `coverage-baseline.txt` is
  empty and can excuse nothing now. `gate.py` still fails on a listed operation
  whose probes all pass, and on the probes a hook ended beyond their baseline. It
  also fails when `exclude-operation-id` and
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

`createAccount` (`address_missing`), `restmailDeliver` (`recipients_missing`, `message_id_invalid`),
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

`openGatewaySession` uses the E2E fixture's app password with its named requests:
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

The browser-session checker (`tests/contract/browser_sessions`) is a required
gate already, the first step of `run.sh gate`, so its CSRF refusals can be
proven now. `create_account_csrf_skipped` gets a cookie session's
`POST /api/v1/accounts` past the CSRF middleware: before the router sees the
request, the switch gives it a matching `restmail_csrf` cookie and
`X-CSRF-Token` header, whatever the client sent. A request without the session
cookie, every other route and every other cookie and header are left as they
are. The `browser_session` phase runs the narrowed gate and judges only the
checker, by its output line and its receipt (`browser-sessions.json`). The fault
is caught only when the checker failed `createAccount`'s refusal of a request
lacking CSRF (`expected 403 csrf_failed`) and its receipt ends with that check.
The baseline needs the checker passed, with `createAccount` refusing each of
the three requests lacking CSRF with 403. A failure of any other check, or one
it could not run, is an error, not a verdict.

The whole-API authorization and ownership phases are still reports, so they
witness no fault. The other authorization faults are caught by a required gate
instead: `chore contract:pairs`, the in-process example pairs a contract run
starts with. Those tests send a pair that says a principal is refused, through
`NewRouters`, the real router, with Vertrag. A guard sits inside the server, out
of a build tag's reach, so these faults are a `patch`: a diff removing the real
guard, in a throwaway `git worktree` of HEAD (never the running checkout, whose
API rebuilds itself when it changes). The `pairs` phase runs the entry's `test`,
narrowed to its `subtest`, first on HEAD (green) and then with the patch, where
it must fail with `expected_failure`. A patch that no longer applies or does not
build, a subtest that did not run, and a failure for any other reason are not a
caught fault.

| Fault | What a client sees | Vertrag's failure |
|---|---|---|
| `get_domain_capability_check_removed` | The route still documents its capability check, and holds none: another domain's administrator reads this domain | `GET /api/v1/admin/domains/{id} 403 answered 200` |
| `domain_scope_widened_to_any_grant` | A capability held on any domain passes on the target, as if held at `*`: another domain's administrator reads this one's custom filter | `GET /api/v1/admin/custom-filters/{id} 403 answered 200` |
| `domain_scope_checked_at_all` | A check on the domain is made at `*`: the domain's own administrator is refused | `GET /api/v1/admin/domains/{id}/allowlist 200 answered 403` |
| `get_quota_may_reach_skipped` | The handler skips its `mayReach` check: a key scoped to another mailbox reads this account's quota | `GET /api/v1/accounts/{id}/quota 404 answered 200` |

`faults.py pairs` runs only these. It needs `RESTMAIL_VERTRAG_BINARY` and no
topology, so it can run on a laptop.

### Adding a fault

A fault that removes a guard inside the server is one change, to `faults.yml`:
an entry with `name`, `operation`, `path`, `phase: pairs`, `breaks`, `test` (a
`*WithVertrag` test `chore contract:pairs` names), `subtest`, `expected_failure`
and `patch`, a `git diff` with a short context (`-U1`). `go test
./internal/faultinject` holds the patch to the code, so a change to the guarded
lines names the fault it broke, and `faults.py pairs` says whether it is caught.

A fault that changes an answer takes a switch file and an entry:

1. Write `internal/faultinject/fault_<name>.go`, starting
   `//go:build faultinject && fault_<name>`. Its `init` registers a `Fault`:
   the route's method and path, a `Break` that rewrites the recorded answer
   (`renameField`, `replaceField`, `addDataField` and `setMediaType` are in
   `faultinject.go`), and an `Example` of the route's normal answer for the
   package test. Set `ExampleStatus` when that answer is not 200. A fault that
   repeats what the client sent uses `BreakSeeing`, which is given the request
   body as well, with `ExampleRequest` for the package test. A fault that
   gets a request past a guard in front of the route changes the request with
   `BreakRequest` instead, and needs its own package test of that guard. A switch
   disabling a real guard uses `Configure` instead of `Break`; only tagged
   builds apply it to the configuration before constructing the router. The
   switch check exercises the configured limit middleware as well as the
   response-rewriting faults. A normal build's configuration hook is a no-op.
2. Add its entry to `faults.yml`: `name`, `build_tags`
   (`faultinject,fault_<name>`), `operation`, `path`, `phase` (`examples`,
   `body_cap`, `auth_throttle`, `secret_scan` or `browser_session`) and `breaks`,
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
  folder for each rename probe (a successful rename consumes the previous one;
  a probe on an account the run cannot reach is left for the route to refuse),
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
  it removed. These hooks look for the account through every page of the
  mailbox list: on the topology it sits past the first page by the ownership
  run, where a look at one page called it absent and its create answered 409.
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
  removed after the run and is never uploaded. It provides states the
  examples phase does not (#1162). `recoverable_account`: recoverAccount names
  its account in the body, so `hooks.py` makes a fresh account, enrolled in
  TOTP, for each principal's request and recovers that one. `owner_key`: just
  before the phase, `chore test:contract-owner-key -- add` makes a throwaway
  Ed25519 key (`owner_key.py`; the seed stays in the private, never-uploaded
  `.cache/contract/owner-key.json`) and registers its public half with the
  documented operator action, `restmail-api owner-key add`; after the phase,
  `-- revoke` revokes it and deletes the seed, so the gate and later phases see
  no owner key. `hooks.py` signs each owner success with it as every
  principal: ownerStatus on a fresh challenge; ownerToken for an account made
  for that request; resetAdmin redeems a token minted for such an account, so
  no token for one of the run's own accounts is minted or spent. The refusals
  that require the key are sent on a real fresh challenge as well, so the
  placeholder one does not refuse them first: `not_status_key` and `not_owner`
  signed by a key made for that request and registered nowhere, `no_account` by
  the run's key for a fresh address no account has. The owner endpoints'
  409s (`no_keys`) require the opposite, `no_owner_keys`: a server that
  lists no owner keys and no status keys. The phase does not provide it, as
  its key is registered, so it does not send them; the examples phase does
  not provide it either, as `CONTRACT_ENDPOINT` may name a server with keys.
  `TestOwnerRecoveryRefusalExamples` proves them on a server with an empty
  owner registry. `eligible_grant` and
  `active_grant`: a grant change acts on the caller's own grant, so before the
  two-factor administrator's request (the admin principal, and the examples'
  sender) `hooks.py` makes it a grant in the state the example names (an admin
  role at a domain no one hosts, which gives a superadmin at `*` nothing new),
  switched on first where the example needs a running activation, and removes
  it after the request. `existing_link`: unlinkAccount gives up, and
  linkAccount's `already_linked` meets, a fresh account linked to that
  administrator just before. `grant_holder`: createGrant's `granted` names an
  account made for that request, so it no longer requires
  `two_factor_session`, which the phase does not provide: other examples name
  it for changes to the run's own data. The other principals hold no second
  factor, so their grant and link requests are refused as documented.
  `event_stream` (accountEvents) is not provided: the stream stays open while
  the session lives. States of the
  caller's own account (`known_password`, `enrolled_authenticator`,
  `pending_enrollment`) are not provided: their success would end or change the
  principal's own sessions. Its unproven list is
  `.cache/contract/unproven-authorization.json`.
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
- `coverage_bases.py`: names the operations whose coverage probes start from a different example than on `origin/main`, without a server. `chore contract:bases` sends them.
- `test_gate.py`: `gate.py`'s own tests (`python3 -m unittest discover -s tests/contract -p test_gate.py`).
- `test_hooks_save_draft.py`: the saveDraft sender hook's tests, against a stub admin API.
- `test_hooks_rename_folder.py`: the renameFolder source-folder hook's tests, against a stub admin API.
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
domains, credentials and own grants survive, as does the server capability
ceiling's grant, with its set back to every capability, and so do
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
domain. `updatePipeline`, which follows `createPipeline`'s link, has moved too:
an update to a positive id other than missing's, body refusals included, updates
an inactive pipeline of its own on a domain of its own, made after its reset
(`update_a_pipeline_of_its_own`); `0`, an undecodable id and missing's
`2147483647` keep theirs. `listPipelines`, `listPipelineLogs` and
`getPipelineAnalytics` reset and seed before each exchange as well
(`pipeline_reads_after_a_reset`). A listing that succeeds gets a pipeline of its
own on a domain of its own; the logs and the analytics need nothing made. No
link starts from or reaches the three reads. A run with no reset changes
nothing in these hooks.

The server settings, `listRoles`, `listCapabilities`, `getServerLimits`,
`updateServerLimits`, `getServerCapabilities` and `updateServerCapabilities`,
reset and seed before each exchange too, refusals included
(`server_settings_after_a_reset`). None needs anything made, and no link starts
from or reaches any of them. The reset empties `server_limits`, so the limits are
back at their defaults, and puts the server capability ceiling back to every
capability, so a ceiling one exchange narrows does not bound the next.

`listDmarcReports`, `listTlsReports`, `listAuditEntries`, `verifyAuditLog`,
`getDeliveryLogs`, `getDashboardStats` and the TLS report intake
`receiveTlsReport` reset and seed before each exchange too, refusals included
(`report_and_audit_reads_after_a_reset`, registered ahead of their other
hooks). A listing of TLS reports that succeeds gets a report of its own,
received after its reset on the seeded `restmail.test`; the intake hosts the
domains its report names in its own hook, after the reset. The others need
nothing made. The audit log is append-only and kept by the reset
(`resetRetainedTables`), so its reads meet what is already in it. No link
starts from or reaches any of them. `getDmarcReport` has not moved: its success
needs the received DMARC report the Go fixtures make, and no API call makes one,
since a report arrives only in mail with an aligned DMARC pass. A run with no
reset changes nothing in this hook.

`listFolders`, `listMessages` and `searchMessages`, the mailbox reads
`listAccounts`' link reaches, reset and seed before each exchange too, refusals
included (`a_mailbox_read_after_a_reset`). Their examples name account 1, the
fixture admin's mailbox, which the reset seeds again under another id, so an
exchange aimed at a positive account other than missing's `2147483647` goes to
that mailbox, looked up by address. A listing that succeeds gets something of
its own, made through the API after its reset: the folder list a folder, the
folder's page a message imported into that folder, and a search a message whose
subject holds the query's words. `0` and every documented 404 keep their ids.
`listAccounts` is not selected, and no link starts from any of the three. A run
with no reset changes nothing in this hook.

`getAttachment` resets and seeds before each exchange too, refusals included
(`on_an_attachment_of_its_own`). Its examples name attachment 1, which the reset
does not make. A read of an attachment that is there (the whole file, the single
and multiple ranges, the 304 and 412 conditionals and the 416 range) is sent to
an attachment of its own: a message holding `ATTACHMENT_BYTES` as an attachment,
imported into the seeded `admin@restmail.test` through the API after the reset,
its attachment's id read back from `listAttachments`. Its after hook
(`the_attachment_answers_its_bytes`) holds the answer to those bytes: the whole
body, each requested range with its `Content-Range`, and the 416's
`bytes */<length>`. Refusals keep their ids (`0`, missing's `2147483647` and the
others' own). No link reaches it. A run with no reset changes nothing in either
hook.

The delivery and federation operations reset and seed before each exchange
too, refusals included (`delivery_after_a_reset`, registered ahead of their
other hooks). They are the gateway's `checkMailboxAddress` and
`deliverMessage`, and RESTMAIL's `restmailDiscover`, `restmailCapabilities`,
`restmailCheckMailbox` and `restmailDeliver`. The topology sends the gateway
routes to the API's internal listener, which serves nothing else. So the hooks
make their resets and data on the run's endpoint, which run.sh names in
`CONTRACT_SETUP_ORIGIN` (`setup_origin`).

Each documented success of a check or a delivery goes to a mailbox of its own
on `restmail.test`, made after the reset:
- the gateway's found check;
- the gateway's deliveries to `admin@restmail.test`;
- the RESTMAIL check's 200, whatever address Vertrag gives it (#1801);
- the RESTMAIL delivery to `eve@restmail.test`. This one also gets
  `restmail.test` an active inbound pipeline that only validates headers. After
  a reset the domain has none, and the default pipeline greylists a new sender,
  which defers the delivery 503 for five minutes.

Their after hook (`the_answer_names_the_recipient_made`) fails a success unless
the answer is for that mailbox:
- a check must say it exists, with its address, and from the gateway its id;
- a gateway delivery must be stored in it;
- a RESTMAIL delivery must name it delivered, with nothing failed.

Refusals keep what they name: an empty or missing address,
`nobody@restmail.test`, missing's `2147483647`, `unavailable@example.com`.
Discovery asks about the seeded `restmail.test`, and the capabilities need
nothing made. No link starts from or reaches any of them.

`acmeChallenge` has not moved. The server registers the route, and describes
it, only with ACME on, which the topology is not, so no run sends it. A run with
no reset changes nothing in these hooks.

A listing's 200 has the same shape with no rows as with the one made for it, so
its status and schema alone pass on an empty list (#1785). After a reset,
`listTlsReports`, `listPipelines`, `listThreads`, `listFolders`, `listMessages`
and `searchMessages` must list what their before hook made, or the exchange
fails (`the_listing_holds_what_was_made`, through the transaction's `fail`
field): the TLS report by its reporter and policy domain, the pipeline by its
id, the awaiting thread by its subject, which names it by the time it is made,
the folder by its name, the folder's message by its subject and folder, and the
found message by its subject. A documented 200 with nothing made for it fails too. The
threads page after the example's cursor holds older threads only and is empty
by design, so its hook notes that it lists none of it. Refusals and a run with
no reset are left alone. The other moved listings have nothing an API call can
make for them after a reset (`listDmarcReports`, `getDeliveryLogs`,
`listPipelineLogs`, `listAuditEntries`), or list what the seed holds
(`listMailboxes`, `listGrants`, `listMyGrants`, `listContacts`), or list a
domain or message of their own that holds no rows (`listSuppressions`,
`listAllowlist`, `listBlocklist`, `listAttachments`).

`testFilter`, `listCustomFilters` and `validateCustomFilter` reset and seed before
each exchange too, refusals included (`filter_requests_after_a_reset`, each
operation's first hook). A listing that succeeds gets a custom filter of its own;
the preview and the validation take only a body and need nothing made. No link
starts from or reaches any of them.

The public reads (`getHealth`, `describeServer`, `apiRoot`, `swaggerUI`,
`openapiSpec`, `openapiDocument` and `prometheusMetrics`) and the mail client
discovery reads (`mozillaAutoconfig`, `mozillaAutoconfigWellKnown` and
`microsoftAutodiscover`) reset and seed before each exchange too, refusals
included (`public_reads_after_a_reset`, each operation's first hook). The public
reads need nothing made. The discovery reads need the address's domain hosted
and active: `an_active_domain_for_autoconfig` activates it after the reset, as
the autoconfig reads' own later hook, and for an autodiscover that succeeds from
the reset hook (its request is still sent as generated). No link starts from or
reaches any of them.

The domain administration lists (`listDomains`, `listAliases`, `listDKIMKeys`
and `listCertificates`) and the certificate operations (`createCertificate`,
`getCertificate` and `deleteCertificate`) reset and seed before each exchange
too, refusals included (`domain_administration_after_a_reset`, each operation's
first hook). A listing that succeeds gets a row of its own after the reset: a
domain, an alias on a domain of its own, a DKIM key on a domain of its own, or a
certificate on a domain of its own. A read or a delete of a certificate aimed at
a positive id other than missing's gets a certificate of its own, since the one
createCertificate's link names is gone. The create needs nothing made: its
`domain_id` 1 is the seeded `restmail.test`. createCertificate's link reaches the
read, and the read's the delete; no other link starts from or reaches any of
them. `clearSuppression` is not moved: a suppression is made only by the Go
fixtures, which the reset does not run.

After a reset, the 200 of `listCustomFilters`, `listDomains`, `listAliases`,
`listDKIMKeys` and `listCertificates` must list the row its before hook made
(`the_listing_holds_what_was_made`, #1789), since the status and the schema
alone pass on an empty list. The before hook notes the row in
`hooks.stash["listing_made"]`; a documented 200 with nothing noted fails too.
Refusals and a run without a reset are left alone.

The account link operations (`linkAccount` and `unlinkAccount`) and the token
exchange (`issueClientCredentialsToken`) reset and seed before each exchange too,
refusals included (`account_links_and_the_token_exchange_after_a_reset`, each
operation's first hook); the client secret operations reset with the credentials
(`a_credential_to_manage`). None needs anything made. The link's 201 links the seeded `eve@restmail.test`,
which the reset puts back unlinked with the seed's password, so after a reset
`link_a_seeded_account` no longer undoes an earlier link. The rest are refusals,
which keep their ids and bodies. linkAccount's link reaches only the unlink; no
other link starts from or reaches any of them. `listAccounts` and `getAccount`
are not moved: `listAccounts`' link reaches operations not moved yet, and
`getAccount` among them. Nor are the push device operations: without an APNs
key their routes do not exist, so the run sends none of them.

The sign-in and recovery operations reset and seed before each exchange too,
refusals included (`sign_in_and_recovery_after_a_reset`, each operation's first
hook): `login`, `logout`, `refreshToken`, `openGatewaySession`, `resetAdmin`,
`recoverAccount`, the three owner operations, the five two-factor and password
operations, and the six recovery email operations. No link starts from or
reaches any of them. Refusals keep their bodies and tokens. A success that ends
a session, rotates one or starts a second factor acts on an account made for
it after the reset, with a password of its own, never on a session or
credential the run uses: `logout`'s 204 ends a session of that account, and
`the_session_it_ended_is_over` fails the exchange unless its refresh token is
refused afterwards; `refreshToken`'s 200 rotates that account's refresh token;
`enrollTwoFactor`'s 200 starts that account's enrollment. `openGatewaySession`
signs in with an app password the two-factor administrator mints for an
account made for it, since the reset removed the principal
`sign_in_a_mail_client` names. The other successes read, sign the fixture
admin in, or ask about an address no account holds, and need nothing made. The
local run needs the topology's `RESTMAIL_RECOVERY_EMAIL_BASE_URL`
(`https://restmail.localhost`): without it `requestRecoveryEmail` answers 503.
The reset also drops a kept administrator's enrollment that was never
confirmed, with its recovery codes, so the primary run's enrollment of the
fixture admin does not come back after it (`TestCleanResetPendingEnrollment`).

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
nothing that follows it is left unmoved, and it gets no stand-in domain.
Whether a domain is certified is read
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
to a domain of its own (`contract-own-domain.test`), not a stand-in. A list reads that domain's empty list,
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
only a send writes a trace. The trace is written in the background, so the
hook asks for it until it is there (`a_traced_message`, up to 10 s). The API
keeps a delivered message's trace only at `TRACE_SAMPLE_RATE`, 0.1 unless set,
so a server the own-data phase runs against needs `TRACE_SAMPLE_RATE=1`, as
`tasks/api.yml` sets for the topology; without it the hook names the setting.
`getThread` reads the mailbox and thread of a
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

`createDomain` and the rest of what its link reaches move too, with
`deleteDomain` and `mtaStsPolicyFile`, so nothing that follows `createDomain` is
left unmoved and `own_data.py` has no list of unmoved followers of it. Each
exchange, refusals included, resets and seeds first. The documented successes of
`domainDNSCheck`, `getDomainRelay`, `setDomainRelay` and `listSuppressions` are
sent to a domain of its own (`a_domain_setting_of_its_own`); their refusals keep
their ids (`0`, `not-an-integer`, missing's `2147483647`, and the relay's 422s on
the seeded `restmail.test`, id `1` again after the reset). `createDomain`'s
documented create meets no leftover after its reset. `deleteDomain`'s 204 deletes
a domain made for it, and `has_accounts` names the seeded fixture domain.
`mtaStsPolicyFile` asks for a domain of its own, with an active policy for the
200 and an inactive one for the documented `inactive_policy` 404. The
`linked_domain_ids` stash value lost its last reader with these, so it is gone,
and `start_clean` keeps only the sessions and the reset count.

The mailbox administration operations, `createAccount`, `getMailbox`,
`updateMailbox`, `deleteMailbox`, `listMailboxes` and `endMailboxSessions`, reset
and seed before each exchange too, refusals included (`a_mailbox_to_administer`,
registered before the operations' other hooks). `createAccount`'s documented
create then meets no earlier probe's account, and a list reads the seeded
mailboxes. An exchange aimed at a positive mailbox id other than missing's
`2147483647` gets an account of its own on the seeded `restmail.test`
(`contract-own-mailbox-<time>@restmail.test`, made with the two-factor session),
because both the `found` example's `1` and the id `createAccount`'s link names are
gone after the reset. That includes `updateMailbox`'s 422, so it is refused for its
body and not for a mailbox that is gone. `updated_password_at_minimum`'s 200 sets
its password on that account too: `a_new_password_only_on_the_created_account`
has no created account after a reset, and would otherwise leave it unsent.
`0`, an undecodable id and missing's id keep theirs. `createAccount`'s link reaches only the read, update and delete, and
no link reaches the list or the sessions' end, so nothing that follows these is
left unmoved. `created_account` stays, because without a reset (the primary run
and coverage) the read, update and delete still follow the created account to its
current id, made again when a probe removed it; the reset clears it, so it carries
nothing across one.

The thread and import operations, `listThreads` (the threads awaiting a reply),
`setThreadDue`, `snoozeThread` (a snooze and its cancel) and `importMessage`,
reset and seed before each exchange too, refusals included
(`on_the_admins_mailbox_after_a_reset`, registered before the operations' other
hooks). Their examples name account `1` as the fixture admin's own mailbox, so an
exchange aimed at a positive account other than missing's `2147483647` goes to
the fixture admin's mailbox, looked up by address, whichever id the reset kept it
at. A `listThreads` success gets a thread awaiting a reply of its own: a message
from the fixture admin, imported into its `Sent` folder after the reset. The due
date's and the snooze's thread is imported after the reset by their own hooks
(`thread_due_hooks.py`, `thread_snooze_hooks.py`, registered after this one), into
the account this leaves in the path. `0`, an undecodable id and every documented
404 keep their ids. No link starts from or reaches any of these, and none of them
reads or writes a stash value. The due dates and snoozes live in tables the reset
empties.

The account settings operations reset and seed before each exchange as well,
refusals included (`account_settings_of_its_own`): `getVacation`, `setVacation`,
`disableVacation`, `getSieveScript`, `putSieveScript`, `deleteSieveScript`,
`validateSieveScript`, `getQuietHours`, `setQuietHours`, `getAccountSendLimits`,
`setAccountSendLimits` and `getQuota`. The examples name account `1`, which after
a reset is the seeded `eve@restmail.test`, an account the fixture admin's session
does not reach (404). So an exchange aimed at a positive account id other than
missing's `2147483647` gets an account of its own on the seeded `restmail.test`
(`contract-own-settings-<time>@restmail.test`, made with the two-factor session),
the 422s included. What the exchange reads, replaces or removes is made on that
account after the reset: a script for the Sieve read and delete, and a vacation
reply for the disable. The install's 201 needs none; its 200, a replace, has no
example of its own and is not sent in this phase. `0` and
missing's id keep theirs. The hook is registered before these operations' other
hooks, except for the quiet hours: `entrypoint.py` registers it after their own
hook, which fixes their time zone. In a run with no reset that hook also sends the
examples' `1` to the first account the fixture admin lists; with a reset asked
for it leaves the `1`, so the quiet hours' successes reach only the account made
after the reset, and fail without it (#1767).
`putSieveScript`'s link reaches the read and the read's the delete, and no other
link reaches or starts from these, so nothing that follows them is left unmoved.
No stash value is involved. The reset also puts the retained administrators'
account settings (quiet hours, the contact setting, the quota) back to a new
account's, as it does for their domains' (`resetMailboxSettings`, held to every
column of `mailboxes` by `TestCleanResetEveryMailboxColumn`).

The delivery queue, held and scheduled mail operations, `listQueue`,
`getQueueStats`, `getQueueEntry`, `retryQueueEntry`, `bounceQueueEntry`,
`deleteQueueEntry`, `bulkRetryQueue`, `bulkBounceQueue`, `bulkDeleteQueue`,
`listHeldMessages`, `releaseHeldMessage`, `listScheduledMessages` and
`cancelScheduledMessage`, reset and seed before each exchange too, refusals
included (`queued_and_held_mail_of_its_own`, registered before the operations'
other hooks). What each needs is made after the reset through the API, never in
the database:

- A queue entry is a send from the fixture admin to a reserved `.invalid` address
  (`contract-own-queue-<time>@queue.invalid`), which the server queues for the
  internet. The documented list asks for that recipient, so `listQueue`'s links
  lead to the entry. A success of the read, retry, bounce or delete gets an entry
  of its own, and retry's 400 (`already_bounced`) gets one bounced first, so it is
  refused for that and not for an entry that is gone.
- A bulk action's empty filter selects every entry, so it gets one to act on. The
  documented filter selects nothing by design and needs nothing. After the empty
  filter's 200, `the_bulk_action_changed_the_entries_made` holds its `affected` to
  the number of entries made for it, all pending and so eligible for each action,
  and reads each back: pending after a retry, bounced after a bounce, gone after a
  delete. A wrong count, an entry not changed, or no entry made fails the exchange
  (`transaction["fail"]`), so `affected: 0` no longer passes on its shape (#1772).
- `listHeldMessages`' 200 (`none_held`) names an account that holds nothing, and
  the reset removed `1`, so it gets an account of its own
  (`contract-own-held-<time>@restmail.test`).
- The scheduled messages' own hooks schedule theirs through the API after the
  reset.

`0`, an undecodable id, missing's `2147483647` and the other refusals keep their
ids. The fixture queue hooks (`list_the_sent_messages_queue_entry`,
`retry_a_held_entry_after_bulk_examples`, `bounce_the_held_bounce_entry`) stand
aside after a reset, because the fixture entries they look for are gone. No stash
value is involved.

The credential operations, `listCredentials`, `createAppPassword`,
`revokeCredential`, `listAPIKeys`, `createAPIKey`, `updateAPIKey`, `revokeAPIKey`,
`listAdminAPIKeys`, `listClientSecrets`, `createClientSecret` and
`revokeClientSecret`, reset and seed before each exchange, refusals included
(`a_credential_to_manage`, registered before their other hooks). An exchange aimed
at a positive account id other than missing's `2147483647` gets an account of its
own on the seeded `restmail.test` (`contract-own-credential-<time>@restmail.test`),
the 422s included, because the example's `1` and the id a link names are gone after
the reset. A revoke aimed at a positive credential id other than the missing one
also gets the app password, API key or client secret it revokes, made on that
account with the two-factor session, refusals included, so the run never revokes a
credential it signs in with. The edit already makes the key it changes. `0`, an
undecodable id and the missing ids keep theirs.
The reset keeps only the retained administrators' password, authenticator and
recovery codes (`resetCredentialKindsKept`), so an app password, API key or client
secret made before a reset is not listed, and does not work, after it. The ban
operations, `listBans`, `createBan`, `deleteBan` and `unbanIP`, reset first too
(`a_ban_to_manage`); the documented delete gets a ban of its own, and its 404
keeps its id. The create's and the unban's own hooks still give them the state
they document. No stash value carried these across a reset, so none was removed.

This happens only with `CONTRACT_RESET_SEED=1`, which `tests/contract/run.sh own-data`
sets, and that phase is a gate of its own, sent by `chore test:contract-teardown`
just before the teardown. It sends the moved operations with everything that follows
`createDomain`'s, `createPipeline`'s, `createAlias`'s, `createCustomFilter`'s,
`createFolder`'s, `createContact`'s, `sendMessage`'s, `saveDraft`'s,
`createAccount`'s, `putSieveScript`'s, `listQueue`'s, `createAppPassword`'s,
`createAPIKey`'s and `createBan`'s links
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

A reset removes the pipeline `createPipeline` made, and its id is what the link
carries. `updatePipeline`, the last unmoved operation that followed such a link,
has moved, so no link is carried across a reset any more, and `hooks.stash`
keeps nothing across one but the setup sessions (`SURVIVES_A_RESET`).

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
`admin`, `ordinary`, and `app-password` from the fresh E2E fixture, except that
`admin` is the two-factor administrator signed in with an unused second factor:
credential, link and grant operations need a recent one, which the seeded admin
never has. The owned
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

The run does not rely on what the gate left. Its creates clear what the gate made
from the same examples (`a_leftover_to_remove`; `run.sh` sets
`CONTRACT_REMOVE_LEFTOVERS`, as for the ownership runs). The admin's
`updateDraft` cell updates a draft made for it, since `saveDraft`'s 201 is not
sent here. Each documented `requestRecoveryEmail` success asks about an address
of its own, since the server allows five requests an address every 15 minutes. The
documented `retryQueueEntry` refusal retries an entry bounced for it, and an upload
whose run's domain `deleteDomain` removed goes to a domain of its own. Coverage
probes of `updateMailbox` go to a mailbox made for them, so their generated
password never replaces the one `linkAccount`'s example proves on
`eve@restmail.test`.

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
The domain run loads `domain_ownership_hooks.py` after the shared hooks. For
successful alias, custom-filter and pipeline creates, it reads the caller's real session
address and the domain-tenant fixture (`.cache/contract/domain-tenants.json`, or
`CONTRACT_DOMAIN_TENANTS`). All three creates use that caller's own domain;
aliases and custom filters also get fresh names. Setup and attack credentials,
target paths and expectations are unchanged, as are refusal examples and other
phases.
The runner still reports setup that requires installation-wide privileges as
unsent: a scoped domain administrator cannot create domains or certificates.
Those setup limitations are not ownership passes.
The queue, grants and bans take ids that no link makes and that are not a
domain's, so they are not sent. The admin mailbox routes have a third run,
`ownership-mailboxes` (`ownership.py --mailboxes`): the same administrators,
each declaring its own mailbox, which the fixture prints beside its domain, are
sent on `/api/v1/admin/mailboxes/{id}` and the mailbox list. Its report is `ownership-domains`.
Tenant ids are keyed by parameter name, and the account run already declares
`path.id`, so the domains need a run of their own.

A fourth run, `ownership-messages` (`ownership.py --messages`), sends the two
account tenants each other's messages, drafts, scheduled sends, invitations
and attachments (#1163), with the creates their links start from. Their ids are
not an account's: for each cell the victim tenant's signed-in owner sends the
chain the links lead from (`sendMessage`, `saveDraft`, `updateDraft`,
`getRawMessage`), and the attacker is sent the id it made. That covers
`getMessage`, `updateMessage`, `deleteMessage`, `getRawMessage`,
`listAttachments`, `updateDraft`, `sendDraft`, `cancelScheduledMessage` (from
the scheduled send's 202) and `respondToCalendar` (from a send carrying an
invitation). The run provides `future_time` and `calendar_invitation`, which
`ownership_hooks.py` makes true: the victim's scheduled send is moved to
tomorrow, and its other sends invite the sender to an hour tomorrow. No link
leads to `getAttachment`, so before the run each tenant's owner imports a
message with an attachment into its own account through the API and declares
the attachment's id as `path.id`, which no row with a chain reads; if that
fails, the tenant declares nothing and those cells are unsent.
`forwardMessage` is selected too, but has no row: its success example names
no message, so it inherits a fault-only path parameter. Every answer must be 404: someone else's
account, or an object in it, is answered as if it did not exist
(`docs/api-conventions.md`, rows 11 and 12). The examples send from the fixture
administrator, which an account may not, so `ownership_hooks.py`, loaded after
`hooks.py`, sends the chain and the attacker's request from, and only to, the
sender's own address; `run.sh` sets `CONTRACT_OWNERSHIP` for these runs, and
the draft send then keeps the draft its link filled. The run also sends the
documented examples, as the administrator, and provides `future_time` and
`calendar_invitation` for them too, so the administrator's scheduled send is
moved to tomorrow and its other sends carry an invitation as well. Only the
success reply is filled by a link: `foreign_sender` (403) names message 1, so
it is sent on the invitation the same credential's send made, or the handler
answers 400 for the message before it reads the sender (#1942).

### Times that must be in the future

`scheduled_at`, `snoozed_until` and a new credential's or grant's `expires_at`
are taken only in the future, so a date written into an example expires: the
scheduled send's example said 2026-10-27 and was refused 422 from that day on
(#1942). `future_times.py` lists every such date in `openapi.yaml`,
`openapi-example.json`, `internal/examplepairs/example-matrix.json` and this
directory's sources that is past or less than 30 days ahead, unless its example
is the refusal of that field or the run moves it (`RUN_RELATIVE`: a send must be
scheduled within a year, so `hooks.py` sends the scheduled example an hour
ahead). `test_future_times.py` holds the repository to an empty list, checks
that each moved example is sent a minute to a year ahead, and fails on a field
the handlers refuse as "not in the future" that the list does not name.

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
chore testbed:init       # clone rest-mail/testbed at its pin, seed config/restmail.test
chore mailserver:init    # clone rest-mail/reference-mailserver at its pin into .workspace/
chore substrate:check    # the version receipt; fails when a clone is not at its pin
```

Both clone over `git@github.com:`, so they need GitHub SSH access. The first
contract run also needs the internet: it downloads the pinned Vertrag release,
runs `pip install pyyaml` when PyYAML is missing, and pulls the topology's
container images. After that, the check talks only to the local
`restmail.test` topology.

The substrate commits come from one file, `scripts/substrate-pins.yml` (#1751).
Both init tasks check out its commits through `scripts/substrate_pins.py sync`,
and CI's E2E job runs the same two tasks, so a clean checkout gets CI's inputs
without reconciling clones by hand. Re-running an init task moves an existing
clean clone to the pin; a clone with uncommitted changes is left alone and the
task fails, naming the files. To adopt upstream substrate changes, bump the
commit in that file.

The one platform-specific step is scripted there too: on native-Linux Docker
(CI's runner, and a Linux machine) `mailserver:init` rewrites the reference
server's `dkim:provision` so its `chmod` of the container-root-owned key runs in
a container. Docker Desktop's host-owned bind mounts need no change, so macOS
skips it.

Before a run, `chore contract` prints the version receipt: the server commit
(marked when it has uncommitted changes) and each substrate's checked-out commit
against its pin. When it is about to bring the topology up and a clone is
missing, at another commit, unprepared or changed, it fails before the example
pairs. With `CONTRACT_ENDPOINT` it only reports, because the running topology was
not necessarily built from these clones. CI prints the same receipt after its
init step and fails there. `RESTMAIL_SUBSTRATE=unpinned` keeps an existing
clone where it is, to run against a substrate branch, and turns the failures
into warnings; that run's inputs are not CI's.

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

`chore contract` then prints the substrate receipt (above) and runs these steps
in order:

1. `chore contract:pairs`, the registered example-pair checks
   (`tests/contract/example_pairs.sh`). It builds the `internal/api` test binary
   once and runs the matching tests in four parallel shards, each limited to 25
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
scan fail. The authorization and ownership reports never fail it. CI's gating job
calls `chore contract` with `CONTRACT_REPORTS=false`; a separate job runs
`chore contract:reports` beside it on an independent topology. The report job
replays the E2E stages: documented account IDs, the first attachment and the
delivery pipeline depend on their state, not just on the principal fixture.
The command makes fresh principal, queue, MFA and domain-tenant fixtures and fetches the description
when no saved copy exists. Neither its result nor its duration decides the required
E2E result. The live proofs also run beside the gate, with their own principal and
queue fixtures. Their process exit status remains report-only, but their proof
artifact still participates in the required proof union. A run here whose receipt shows the same server commit
and substrate pins has CI's inputs. A recorded local and CI run with equal
verdicts at the same pins is still open in #1751.

The `contract:pairs` task in `chores.yml` selects `WithVertrag$`. The wrapper
discovers the matching tests from the compiled `internal/api` test binary before
sharding them, so new `*WithVertrag` tests run without editing a shared name list.
The four shards use longest-first assignment to the currently lightest shard,
with measured times in `example_pair_seconds.json`. That file records the source
run; it is a scheduling hint, never a test allowlist. New tests receive the median
known duration, ties break by name and shard number, and every discovered test
still runs exactly once. Refresh the recorded seconds from top-level test verdicts
when measurements show drift.
`test_run.py` checks that the pattern selects every such test, and the wrapper
fails if no tests match. Without `RESTMAIL_VERTRAG_BINARY` those tests skip, so
`go test` alone does not run them.

A narrowed run (`--tag`, `--operation-id`, `--only-matching`)
reports without the surface gate and the baseline, which are about the whole API.
Its coverage verdict comes directly from Vertrag: exit 0 for passing selected
probes, exit 2 for findings or skipped valid probes. A narrowed finding is not
excused by the whole-API baseline. The complete command preserves that failure while still running the
secret scan and producing the authorization report.

Coverage builds all of an operation's valid probes on one example: the first by
name among its lowest 2xx status. A new example that sorts first therefore changes
every one of those probes, and if it cannot be sent live they are skipped.
`python3 tests/contract/coverage_bases.py` compares each operation's example with
`origin/main` and needs no server. It exits 1 and names any that changed.
`chore contract:bases` runs the narrowed gate on those operations. The comparison
does not say a probe will be skipped, and a narrowed run holds only what it selects:
`updateMailbox`'s new example on #1724 left an account inactive and lost
`restmailDeliver`'s probes, which only a whole-API run shows.

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

The items of `to` and `cc`, in `saveDraft` and `updateDraft`, are described as
`idn-email`, in `internal/api/described_items.go`. The handlers also accept the
wider forms `net/mail` reads as one address, such as a display name around one,
which the operations' descriptions say. `from` has no format: an empty one is
accepted, and an address that is no mailbox the caller may write to is a 403, not
a bad body (`draft_fields_test.go`). Vertrag 0.42.0 generates no probe from a list
item's format, so these formats add none: the draft refusals are proved by named
examples. Those of `updateDraft` that need the draft to exist say
`existing_draft` and are proved in process, because the examples phase has no
draft at the example's id.

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

### Live requirement proofs (#1237)

`live_requirements.py` proves named anonymous and over-cap examples, and
`live_principals.py` a bounded matrix of real credentials, against a running
API. Use a disposable seeded `restmail.test` database, the repository's API
build, and the pinned binary from `scripts/vertrag_pin.py ensure`, not a Vertrag
found on PATH. Prepare the MFA admin and contract principals first;
`CONTRACT_MFA_ADMIN` selects their disposable enrollment file.

```sh
python3 -B tests/contract/live_requirements.py anonymous "$CONTRACT_ENDPOINT" "$work/anonymous" \
  --vertrag "$CONTRACT_VERTRAG"
python3 -B tests/contract/live_requirements.py over-cap "$CONTRACT_ENDPOINT" "$work/over-cap" \
  --vertrag "$CONTRACT_VERTRAG"
go run ./tests/contract/fixtures --test-domain=restmail.test --api-keys > "$work/fixture.log"
sed -n 's/^API_KEY_FIXTURE=//p' "$work/fixture.log" > "$work/api-key.json"
python3 -B tests/contract/live_principals.py "$CONTRACT_ENDPOINT" "$work/principals" \
  --vertrag "$CONTRACT_VERTRAG" --principals "$CONTRACT_PRINCIPALS" --fixture "$work/api-key.json"
```

- A pair counts only with exactly one passing exchange, the expected status and
  a clean runner exit. A missing route, skipped exchange or tool failure is
  unproven. `--operation-id` narrows the run for local use and is no gate result.
- The over-cap hook sends 33,554,433 real bytes, above the default API and
  delivery caps. A deployment with larger limits must pass a larger `--body-bytes`.
- The `--api-keys` fixture must be fresh, because the runner enrols it in MFA.
- Push pairs need the real API started with a disposable P-256 APNs key and all
  three APNs settings. These refusals send no notification.
- The owner refusals need one registered owner key (`restmail-api owner-key add`).
  Without one, the API correctly answers 409 before checking the challenge.
- `--workers` runs over-cap cases side by side. Each message carrying a 33 MB body
  costs about 5 seconds of CPU in Vertrag's hook worker. The hook file empties the
  body once it has been sent, so a case pays for one such message, not three.

Each runner writes a per-exchange Vertrag verdict, a compatible
`{operation: [example, ...]}` proof list, and an explicit unproven list.

The E2E suite runs the three runners after the gate, before the limits suite
(`chore test:contract-live-proofs`, `tests/contract/live_proofs.sh`), and uploads
their results. That stage is a report: a failing runner does not turn the run red.
Then the `E2E (every example is proven)` job in `e2e.yml` joins them with CI's
in-process proofs for the same commit and runs `scripts/example_proofs.py --live`.
It fails on any example no runner proved, and the required `E2E (full topology)`
check needs it.

A live pair counts only with a finished run, a proof list that agrees with the
run's inventory, the requirement the run provided, and that pair's own passing
Vertrag verdict. A missing, empty, malformed, failed, skipped or tool-error result
counts for nothing and is named in the output. One failed pair does not take the
runner's other proofs with it. Only the merge queue proves this in the topology;
local evidence must not be described as a CI result.

CI's `Test report` has no live results to read, so `--defer-live` leaves it the
pairs a live runner selects, and they are judged in the merge queue instead. Every
other pair still fails there.

### In-process example proofs

Give `RESTMAIL_EXAMPLE_PROOFS` a fresh path for each run. The Go writer merges an
existing file, so reusing one can hide a skipped or failing proof. Then check it
with `python3 scripts/example_proofs.py openapi-example.json <proofs.json>`.

Run database-backed Go packages through `tools/testdb-exec`, as CI does. Against
the seeded database a live run uses, tests that expect an empty one fail, such as
`TestAuthLogin_DocumentedCredentialExamples`, and that says nothing about the code.

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
fail the gate. These fixtures test the proof checker. They are no claim that the
live API returns malformed bodies or refuses safe CSRF variants.
Live topology and broader mutation workflows still run in GitHub.

Reset and recovery are public sign-in routes, so they rely on `SameSite=Strict`
rather than the CSRF guard. The command sends a refused reset (an unknown token)
and a refused recovery (a throwaway `restmail.test` address) from a browser
carrying a session but no CSRF header. Each must answer 401 with its fixed code,
not 403 `csrf_failed`, and must set or clear no session cookie. A wrong token
spends nothing, and the refused recovery is charged only to the throwaway
address. A successful reset or recovery runs only with `--session-starts`, which
the contract run does not pass yet (#1565).

### Contract changes in pull requests (#653)

The `API contract changes` CI job generates the description from the exact PR
head and from where that head branched from its target base: their merge base,
not the base's newest commit. It uses the same
`TestTheGeneratedDescriptionIsServed` export as the UI type builds, without a
database. Neither document is committed. For a stack, the comparison follows
each PR's base, so each layer shows its own changes.

The result is what the PR introduces. An operation or field its base gained
after it branched is not reported as a removal, and a PR that removes one after
rebasing is. It is not a check that the PR's head is compatible with the newest
base. The job runs for pull requests only, so a merge group skips it. A branch
with no single merge base fails the job rather than comparing from the tip.

The verdict runs `scripts/contract_diff.py` and installs the comparator with
`scripts/oasdiff_pin.py` as they are at the base's newest commit (#1760), so a PR
cannot change the rules that judge it. For a stack that is the parent branch's
copy, and the PR is judged again when it is retargeted. `test_contract_diff.py`
proves the PR's own copies, with the comparator version the PR pins, and the
check summary says when a PR changes either script. New rules therefore apply
from the first run after they merge. The workflow comes from the merged ref, so
its arguments and the base's copy match on any branch, however stale. A PR that
changes both fails with exit code 2, because the base's copy does not know the
new argument. Land such a change in two PRs: add the new argument beside the old
one, then change the workflow once that is merged. A test runs the workflow's
arguments against the checked-out script, so dropping one the workflow still
passes fails the PR and the merge queue.

This covers an edit to those two scripts. A PR can still change the verdict by
editing the workflow itself, or the code that generates the head description.
Only review catches those (#1776).

The checksum-pinned oasdiff 1.34.0 compares OpenAPI 3.1 operations, parameters,
schemas, statuses, headers and security requirements, including capability roles.
The check summary names the exact revisions and classifies the changes; the
`api-contract-changes` artifact contains both generated documents, the JSON and
Markdown comparison, and `comparison.json` with the revisions compared.

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
