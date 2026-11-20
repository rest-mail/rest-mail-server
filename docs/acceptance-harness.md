# Acceptance harness

How a criterion that needs a live installation gets proven. This exists so that a criterion
in the ledger's `H` bucket has a route to `P` that does not depend on someone remembering
the steps or being careful with shared state.

## The one rule

**Create it, prove it, destroy it.** Every recipe makes its own resources under a name no
other run can have, proves the property against them, and removes them again. A run that
leaves anything behind is a failed run, whatever the assertions said.

That is what makes a destructive test safe: nothing is destroyed except what the run created
a minute earlier, so "blast radius" is the prefix, not the install.

## What a recipe may touch

| Allowed | Never |
|---|---|
| A subdomain the run created under a run-scoped label, e.g. `a-7f3c2d1.rest-mail.com` | An apex or pre-existing domain, or a sibling run's name |
| A mailbox, alias, folder or contact the run created | An account or mailbox that existed before the run |
| A temporary certificate for its own name | The installation's serving certificate |
| A temporary object prefix in the object store | Any prefix it did not create |
| Its own reset of its own resources | A global setting, a restart of a shared service, a DNS record another name depends on |

The install may serve other work while a recipe runs. A recipe that could take it down is not
an acceptance test, it is an outage with assertions.

## Teardown on the failure path

Teardown is registered before the first resource is created, so a failed assertion, a timeout
and an interrupt all remove what exists. In shell that means a `trap` set before the create;
in Go a `defer` set before the create. A recipe that only destroys on success is how orphans
are made.

## Receipts

Every run writes a receipt. The receipt is what the ledger cites when it moves a criterion to
`P`, and it is what a stale receipt check reads later.

```json
{
  "criterion": "2104/acme-renewal",
  "sha": "0b7c1d9e4f",
  "run_id": "44694152387",
  "installation": "rest-mail.com",
  "prefix": "a-7f3c2d1",
  "started": "2026-11-20T08:02:00Z",
  "finished": "2026-11-20T08:09:41Z",
  "created": ["a-7f3c2d1.rest-mail.com", "a-7f3c2d1.rest-mail.com:postmaster"],
  "destroyed": ["a-7f3c2d1.rest-mail.com:postmaster", "a-7f3c2d1.rest-mail.com"],
  "observations": [
    "issuer CN=R3, notBefore 08:03:12Z",
    "renewal was refused after the account key was replaced"
  ],
  "result": "passed"
}
```

`scripts/acceptance_receipt.py` validates it, and refuses:

- a created name that does not start with the run's `prefix` (the blast-radius rule);
- a created name that is not in `destroyed` (an orphan);
- a `destroyed` name that was never `created` (a run claiming to clean up someone else's work);
- a finished time before the start, or a missing criterion/sha/installation;
- a `result` of `passed` with no observations (nothing was actually observed).

The same rule is what a sweeper may act on: it removes only names carrying a known run prefix
and older than a stated age, never by pattern on the installation's own names. That is
`#2113` AC03 ("cleanup affects only verified run-owned resources") applied to acceptance runs.

## Why the receipt, and not the log

A log proves that something ran. A receipt proves **what it created and that it removed it**,
which is the part that can hurt. It also gives the ledger a way to tell a fresh proof from a
stale one: a receipt names the sha it ran against, so a criterion whose receipt predates the
head is unproven again, exactly like the release gate's receipts.

## Where recipes live

`acceptance/<issue>/<criterion>/` — the recipe, the fixture it needs, and a short `README.md`
naming the preconditions, the expected observations and what would make the run refuse. The
prerequisites (DNS credentials, ACME account, an SSH key) are named there and held as CI
secrets, never in the recipe.
