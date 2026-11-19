Each clean API area gets its own JSON configuration. Files contain an area issue number mapped to sorted operation IDs.
Separate files let independent strict-mode PRs merge without overwriting one
another. The configuration is embedded in the generator; TestStrictAPIAreas
calls go-oapifly's Check for production and optional routes. Production omits
the test routes, so it reports their example pairs as orphaned; those orphans
are excused there because the test configuration registers and checks them.

The root strict_areas.json is the initial empty configuration. Do not remove an
enabled area to silence a warning; fix the reported contract gap.

Every operation that production or the test configuration registers must be in
an area: TestEveryRegisteredOperationIsStrict fails on one that is not, because
its description warnings would fail no test. A change that registers an operation
adds it to an area in the same change.
