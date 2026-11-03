# Live requirement proofs (#1237)

This is additional live evidence, not a claim that the issue is complete. The
primary examples gate, the provisional authorization matrix and every baseline
remain unchanged.

## What the new runners establish

`live_requirements.py` fetches the running server's generated description. For
each selected pair it retains that operation's response schema and declared
headers, selects exactly its named request/response examples, and invokes
Vertrag's examples phase. It does not use the primary fixture hooks, change an
answer, disable validation, or configure `named-values` or an exclusion. It
requires exactly one passing exchange with the expected actual status, no
additional check failures and a successful runner exit. Missing routes, failed
validation, skipped exchanges, empty selections and tool failures do not count.

The over-cap hook sends 33,554,433 actual ASCII bytes, with an equal
Content-Length. This exceeds the default 22,020,096-byte authenticated API cap
and the 32,505,856-byte internal delivery cap. It does not merely claim a large
length on a short body. A deployment with larger configured limits must use
`--body-bytes` above its limits. A real MFA session is renewed during a long run;
no session is forged or authentication guard disabled.

`live_principals.py` exercises a bounded matrix with real credentials: admin,
ordinary, anonymous, app password, scoped account grant and scoped API key. It
checks both allowed and refused resources for the last three, and confirms that
the other account and domain exist before claiming an out-of-scope refusal. The
API key is created through the public API with real MFA and revoked afterwards.
The scoped session must actually contain its fixture's domain grant. These nine
cells do not establish the whole-API authorization cross-product or all 212
`without_capability` examples.

Each runner writes a per-exchange Vertrag verdict, a compatible
`{operation: [example, ...]}` proof list, and an explicit unproven list. New runs
clear the old proof list before sending. Temporary configurations containing
credentials are private and removed on exit. The new outputs are not yet
collected by the existing CI proof-union job; manual or local evidence must not
be described as a new CI gate.

## Running them

Use a disposable seeded `restmail.test` database and the repository's API build.
Install the pinned binary with `scripts/vertrag_pin.py ensure`, not a different
Vertrag found on PATH. Prepare the existing MFA admin and contract principals
first; `CONTRACT_MFA_ADMIN` selects their disposable shared enrollment file.

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

The `--api-keys` fixture must be fresh: this runner enrolls it in MFA. The
examples runner's optional `--operation-id` makes a visibly narrowed local
proof, not a surface-gate result. It exits nonzero if a selected pair cannot be
proved. Running the default inventory against a build without push enabled
therefore reports its push pairs as unproven rather than silently excluding
them. To prove those pairs locally, start the real API with a disposable valid
P-256 APNs signing key and all three APNs settings. Refusal proofs do not send
notifications or establish an actual APNs delivery.

The owner challenge refusals also require at least one registered owner key.
Without one, the live API correctly answers 409 before checking the challenge.
Register a disposable public key using `restmail-api owner-key add`; an invalid
challenge then reaches the documented 401. This is reachable live, not a fault
injection or an exemption from proof.

## Local evidence, November 3, 2026

The API and seed tools were built from main `472c6444`. Tests used a dedicated
`postgres:16-alpine`, development configuration and the checksum-verified
Vertrag 0.42.0 darwin-arm64 release. The Python contract suite also runs in
`python:3.12-slim` with PyYAML and the verified linux-arm64 release; macOS bash
3.2 is not used to run `tests/contract/run.sh`.

- All nine principal cells pass against the live local build.
- The first anonymous run proves 173/177. Its other four are the two owner
  refusals (409 with no registered key) and two push routes (absent when APNs is
  off). Separate live reruns with the required configuration prove all four.
  Thus all 177 anonymous pairs are reachable and were validated locally.
- Live over-cap results are recorded separately from the in-process union;
  the PR records their final measured totals and any unsuccessful first runs.
- Historical CI run 41565210080, commit
  `adae7081e31526cc68a71885e86914252b4a69fd`, reports 1323/1323 setup-bearing
  examples proven in process. Its successful union does not establish these
  live runs. `internal/examplepairs/proofs.go` excludes failed/skipped tests,
  and `scripts/example_proofs.py` still fails on an absent proof.

## Named values are not yet removed globally

An exploratory full local examples run with the override removed returned
464 passed, 36 failed, 3 errors and 14 skipped (517 results, including link
checks). This local build has no full delivery/sidecar/discovery topology or
the topology's held-mail fixtures. Those results must not all be attributed to
named values, and they do not justify removing the override from the CI gate.

One dependency was isolated and repaired: after the old custom-filter ID 1 no
longer exists, `getCustomFilter/found`, `updateCustomFilter/body_boundary` and
`deleteCustomFilter/found` need the filter created for the current run, not
the in-process fixture's ID. The four-operation local sequence returned 8/11
before the change (three 404s), and 11/11 afterwards with the default named-value
behavior. The fixture now remembers the real created filter and binds only
these successful literal-ID examples (including `testCustomFilter`'s success).
Missing IDs, invalid IDs and generated probes are preserved. A comparison with
`illustrate` alone still left the selected delete at 404; fixture setup rather
than a global switch is the repair.

Other failures observed in that exploratory run include `updateContact`,
`getCertificate`, `updatePipeline`, `deletePipeline`, `updateDraft`, message
reads/mutations, attachment reads and conditional/range variants. They require
further isolated fixture/link proofs before their causes can be assigned.
`named-values: illustrate` therefore remains in `vertrag.yml`. Removing it and
proving the complete examples/surface gate is outstanding, as is integrating
the live runner lists into CI. Full authorization fixture/scope policy work
belongs to #1162 and is not changed here. In particular, this patch does not
decide the existing matrix's flat-role/ownership and 403-versus-404 questions.

The full topology gate, internal mTLS delivery context and dependency-fault
tier are not proven by these local runs. The issue permits existing in-process
fault proofs until the fault tier exists; no live fault injection is claimed.
