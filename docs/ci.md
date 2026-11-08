# CI branch freshness

Every pull-request workflow that runs builds or tests starts with **Preflight:
branch is up to date**. It checks out the PR head (not GitHub’s synthetic merge
commit), fetches its own base branch’s current tip at run time, and requires that
tip to be an ancestor of the head. A stale branch runs no heavy jobs; the required
`CI` and `E2E (full topology)` verdicts fail. The error names both commits and gives
the repair command: `git fetch origin <base> && git rebase origin/<base>`, then push
again. Stacked PRs use their actual base, not necessarily `main`. Pushes, merge-queue
runs and manual dispatches pass preflight without an ancestry check; metadata-only
PR edits retain their existing ignored-edit behavior.

Locally, github-guard’s default-on `git-push-up-to-date` guard rejects branches that
do not contain `origin/main`. `.github-guard` explicitly states that default with
`guards.push-up-to-date = true` (the `push-up-to-date` key under `[guards]`); the
guard itself is maintained in `antimatter-studios/agent-skills`, not this repository.
An offline fetch warns and permits a local push; CI still enforces freshness.
Run `chore ci:local` on the exact rebased head before pushing.
