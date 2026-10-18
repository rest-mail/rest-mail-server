# rest-mail dev cheatsheet

The most common commands for the **local dev environment**, which is driven
entirely by [`chore`](https://github.com/antimatter-studios/chore) (`chore <name>`)
— there is no docker-compose. For the Kubernetes/Helm deployment instead, see
[`helm/restmail/README.md`](../helm/restmail/README.md).

## Mental model

One shared **substrate** plus the things that run on top of it:

- **testbed** — the substrate: the `mailnet` Docker network, a shared TLS certs
  volume, and a `dnsmasq` DNS server resolving the `.test` domains. Bring it up
  **first**; everything else joins it.
- **rest-mail instances** — the product. Each instance (default `restmail.test`)
  is a full stack: postgres, api, js-filter, the smtp/imap/pop3 gateways,
  webmail, admin. Several can run side by side (`restmail.test`, `mail4.test`, …),
  each under `config/<domain>/`.
- **reference servers** — traditional Postfix/Dovecot mail servers (`mail1`,
  `mail2`) to test against "real" mail software. A separate composer, launched
  one at a time.

## First-time setup

```sh
chore testbed:init      # clone the testbed, copy the restmail.test config, render config.env (once)
chore testbed:up        # start mailnet + certs volume + dnsmasq
chore mailserver:init   # OPTIONAL — clone reference-mailserver (once), only if you want mail1/mail2
```

Prerequisites: **Docker** and **chore** (`brew install antimatter-studios/tap/chore`).

## Start a server

```sh
chore instance:up                      # the default instance (restmail.test)
chore instance:new DOMAIN=mail4.test   # a brand-new instance end-to-end: scaffold → certs → mTLS → DNS → up → DKIM
chore instance:up --name mail4.test    # bring up an already-scaffolded instance
```

`instance:up` chains postgres → js-filter → api → seed → gateways → webmail →
admin, then prints `chore status`.

## Inspect what's running

```sh
chore status                      # FLEET view: every instance + reference servers + testbed + orphans
chore status --name restmail.test # narrow to a single instance
chore <svc>:logs                  # tail one service (api, smtp-gateway, postgres, js-filter, …)
chore ps                          # raw docker ps of rest-mail-* containers
docker ps                        # the real full picture (rest-mail + reference + testbed)
```

`chore status` legend: `● up`  `○ down`  `· absent`. Its **orphans** section flags
stale `rest-mail-*` containers left over from a previous run — clear them with
`chore purge`.

## Stop a server

```sh
chore instance:down                    # stop the default instance (keeps data volumes)
chore instance:down --name mail4.test  # stop a named instance
chore instance:restart                 # stop + recreate (rebuilds images)
```

## Reference servers (mail1 / mail2)

```sh
chore mailserver:certs:issue                                 # issue their TLS certs (once per run)
chore instance:up --type reference --name mail1    # start one (or --name mail2)
chore instance:down --type reference --name mail1  # stop one
```

## The whole topology at once

```sh
chore e2e:up      # testbed + reference mail1/mail2 + restmail.test
chore e2e:down    # tear it all down (keeps data volumes)
chore e2e         # up → run the e2e test suite → down
```

## Individual services

Every service supports `:up`, `:down`, `:restart`, `:logs`, `:build`:

```sh
chore api:restart
chore smtp-gateway:logs
```

Services: `postgres`, `api`, `js-filter`, `smtp-gateway`, `imap-gateway`,
`pop3-gateway`, `webmail`, `admin`.

## Data & cleanup

```sh
chore db:seed              # seed fixture users into the instance database
chore purge                # remove all rest-mail-* containers (keeps volumes)
chore postgres:reset       # DESTRUCTIVE — drop the postgres data volume + recreate
chore db:reset             # DESTRUCTIVE — drop the DB volume, recreate, reseed
chore testbed:down         # stop the testbed (keeps certs volume + network)
chore testbed:down:clean   # DESTRUCTIVE — also remove the network + certs + DNS fragments
```

## Optional extras

```sh
chore monitoring:up | :down          # prometheus + grafana + postgres-exporter
chore website:init | :up | :down     # the marketing website (separate subproject)
```

## Edit the extracted website

The website's `master` branch in [rest-mail/website](https://github.com/rest-mail/website)
is authoritative. The helpers use a separate, gitignored working copy at
`.workspace/website`; they never commit website source to this server repository.
Git and Node >=22.12 (with npm) are needed for development.

```sh
chore website:dev                    # clone master if missing, npm ci, npm run dev
# Open the URL printed by upstream Vite (normally http://localhost:5173).
# Ctrl-C stops the foreground dev server.
EDITOR='code --wait' chore website:edit
# Prefer a website branch for review before publishing changes:
git -C .workspace/website switch -c fix/site-copy
chore website:push --message "fix: clarify the site copy"
chore website:reset                  # fetch master; refuse to lose edits or local commits
```

`website:dev` follows the current upstream package lock and live-reload script.
It runs `npm ci` each time (replacing that working copy's `node_modules`) before
`npm run dev`. Upstream now uses TanStack/Node, so binding raw source over an
nginx web root would not serve the current site. Production website tasks,
images and deployment configuration retain their existing ownership and behavior.
The helpers do not update an existing working copy on dev/edit; use normal Git
commands to update or switch it. `VISUAL` takes precedence over `EDITOR`, with
`vi` as the fallback; editor commands may include flags.

`website:push` stages **all nonignored changes** in the website working copy,
commits with the supplied message when dirty, and pushes its **current branch**
to `origin` without force. A clean copy pushes existing commits without needing a
message. It does not open or merge a PR; use the website repository's review flow.
Both fetch and push URLs must identify `rest-mail/website`; an unrelated existing
repository is refused rather than replaced. `WEBSITE_DIR` can select another
website working copy; it must be a repository root with the same upstream.

To deliberately discard work, inspect that exact copy first:

```sh
git -C .workspace/website status --short
git -C .workspace/website log --oneline origin/master..HEAD
chore website:reset --discard-local-work
```

The flag explicitly permits resetting the **current branch** to fetched upstream
`master`, losing local commits and tracked edits, and deleting nonignored untracked
files/directories (`git clean -fd`). Ignored files such as dependencies are kept
by the clean step. Without it, dirty work and commits absent from upstream block
the reset. Save wanted work on another branch or outside the copy first.
`website:push` and `website:reset` require an existing validated copy; neither
silently creates one.

## Full teardown

```sh
chore e2e:down                                             # rest-mail + reference servers
chore instance:down --type reference --name mail2 # any stray reference server
chore testbed:down                                         # the substrate
```
