# Contract tests

[vertrag](https://github.com/antimatter-studios/vertrag) sends every request
`openapi.yaml` documents to a running API and checks each answer against what
the spec promises: the status, the headers and the body's schema (#376).

## Where it runs

In the `E2E (full topology)` job (`.github/workflows/e2e-suite.yml`), after the
14 stages pass, against the topology they ran on, as the fixture admin
(`admin@restmail.test`, from `cmd/seed`):

| Phase | What it checks | In CI |
| --- | --- | --- |
| `examples` | Each documented request gets its documented answer. Ordered by the spec's links (`--sequence`), so a read, an update or a delete addresses what its create made. | Gate |
| `coverage` | Every boundary each schema implies (the maximum, one past it, a required property missing) gets a 4xx, never a 2xx or a 5xx. | Gate, as a ratchet (below) |
| `ignored-auth` check | Every authenticated request, sent again with no credential, is refused. | Gate |
| `authorization` | Every selected operation as each configured principal, checked against the spec's security roles. | Report only, after the gates |
| `fuzz` | Values drawn from each schema, shrunk to the smallest failing input, with a replayable seed. | Nightly (`contract-fuzz.yml`), never blocks |

Two gates judge the run:

- **examples, in vertrag:** `vertrag.yml`'s surface gate (`surface: {count:
  passed, require: 100}`) counts an operation only when every documented
  transaction of it was sent and passed, none failed, none skipped (because a
  hook skipped it or the link it follows did not resolve), none answered 5xx,
  and requires every operation it does not exclude. A shortfall exits 3, a
  failed transaction 1, and the contract step fails on either. Each exclusion
  is printed with its reason: the operations `exclude-operation-id` never
  sends, and the documented statuses `exclude-matching` keeps back, which
  `surface.exclude` mirrors.
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
400, the `bad_request` the handler answers before it reads any field; its
`source_invalid` request (the `created` one with a source that is not an
address) goes with its 422 naming `source_address`
(`contract_alias_examples_test.go`). `createPipeline` is the third: its
`direction_missing` request (the `created` one without its direction) goes with
its 422, and `created` with its 201; the hooks give the refused request the
created domain but no direction. Its `body_array` request (`[]`) goes with its
400, the `bad_request` the handler sends for a body it cannot decode, and is
sent as it is: the hooks have no object to put the domain in.
`internal/api/handlers/contract_examples_test.go` (and
`contract_pipeline_examples_test.go`, for `createPipeline`) sends each request
named on any of these 422s to the real handler, which has no database, and
checks it gets the documented body: the refusal comes before anything is looked
up or stored. `contract_pipeline_body400_test.go` does the same for
`body_array` and its 400.

`createDomain`'s 400 is paired the same way: its `body_array` request (`[]`,
JSON but not an object) goes with the `bad_request` the handler answers before
it reads any field (`contract_domain_body_examples_test.go`, which sends it to
the real handler with no database).

`createBan` (`ip_missing`, `duration_invalid`), `createCustomFilter`
(`filter_type_invalid`), `validateCustomFilter` (`body_array` with its 400,
`script_missing` with its 422) and `sendTestEmail` (`from_missing`) are paired
the same way, each checked against the real handler with no database
(`contract_ban_examples_test.go`, `contract_customfilter_examples_test.go`,
`contract_validatejs_examples_test.go`, `contract_validatejs_body400_test.go`,
`contract_testsend_examples_test.go`).

`createAccount` (`address_missing`), `restmailDeliver` (`recipients_missing`),
`addToAllowlist` and `addToBlocklist` (`pattern_missing`) now send their paired
422s too. `bulkDeleteQueue` sends its `selection_missing` request (`{}`) with
its paired 400. Their existing handler tests check these refusals before any
database access (`contract_account_examples_test.go`,
`contract_restmail_examples_test.go`, `contract_allowlist_examples_test.go`,
`contract_blocklist_examples_test.go`, `contract_bulkdelete_selection400_test.go`).
No before hook rewrites these requests. Sibling operations remain excluded.

`bulkRetryQueue` and `bulkBounceQueue` send their `invalid_body` (`[]`) and
`selection_missing` (`{}`) requests with their paired 400s; their original
request is named `by_ids`, with a matching 200 example, so neither refusal is
paired with success. The 400s no longer carry a `too_many_ids` example: a
request of 1001 ids is not worth a place in the spec, and the handler tests
still check that refusal (`contract_queue_bulk_pairs_test.go`).

`receiveTlsReport` sends its `organization_missing` request with its paired
422 and its `body_array` request (`[]`) with its paired 400, the `bad_request`
the handler sends for a body that does not decode as a report. Both are refused
before any domain is looked up or report stored
(`contract_tlsrpt_examples_test.go`). The other `/.well-known` paths keep
their exclusions.

`microsoftAutodiscover` pairs its `malformed` XML request with its plain-text
400 (`invalid request\n`), and `valid` with its XML 200. Vertrag v0.29.0
pairs XML examples by name but does not serialize their request bodies, so
the hook supplies the matching XML for each status. The hook test checks both
bodies against the spec; `contract_autodiscover_body400_test.go` sends the
spec's malformed request to the real handler with no database. The 200's
named example declares no fixed response body: the XML contains the requested
mailbox's settings. This route's 400 is now sent and judged.

`testFilter` sends its `invalid_body` request (`[]`) with its paired 400 and
`filter_name_missing` with its paired 422. Both are refused before any filter
is made; `valid` still runs `header_validate` and gets the documented 200
(`contract_testfilter_examples_test.go`). No hook rewrites these requests.

`testPipeline` sends its `invalid_body` request (`[]`) with its paired 400 and
`email_missing` (a pipeline id and no email) with its paired 422. Both are
refused before any pipeline is looked up; the hooks give only `valid` the
created pipeline's id, and it gets the documented 200
(`contract_testpipeline_examples_test.go`). Other pipeline operations keep
their exclusions.

`testCustomFilter` sends its `invalid_body` request (`[]`) with its paired
400, the `bad_request` the handler sends for a body that does not decode,
before the filter is looked up. Its `valid` request (`{}`, the built-in sample
email) runs the created filter and gets the 200
(`contract_testcustomfilter_examples_test.go`). The other
`/custom-filters/{id}` operations keep their exclusions.

`createCertificate` sends `invalid_body` (`[]`) and `fields_missing` (`{}`)
with their paired 400s, both refused before the domain lookup. Its `valid`
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
The previously documented 422 was removed: this handler never returns it;
invalid PEM and missing fields return 400.

`sendMessage` sends `from_missing` (`valid` without its from) with its paired
400, refused before the sender is looked up, and `unsubscribe_not_allowed`
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

These operations' 400s and 422s are sent and judged: `vertrag.yml` excludes
400 and 422 by path, naming every path but the paired operations' own,
rather than everywhere. A transaction sent and refused other than as
documented fails the examples phase like any other. `test_gate.py` checks
that the documented 400s and 422s sent are exactly the paired operations'
(its `PAIRED`), so an operation paired next comes off those patterns in the
same change. It also checks that each sent refusal has named response examples
with matching request examples. The statuses stay in `surface.exclude`: the
surface gate cannot take an exclusion off one operation only.

The coverage phase probes every transaction sent, and judges valid-mode probes
whenever the transaction's own request gets the status it documents. For a
refusal that request is the refused one, so every valid probe built from it
(`{}` with a `server_type`, a destination address with no source, or a
pipeline with no direction, say) is refused as well, and vertrag reports each
as a failed probe. `gate.py` does not judge a probe built from a refusal (a
transaction whose status is 4xx), so those failures do not count against the
coverage ratchet (`test_gate.py`).

vertrag is the released, checksum-pinned binary (v0.29.0), not a Go
dependency: it needs Go 1.26 and this module is on 1.25.

## Files

- `../../vertrag.yml`: the run: endpoint, sign-in, exclusions, and the
  surface gate with each exclusion's reason.
- `hooks.py`: only what the spec cannot hold: the fixture's passwords, the
  refresh token a sign-in hands out, a fresh key pair, the created domain's id
  where a request carries it in its body, and its name in the Host header the
  MTA-STS policy file is served by, cleanup of folders previously created by
  create probes (so repeated valid names do not conflict), and a fresh source
  folder for each rename probe (a successful rename consumes the previous one).
  Folder create probes keep their generated bodies and account IDs unchanged.
  Folder and contact setup signs in as the fixture admin with a separate bearer
  session, cached per endpoint. It leaves the request's own credentials intact,
  including when the principal has no credentials; Basic auth is reserved for
  app passwords, not the fixture admin's account password.
  Every other id comes from the spec's `links`.
- `gate.py`: the contract coverage gate. `gate.py --failing-probes openapi.yaml <report>` prints
  the operations with a failing probe, in the baseline's form.
- `authorization.py`: builds a private configuration from the E2E principals
  for the report-only authorization phase. Its JUnit report and log are
  included in `e2e-container-logs`; the generated credential-bearing YAML is
  removed after the run and is never uploaded.
- `coverage-baseline.txt`: the coverage ratchet's baseline.
- `test_gate.py`: `gate.py`'s own tests (`python3 -m unittest discover -s tests/contract -p test_gate.py`).
- `fixtures/`: prepares a deferred queue entry on the disposable `restmail.test`
  topology before the contract run. Its next attempt is held for 24 hours so
  the background worker cannot deliver it before the retry example. This does
  not change the product's retry rules or any coverage exclusion.
- `../e2e/contract_principals_test.go`: the principals, written to
  `.cache/contract-principals.json`.

## Principals

The E2E suite makes one of each kind of caller and checks a small
authorization matrix with them (`TestContractPrincipals`):

| Principal | How |
| --- | --- |
| admin | the seeded `admin@restmail.test`, superadmin at `*` |
| ordinary account | `contract-ordinary@restmail.test`: a password, no grants |
| owned account, no password | `contract-owned@restmail.test`: reached only through its owner, the admin |
| app password | one for the ordinary account; what it holds is fixed: read, write and send on its own mailbox |
| agent | none: the account model has no agents yet (#341) |
| no credentials | every request without one, and vertrag's `ignored-auth` check |

The examples and coverage run signs in as the admin. The authorization run
uses vertrag v0.29.0's authorization phase: each operation's `security`
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
and out-of-scope 404 versus 403 still need a policy decision. The authorization
phase uses literal example IDs instead of lifecycle links (vertrag#81), so
deleted or absent resources, reused refresh tokens and hooks that replace a
credential also cause failures or unsent cells. This report does not establish
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

## Running it

Against a topology that is up (`chore e2e:up`, then `chore test:e2e` for the
principals):

```sh
chore test:contract-fixtures
vertrag doctor                          # sends nothing: does every setting reach something?
mkdir -p .cache/contract                # the report's directory; vertrag makes none
vertrag run --sequence --phases coverage
echo "vertrag exited $?"                 # 0, or 2 for probe findings; 1 and 3 fail
python3 tests/contract/gate.py openapi.yaml vertrag.yml .cache/contract/examples.xml \
  tests/contract/coverage-baseline.txt
```

`vertrag run --dry-run --sequence` shows what would be sent, with nothing up.

Contact create probes use a before hook to remove contacts made by earlier
probes when an email is reused. It first checks that the email was absent,
leaves pre-existing contacts alone, and preserves generated bodies, account IDs
and headers. Coverage does not call after hooks, so ownership is recorded before
the first create attempt.

`getCustomFilter` sends its documented `missing` 404 using the missing-ID path
parameter example. Vertrag 0.29 does not pair parameter example names, and the
create link supplies the created ID to both responses, so a hook selects
2147483647 only for the 404 transaction. The successful lookup and its boundary
probes retain their original IDs. The handler test proves the missing row is
absent, checks the exact refusal and successful lookup, and verifies no stored
filter changes. Other missing-resource responses remain excluded (#386).
