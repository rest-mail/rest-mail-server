# Public read request budgets

This partial #1161 batch covers the public MTA-STS policy fetch (#631),
RESTMAIL recipient lookup (#643), and privileged health detail (#630).

On main `9377861bd5f2abffb0b53e21c21cbae10bf1f1ec`, the MTA-STS domain/policy
queries, recipient identity lookup and five auxiliary health reads did not carry
the HTTP context. A database/sql driver observed missing request values and
deadlines. Its blocked reads outlived explicit cancellation until a finite test
backstop. Health's initial `PingContext` already carried the request context.

The corrected handlers pass the request context through both policy reads,
recipient resolution, current-session checking, grant loading (including the
account's address), certificate lookup and its domain preload. No request or
response type, status declaration, generated client, description, oracle,
shared example or exception is changed.

Five registered-route test groups exercise actual GORM/database/sql calls
without PostgreSQL:

- Successful policy bytes, recipient identity and authorized certificate detail
  retain their existing wire forms while every read carries the request budget.
- Cancellation reaches both policy stages, the recipient read and all five
  privileged health stages. Mutations and unscripted reads are refused by the
  fixture. No sleep is used to simulate successful cancellation.
- A missing domain/policy remains 404; a dependency fault remains a sanitized 500.
  Missing or inactive recipients remain negative successes, while lookup faults
  remain 500. Private database diagnostics and certificate/key bytes stay off the
  response.
- Anonymous, invalid-token, setup-pending, stale-session, ungranted and
  domain-scoped callers receive public liveness only. A current session holding
  `domains:read` at `*` retains its certificate warnings.

The initial policy fixture used the wrong SQL table spelling. It was corrected
to `mtasts_policies` before the successful-policy baseline was recorded; that
fixture error is not a production policy-response finding.

Health's auxiliary reads retain their existing best-effort behavior: the probe
stays 200 after an auxiliary failure, and a failed domain preload may leave an
already loaded authorized warning without its domain name. This batch changes
the budget those reads use, not the probe's disclosure or failure policy.

These are driver and registered-handler proofs, not live database, certificate
inventory or installed-cluster acceptance. Wider PR/daily tiers, internal-mTLS
deployment, delivery write atomicity and mixed-report behavior remain partial.
Shared named pairs stay with #625/#1171. Existing unresolved API policy choices
and the first-recipient domain-policy behavior are preserved.
