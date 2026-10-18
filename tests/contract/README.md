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
| `fuzz` | Values drawn from each schema, shrunk to the smallest failing input, with a replayable seed. | Nightly (`contract-fuzz.yml`), never blocks |

Then `gate.py` reads vertrag's JUnit report and fails the job unless:

- **examples:** every operation not excluded in `vertrag.yml` was sent and
  passed: every one of its transactions, with none skipped because the step it
  follows failed. Each exclusion is printed with its reason. (The examples half
  becomes `vertrag run --require-surface 100`: antimatter-studios/vertrag#70.)
- **coverage, a ratchet:** `coverage-baseline.txt` lists the operations whose
  boundary probes still fail (#388). A failing probe on any other operation
  fails the job, and so does an operation in the baseline whose probes all
  pass now. A fix takes its operation out of the baseline in the same change,
  so the baseline only shrinks after its initial seeding in #376.

The documented error responses other than 401 are excluded by pattern in
`vertrag.yml`: 403 needs a principal without the capability
(antimatter-studios/vertrag#71), and 400, 404, 409 and 422 each need a request
of their own that provokes them (#386).

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
its 422, and `created` with its 201; the hooks give the refused request the created domain but no direction.
`internal/api/handlers/contract_examples_test.go` (and
`contract_pipeline_examples_test.go`, for `createPipeline`) sends each request
named on any of these 422s to the real handler, which has no database, and
checks it gets the documented body: the refusal comes before anything is looked
up or stored.

Their 422s are still excluded, though vertrag sends them and the handlers
answer as documented: the coverage phase probes every transaction sent, and
judges valid-mode probes whenever the transaction's own request gets the status
it documents. For a refusal that request is the refused one, so every valid
probe built from it (`{}` with a `server_type`, a destination address with no
source, or a pipeline with no direction, say) is refused as well, and each is
counted as a failure on an operation the coverage baseline does not list
(neither `createAlias` nor `createPipeline` is in it). The error responses come
off `exclude-matching` once vertrag does not judge valid probes built from a
documented refusal.

`createDomain`'s 400 is paired the same way: its `body_array` request (`[]`,
JSON but not an object) goes with the `bad_request` the handler answers before
it reads any field (`contract_domain_body_examples_test.go`, which sends it to
the real handler with no database). It stays excluded with the other 400s.

vertrag is the released, checksum-pinned binary (v0.27.0), not a Go
dependency: it needs Go 1.26 and this module is on 1.25.

## Files

- `../../vertrag.yml`: the run: endpoint, sign-in, exclusions with reasons.
- `hooks.py`: only what the spec cannot hold: the fixture's passwords, the
  refresh token a sign-in hands out, a fresh key pair, the created domain's id
  where a request carries it in its body, and its name in the Host header the
  MTA-STS policy file is served by, and a fresh source folder for each rename
  probe (a successful rename consumes the previous one). Every other id comes
  from the spec's `links`.
- `gate.py`: the gate. `gate.py --failing-probes openapi.yaml <report>` prints
  the operations with a failing probe, in the baseline's form.
- `coverage-baseline.txt`: the coverage ratchet's baseline.
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

The contract run itself signs in as the admin. The others are for the
authorization matrix, which vertrag will check from the spec once it has an
authorization phase (antimatter-studios/vertrag#71): each operation's
`security` requirements name the capability it needs as OpenAPI 3.1 roles,
for example `security: [{BearerAuth: [domains:read]}]`.

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
python3 tests/contract/gate.py openapi.yaml vertrag.yml .cache/contract/examples.xml \
  tests/contract/coverage-baseline.txt
```

`vertrag run --dry-run --sequence` shows what would be sent, with nothing up.
