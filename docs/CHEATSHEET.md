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
chore restmail:up                      # the default instance (restmail.test); alias: chore dev
chore instance:new DOMAIN=mail4.test   # a brand-new instance end-to-end: scaffold → certs → mTLS → DNS → up → DKIM
chore instance:up --name mail4.test    # bring up an already-scaffolded instance
```

`restmail:up` chains postgres → js-filter → api → seed → gateways → webmail →
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
chore restmail:down                     # stop the default instance (keeps data volumes)
chore instance:down --name mail4.test  # stop a named instance
chore restmail:restart                  # stop + recreate (rebuilds images)
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

## Full teardown

```sh
chore e2e:down                                             # rest-mail + reference servers
chore instance:down --type reference --name mail2 # any stray reference server
chore testbed:down                                         # the substrate
```
