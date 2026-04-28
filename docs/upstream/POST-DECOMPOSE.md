# Post-Decomposition Followups

**Date:** 2026-04-28
**Status:** Open questions to resolve once the upstream split is in place. Not blocking the decomposition itself, but worth solving early so the new structure is genuinely livable.

## 1. Iterating on the website locally

**Problem.** Once `website/` is removed from this repo and the site lives entirely in [`rest-mail/website`](https://github.com/rest-mail/website), the local-dev experience for editing the site degrades:

- The published image (`ghcr.io/rest-mail/website:latest`) is built from `master` — no good for trying changes before pushing.
- Cloning the upstream repo separately and switching contexts to edit it works, but breaks the "one window, one workflow" feel.

**Idea.** A small script (or task) that:

1. Clones `rest-mail/website` into a known local path (e.g. `.local/website/` — gitignored).
2. Runs the upstream image with the local clone bind-mounted as an overlay onto `/usr/share/nginx/html/`, so edits to the local clone are served live.
3. Provides a way to commit + push from that overlay path back to `rest-mail/website`.

Sketch:

```bash
task website:dev    # clones (if missing) + runs nginx with the clone overlaid, hot reload
task website:edit   # opens the local clone in $EDITOR
task website:push   # commits + pushes the local clone to upstream
task website:reset  # blows away the local clone
```

Implementation should be a thin shell script — no fancy orchestration. The clone is purely a local working copy; `master` of the upstream repo is the source of truth.

Open: should the script also work for other reference repos (cloning + overlay-editing `reference-postfix`, `reference-dovecot`, etc.)? A generalized `task upstream:edit -- <repo>` would be nicer than per-repo tasks. Defer until we have ≥2 repos with the same need.

## 2. Tests — where do they live?

**Problem.** The current repo has `tests/e2e/` covering protocol-level smoke tests across mail1/mail2/mail3. After decomposition:

- `tests/e2e/` covers behavior that spans **multiple repos** — restmail (mail3), the reference-mailserver (mail1/mail2), and the testbed (DNS, certs).
- A test like "mail1 sends mail3 a message and mail3 receives it" can't live in any one of those repos cleanly. It's a *cross-stack* concern.

**Options:**

- **(a) Keep cross-stack tests in `rest-mail-server`.** Pragmatic — restmail is the product, cross-stack delivery is what verifies the product. Cost: the test suite has to know how to spin up reference-mailserver instances, which feels like wrong-direction coupling.
- **(b) Move them to `rest-mail/testbed`.** The testbed is already the thing that wires multi-stack environments together, so the tests that exercise those wirings naturally belong there. Cost: testbed becomes more than just network/DNS/certs — it becomes a full integration-test harness.
- **(c) New repo `rest-mail/integration-tests`.** Pure cross-stack test suite. Imports nothing from any product repo. Pulls images, brings up testbed + restmail + reference-mailserver, runs the tests. Cost: another repo to maintain, more coordination overhead.

**Tentative lean:** (b). Testbed is already the harness; tests that exercise the harness belong with it. Per-repo unit/integration tests stay in their own repos (restmail's gateway tests stay in restmail, postfix's image-smoke-test stays in `reference-postfix`). The testbed owns "does the whole thing work end-to-end."

Decide before Phase 4 (the slim-down of `rest-mail-server`), so we know whether to move `tests/e2e/` out or keep it.

## 3. CI/release coordination across N image repos

**Problem.** Once we have 8+ image repos, each with its own `build.yml` workflow, drift is inevitable. Today's workflow in [rest-mail/website](https://github.com/rest-mail/website) will diverge from tomorrow's workflow in `reference-postfix`.

**Idea.** Centralize the workflow in a single source: the `rest-mail/conventions` repo (or a `rest-mail/.github` repo) hosts a reusable workflow. Each image repo's `build.yml` is two lines:

```yaml
jobs:
  build:
    uses: rest-mail/conventions/.github/workflows/build-image.yml@v1
```

Solves drift, calver tagging logic, GHCR auth, healthcheck verification — all once, central.

Defer until we have ≥3 image repos. Premature otherwise.

## 4. The `decompose/phase-0` branch lifecycle

This branch holds the planning docs and the upstream-extraction prep. The actual code changes (deletions, Taskfile updates) will likely span multiple PRs and multiple phases. Decide early:

- Does each phase get its own PR off `main`, with `decompose/phase-0` as planning-only?
- Or does the whole decomposition land on one long-lived branch, merged at the end?

Recommend the former. Long-lived branches accumulate conflicts.

## 5. Restmail's gateway log path coupling to fail2ban

The current `docker/fail2ban/jail.local` reads from `/var/log/restmail/<gateway>.log`. Once `reference-fail2ban` is generic and the jail definitions move into restmail, this coupling needs to be explicit:

- Restmail's gateways write to `/var/log/restmail/`
- Restmail's repo holds the fail2ban jail file referencing those logs
- Restmail's compose mounts the jail file into the generic fail2ban container

Verify that the gateways actually write to file logs (vs only stdout). If they only stdout, fail2ban needs a different signal source — likely journald or the Docker log driver, not log files. May reshape this whole thing.
