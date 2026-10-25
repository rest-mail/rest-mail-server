# Contract tests

[vertrag](https://github.com/antimatter-studios/vertrag) sends the selected
contract requests to a running API and checks each answer against the spec's
status, headers and body schema (#376). Every run saves the served
`/api/docs/openapi.json`; the primary phases still use `openapi.yaml` until
#670 completes the generated-description switch, as explained below.

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

### Own-account authenticator contract

The normal examples run includes `getTwoFactorStatus` and `enrollTwoFactor`,
with their 200 responses and missing-session 401s counted. Enrollment has no
request body, leaves a pending secret and does not change the fixture admin's
sign-in requirements until it is confirmed. The shared authentication pacing
hook covers enrollment; it leaves the transaction's principal untouched.
Confirm and disable stay outside this shared fixture's sequence because they
would change its sign-in requirements or revoke its session.

Both new operations document internal 500 errors. Those two fault responses
alone have an operation-scoped exclusion mirrored under `surface.exclude`:
the healthy topology cannot provoke a database/cryptography outage. Existing
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

The three API-key operations run in a required check,
`chore test:contract-api-keys`, inside `chore contract` before the examples gate. It creates a fresh
disposable `restmail.test` account with a domain-scoped admin grant, enrolls
2FA through the public API, confirms a real TOTP and signs in with a recovery
code. The suite checks create/list/revoke, metadata without secrets, password
reauthentication, validation, refusals without MFA, scope restrictions and
immediate revocation. These three operations alone are excluded from the
primary vertrag run because its fixture admin has no MFA. The dedicated step
has no `continue-on-error`, changes no primary principal, and adds no coverage
baseline entries.

Two gates judge the primary run:

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

`createDomain`'s 400 is paired the same way: its `body_array` request (`[]`,
JSON but not an object) goes with the `bad_request` the handler answers before
it reads any field (`contract_domain_body_examples_test.go`, which sends it to
the real handler with no database).

`CreateDomainRequest.sending_policy` uses an inline enum: vertrag 0.29 drops
all body probes when the request schema contains a nested `$ref`. The schema
test holds the inline values to `DomainSendingPolicy` and the real handler's
validation. `createDomain` stays in the coverage baseline: hostname constraints
and fixture state for repeated creates still need work.

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

`importContacts` pairs `body_array` with its 400 and `contacts_missing`,
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
credentials still reach the handler unchanged.

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
(`contract_testcustomfilter_examples_test.go`).

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
malformed JSON and non-object bodies return 400.

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

`setVacation` pairs `configured` with 200, `body_array` with 400, and
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
`port_wrong_type` with 400; and missing/invalid transport, invalid host,
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

`setDKIMKey` pairs `installed` with 200 and `body_array` with 400. Missing,
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

`setDKIMRotation` pairs `configured` with 200 and `body_array` with 400.
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

`saveDraft` includes an `empty` request (`{}`) as well as `with_sender`.
Every draft field is optional: omitting `from` saves in the signed-in
account's mailbox with an empty sender. An explicit sender still needs
mail:write on that mailbox (`contract_draft_test.go`). The operation remains
in the coverage baseline because generated sender addresses can name
mailboxes the principal cannot write to.

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
  folder for each rename probe (a successful rename consumes the previous one),
  and, for `addToAllowlist`, `addToBlocklist` and `createAlias`, the entry a
  previous probe added removed when generated bodies repeat it (the domain
  stays for the whole run, so a repeat is otherwise a 409), with an alias
  probe's domain hosted first. The entries `removeFromAllowlist`,
  `removeFromBlocklist`, `getAlias`, `updateAlias` and `deleteAlias` are linked
  to are followed to their current id, and added again if a probe left none.
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
  gate, holding only the public `fixtures/dkim-key.pem` signing key. Its tests
  keep pins out of the examples gate and preserve all success probes.
- `authorization.py`: builds a private configuration from the E2E principals
  for the report-only authorization phase. Its JUnit report and log are
  included in `e2e-container-logs`; the generated credential-bearing YAML is
  removed after the run and is never uploaded.
- `secret_scan.py`: the secret scan (#673). The examples phase records its
  traffic as `.cache/contract/examples.har` (vertrag's `har` reporter); the scan
  collects every password, private key and recovery code the run sent and every
  app password, enrollment secret, recovery code, session token and API key the
  server issued, and fails when any of them appears in another answer or in the
  audit log, read through the API at the end. vertrag writes its own credentials
  as `<redacted>`, so that string in any answer but sign-in and refresh is a
  finding too. Its tests are `test_secret_scan.py`. The HAR holds fixture
  passwords and is not uploaded with the logs.
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

## Closed schemas

`TestGeneratedSchemasAreClosedUnlessRecorded` inspects component and inline
schemas in the generated description, including nested values. Each dictionary,
opaque JSON value, or explicitly open protocol object is listed by its exact
JSON pointer in `open-schema-exceptions.yml`, with a reason and an area issue
where a more precise shape is still needed. A new open schema fails the test;
closing one requires removing its exception in the same change. Example data
is not treated as schema.

## Running it

One command does what CI does (#627), on any machine with Docker, `chore` and the
pinned Vertrag (`chore vertrag:install`):

```sh
chore contract                      # bring up the E2E topology, then every phase
CONTRACT_ENDPOINT=http://10.99.0.20:8080 chore contract   # a topology already up
chore contract:run -- --tag Domains # the gates for one area, against a running topology
chore contract:run -- --operation-id getDomain
chore contract:fuzz -- --seed 42    # the fuzz phase; replay a finding by its seed
chore contract:diff                 # the generated description against openapi.yaml, no server
```

`chore contract` prepares the fixtures, proves the API key lifecycle, fetches
`/api/docs/openapi.json` into `.cache/contract/openapi.json`, then runs
`tests/contract/run.sh gate` (the examples phase, then the coverage phase held to
`coverage-baseline.txt`) and `tests/contract/run.sh authorization` (a report).
CI's E2E job calls `chore contract`, so a run here gives the verdict CI gives for
the same commit. A narrowed run (`--tag`, `--operation-id`, `--only-matching`)
reports without the surface gate and the baseline, which are about the whole API.

The secret scan also runs locally, including after a failing examples gate.
The API key proof and every Vertrag phase honor `CONTRACT_ENDPOINT`. A fetch
failure stops the run. The phases themselves still read `openapi.yaml`: the
saved generated description is published with the reports, while #670 tracks
the switch and its live contract proof. Required parameter examples were added
in #851 and are held by `TestEveryRequiredParameterHasAnExample`; they are no
longer the blocker. `openapi.yaml` remains the independent oracle for
`chore contract:diff`.

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
counts and each operation/status exclusion with its reason; it also runs when
a gate fails. The complete command prints it once, after the authorization
report. Standalone gate and fuzz commands summarize only their own phases,
so an earlier fuzz report cannot appear in a later gate verdict:

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
probes when an email is reused. It first checks that the email was absent,
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
then leaves them in place. Malformed senders and domains the server will not
host are left to the probe, and the request is never rewritten.

Update probes (updateDomain) that set `active` to true would meet 409
`certificate_required`: no created domain has a certificate when they run. A
before hook gives the domain in the path a self-signed certificate through the
admin API the first time such a request appears, then leaves it in place. The
request is never rewritten, and a domain that does not exist is left to the
probe's 404.

Test send probes (sendTestEmail) send from and to a generated address
(`someone@example.com`) that has no mailbox. A before hook hosts its domain and
creates its account through the admin API the first time an address appears,
then leaves both in place: a test send only adds a message. Malformed addresses
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

### Browser session gate (#659)

Before examples, `chore contract` runs `tests/contract/browser_sessions` against
that run's saved served JSON and existing `restmail.test` admin fixture. It signs
in as a browser, validates all three cookie attributes and paths, checks every
cookie-protected unsafe operation refuses a missing CSRF header, and compares
safe reads with and without the header. Invalid resource IDs and malformed JSON
keep negative probes away from handler mutations. A valid-header folder request
must reach validation, and an own-account event stream must open using cookies.
Refresh must return browser cookies without body tokens; logout must expire all
three cookies and refuse another refresh with the revoked cookie. The probe revokes its own
session on failure as well.

Failures stop the gate. `browser-sessions.json` records operation, principal and
status only, with `passed` true only after the whole proof. Local HTTP fixtures
verify that removing CSRF enforcement or a cookie's Secure attribute fails this
proof. Live topology and broader mutation workflows still run in GitHub.

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
