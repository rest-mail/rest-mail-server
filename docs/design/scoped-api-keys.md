# Scoped API keys

API keys delegate named capabilities at explicit resource scopes to automation.
They are separate from app passwords, which remain mail-client credentials.

## Credential and authority

- Transport: `Authorization: Bearer rmk_<public-id>_<secret>`.
- Store only the secret's SHA-256 hash. Return the raw token once, with
  `Cache-Control: no-store`; listings return metadata and permissions.
- Require 1–64 permissions. Each names a known capability other than `*`
  and a valid `*`, `domain:<name>` or `mailbox:<address>` scope.
- The creator and owner must both hold each delegated permission. Each use
  also intersects the key's permissions with the owner's current grants.
- Default expiry is 90 days; explicit expiry must be in the future.
- Revocation, expiry, account disablement and unfinished account setup refuse
  authentication. Password replacement/recovery revokes API keys too.

## Interactive management

`GET/POST /api/v1/accounts/{id}/api-keys` and
`DELETE /api/v1/accounts/{id}/api-keys/{credential_id}` operate on the caller's
own account or an account they may manage. Creation and revocation require a
session signed in with a second factor; creation additionally checks the
creator's current password. Key management never accepts another API key.

Keys carry no MFA or interactive-session claims. They cannot change credentials,
passwords, two-factor enrollment, or grants. Own-grant escalation also remains
interactive. Mail and ordinary admin operations require their explicit named
permission; server-wide list endpoints still need a server-wide grant until
resource-filtered scoped lists are implemented.

Creation and revocation append audit events in the same database transaction.
Requests retain the credential ID for audit attribution; secrets and passwords
are excluded. Authentication refusals use the existing per-IP/per-credential
failure budgets, and password reauthentication uses the login throttle.

## Verification

Unit tests cover parsing, hashing, credential bounds, middleware and management
responses. The required `chore test:contract-api-keys` CI step prepares a fresh
disposable `restmail.test` account, enrolls real MFA through HTTP and checks the
complete key lifecycle, scope refusals and immediate revocation. It leaves the
primary contract principal unchanged.
