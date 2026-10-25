# Generator warning issues

`API gap issues` runs on each main push and by workflow dispatch. On pull
requests it validates the same report and reconciliation plan without forge
writes. It never reads CI results, merges PRs, releases, or deploys anything.

The report comes from the live registered routers, including production,
non-production testing and configured APNs routes, without a live database.
go-oapifly 0.30.0 already provides its structured report; no annotation-scanner
fallback is needed. `api-areas.json` assigns each operation to exactly one area
issue, #630–#652. ACME's infrastructure responder is outside this API registry.
New or omitted operations fail validation before issue changes occur.

Local, read-only plan:

```sh
RESTMAIL_API_GAP_REPORT=/tmp/restmail-api-gaps.json \
  go test ./internal/api/ -run '^TestAPIGapReport$' -count=1
python3 -B scripts/api_gaps.py /tmp/restmail-api-gaps.json
```

Only the main workflow adds `--apply`. It creates or adopts one `api-gap` issue
per operation with warnings, attaches it as a sub-issue of its area, updates its
generated block, closes it when clean and reopens it when a warning returns.
Handwritten body text outside the markers and all comments are preserved.
Duplicate tracking issues are consolidated into the oldest one. Invalid or
incomplete reports and malformed markers fail closed. No API error is retried;
the next main run reconciles any unfinished issue creation or parent linking.
Content writes are paced, and unchanged issues produce no write.

A clean area gets a ready PR adding its own file under
`internal/api/strict_areas/`. Independent PRs cannot overwrite another area's
configuration. The generator embeds these files as its go-oapifly strict scope;
`TestStrictAPIAreas` rejects scoped warnings in CI. Existing proposals and human
closures are respected. Interrupted branch/commit creation resumes without
rewriting a ref. The PR coordinator owns subsequent review, checks and merging.

The workflow requests issue, content and PR write permissions. It uses
`PIPELINE_RESTMAIL_TOKEN` when available, otherwise the workflow token. GitHub's
existing policy must allow that token to create PRs; changing this policy remains
an owner action (#628). PRs opened by the workflow token need the normal PR
coordinator flow to trigger required pull-request checks. The workflow does not
alter repository settings or dispatch substitute required checks.
