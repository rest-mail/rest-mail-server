# Requirement example audit

Part of #1237. Checked on 2026-11-02 against `origin/main`
`55f413759dcb6ca8fa31b633af5286d66c8ab011`, without Docker or a database
container. This is an audit of the issue's last comment, dated 2026-10-27,
not a new list of production defects.

## Result

- The original **68 response mismatches are now 0**. Every original pair still
  exists in `openapi-example.json`, and all 68 were recorded as proven locally.
- `TestRequirementExamplesThroughTheProductionRouter` passes: 796 leaf subtests,
  no failures or skips. It proves 51 of the old 68 directly; operation-specific
  fixtures prove the other 17. Non-JSON responses use exact string comparison;
  JSON responses use complete decoded-body equality, not a subset assertion.
- The full `internal/api` package passes: 5,408 tests/subtests pass and 182 skip.
  The skips comprise 113 database-dependent tests and 69 independent Vertrag
  checks (`RESTMAIL_VERTRAG_BINARY` is unset).
- The local proof gate still **fails, exit 1**: **1,220 of 1,258** examples with
  requirements are proven, leaving **38**. These are different from the old 68;
  database-backed fixtures must run in CI before claiming those proofs.

The body/status corrections and prerequisite fixtures from #1276 have already
merged. For example, database failure during protocol sign-in is correctly 503
`unavailable`; mailbox delivery's lookup failure is 500, while the deferred pair
reaches its later policy lookup and answers 451. Owner authentication pairs now
exercise invalid challenges rather than an unrelated owner-registry outage.
The body-size pairs exercise capped reads before authentication/database access.
No further body/status correction is justified by this run. No pair was deleted,
excluded, weakened, renamed, or given a new requirement by this audit.

## Where the original 68 are proven today

The following **51** are proven by
`TestRequirementExamplesThroughTheProductionRouter` (each name below is a
subtest under its operation):

| Operation | Pairs |
|---|---|
| `acceptInvitation` | `internal_error`, `invalid_token` |
| `accountEvents` | `internal_error` |
| `activateGrant` | `internal_error` |
| `confirmTwoFactor` | `internal_error` |
| `createAccount` | `internal_error` |
| `createGrant` | `internal_error` |
| `deactivateGrant` | `internal_error` |
| `deleteGrant` | `lookup_unavailable` |
| `deleteQuarantine` | `write_unavailable` |
| `deliverMessage` | `deferred`, `internal_error` |
| `enrollTwoFactor` | `internal_error` |
| `forwardMessage` | `unauthenticated` |
| `inviteAccount` | `internal_error` |
| `listContacts` | `count_unavailable` |
| `login` | `internal_error`, `request_too_large`, `wrong_password` |
| `logout` | `internal_error`, `request_too_large` |
| `makeGrantEligible` | `internal_error` |
| `microsoftAutodiscover` | `internal_error` |
| `mozillaAutoconfig` | `internal_error` |
| `mozillaAutoconfigWellKnown` | `internal_error` |
| `ownerChallenge` | `internal_error` |
| `ownerStatus` | `internal_error`, `request_too_large`, `unauthenticated` |
| `ownerToken` | `internal_error`, `request_too_large`, `unauthenticated` |
| `prometheusMetrics` | `internal_error` |
| `protocolSignIn` | `internal_error` |
| `receiveTlsReport` | `request_too_large` |
| `recoverAccount` | `internal_error`, `request_too_large`, `unauthenticated` |
| `refreshToken` | `internal_error`, `request_too_large` |
| `resetAdmin` | `internal_error`, `request_too_large`, `unauthenticated` |
| `restmailDiscover` | `internal_error` |
| `saveDraft` | `internal_error` |
| `searchMessages` | `internal_error` |
| `sendDraft` | `internal_error` |
| `sendMessage` | `internal_error` |
| `unlinkAccount` | `internal_error` |
| `updateDraft` | `internal_error` |
| `updateMailbox` | `internal_error` |

The other **17** are proven by their specialized fixtures, not silently omitted:

| Pairs | Proof |
|---|---|
| `apiRoot/internal_error`, `describeServer/internal_error`, `getHealth/internal_error`, `openapiDocument/internal_error`, `openapiSpec/internal_error`, `restmailCapabilities/internal_error`, `swaggerUI/internal_error` | `TestPublicReadFailureExamplesMatchErrorAdapter`: `fault=error_adapter` proves the shared sanitized error adapter, not an impossible SQL failure on a static route. |
| `forwardMessage/mime_too_large` | `TestForwardAttachmentsCanonicalExamples`: `limit=message_size` reaches the MIME-size refusal. |
| `getHealth/database_connection_unavailable` | `TestHealthExamplesMatchRegisteredResponses`: `fault=database_pool` distinguishes pool acquisition failure from ping failure. |
| `inviteAccount/unavailable` | `TestInvitationCanonicalExamples`: `state=enrollment_disabled` reaches the configuration refusal. |
| `microsoftAutodiscover/mail_host_unconfigured` | `TestAutodiscoverExamplesMatchRegisteredResponses`: valid XML reaches the unconfigured-host response. |
| `mozillaAutoconfig/mail_host_unconfigured`, `mozillaAutoconfigWellKnown/mail_host_unconfigured` | `TestMozillaExamplesMatchRegisteredResponses`: exact text/plain response and 503. |
| `probeServices/setup_failure` | `TestServiceProbeExamples`: `fault=database_pool` reaches setup failure, not a successful probe reporting an unhealthy service. |
| `restmailDeliver/internal_error` | `TestRestmailDeliveryRefusalExamples`: `fault=error_adapter`; the database delivery fault is a different, deferred response. |
| `testFilter/execution_cancelled` | `TestPipelineTestingExamples`: `fault=context_cancelled` reaches execution cancellation. |
| `validateCustomFilter/sidecar_invalid_json` | `TestCustomFilterValidationExamples`: `fault=sidecar_response` returns malformed JSON from a local HTTP sidecar fixture. |

## Current local proof gaps: 38

These are missing proofs, **not observed response mismatches**. A skipped
database-backed test supplies no evidence that its pair is either correct or
incorrect. None overlaps the original 68. They need the existing PostgreSQL
fixtures in CI; this audit neither changes their requirements nor substitutes a
different response. No new second requirement, fault value, product decision,
or separate production-bug issue was needed for the old 68.

| Operation | Pairs | Requirement |
|---|---|---|
| `confirmTwoFactor` | `no_pending` | `state=no_authenticator` |
| `createAppPassword` | `password_only` | `state=enrolled_authenticator` |
| `createClientSecret` | `created`, `missing`, `tier_refused` | `state=two_factor_session` |
| `createClientSecret` | `password_only` | `principal=without_capability` |
| `disableTwoFactor` | `not_enabled` | `state=no_authenticator` |
| `forwardMessage` | `held` | `state=spent_send_limit` |
| `getRecoveryEmail` | `verified` | `state=verified_recovery_email` |
| `getServerCapabilities` | `not_superadmin` | `principal=without_capability` |
| `issueClientCredentialsToken` | `issued`, `wrong_secret` | `state=client_secret` |
| `issueClientCredentialsToken` | `revoked` | `state=revoked_client_secret` |
| `listClientSecrets` | `listed` | `state=client_secret` |
| `login` | `sessions_restricted` | `state=sessions_restricted` |
| `protocolSignIn` | `sessions_restricted` | `state=sessions_restricted` |
| `redeemRecoveryEmail` | `success` | `state=recovery_email_token` |
| `registerPushDevice` | `already_registered` | `state=existing_device` |
| `registerRecoveryEmail` | `invalid_password`, `success` | `state=known_password` |
| `releaseHeldMessage` | `missing`, `not_held` | `state=two_factor_session` |
| `releaseHeldMessage` | `released` | `state=held_message` |
| `releaseHeldMessage` | `still_held` | `state=held_by_domain_limit` |
| `requestRecoveryEmail` | `success` | `state=verified_recovery_email` |
| `revokeClientSecret` | `missing` | `state=two_factor_session` |
| `revokeClientSecret` | `revoked` | `state=client_secret` |
| `sendDraft` | `discarded` | `state=discarding_pipeline` |
| `sendDraft` | `held` | `state=spent_send_limit` |
| `setAccountSendLimits` | `missing`, `owner_limit_exceeded`, `updated` | `state=two_factor_session` |
| `updateAPIKey` | `beyond_grants` | `principal=without_capability` |
| `updateServerCapabilities` | `from_environment`, `replaced`, `unknown_capability` | `state=two_factor_session` |
| `updateServerCapabilities` | `not_superadmin` | `principal=without_capability` |
| `verifyRecoveryEmail` | `success` | `state=pending_recovery_email` |

## Reproduction and limits

Use a fresh proofs pathname: the Go writer merges an existing file, so reusing a
previous run's inventory can incorrectly hide a skipped or failing proof.

```sh
go test ./internal/api -run '^TestRequirementExamplesThroughTheProductionRouter$' -count=1
RESTMAIL_EXAMPLE_PROOFS="$HOME/tmp/rest-mail-loop/fresh-proofs.json" \
  go test ./internal/api -count=1 -json
python3 scripts/example_proofs.py openapi-example.json \
  "$HOME/tmp/rest-mail-loop/fresh-proofs.json"
python3 -m unittest discover -s tests/contract -p test_example_proofs.py
```

`examplepairs.Proven` records only passing, non-skipped tests. `WriteProofs`
merges package inventories. `scripts/example_proofs.py` unions CI job files and
fails for each required operation/pair missing from that union. The additional
CLI regression tests cover every requirement family, combined requirements,
exact operation/pair identity, and duplicate proofs across job files.

No Docker command or container was used. Real PostgreSQL persistence, independent
Vertrag checks and the full merge-queue E2E job were not run locally and are not
claimed to pass. This remains **Part of #1237**, not a claim to close it.
