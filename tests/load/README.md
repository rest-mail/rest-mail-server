# Load tests

One command puts load on the e2e topology and writes what it measured
(#1583):

```sh
chore load                  # bring up the topology, run the load, tear it down
chore test:load             # against a topology that is already up (chore e2e:up)
chore test:load -- --smtp-in-rate 20 --imap-conns 200 --duration 120
chore load:compare -- before/results.json after/results.json
```

On GitHub, run the **Load** workflow (`.github/workflows/load.yml`) by hand;
its inputs are the targets below, and the results are its `load-results`
artifact.

## What runs

Each load runs in its own container on the topology's network
(`testbed_mailnet`, resolving through the testbed's DNS), so it reaches the
install the way a client on the internet does.

| Load       | Default            | What it does                                                                                                   | Generator                    |
|------------|--------------------|----------------------------------------------------------------------------------------------------------------|------------------------------|
| `smtp_in`  | 100 msg/s          | one message per connection to `restmail.test:25`, from `load@loadgen.test` to `frank@restmail.test`             | `loadgen smtp`               |
| `smtp_out` | 50 msg/s           | signed-in submission on `restmail.test:465` to `load@loadgen.test`; counts what the gateway accepted            | `loadgen smtp -user`         |
| `smtp_delivery` | as `smtp_out` | what then arrived at `loadgen.test`'s MX, and how long after it was sent                                          | `loadgen sink`               |
| `imap`     | 1,000 connections  | opens them at 50 a second, signs each in, then holds them, each sending NOOP or SELECT INBOX every 10 seconds   | `loadgen imap`               |
| `api`      | 100 req/s          | signs in once, then `GET /api/v1/accounts` at a constant arrival rate                                           | `grafana/k6` (pinned digest) |

Every load runs for `--duration` seconds (default 60) at the same time, so the
install carries them all at once. Rates are open loop: a message is due on
schedule whether or not the last one finished, so a slow server shows as
latency and errors, never as less load quietly offered. A target of 0 leaves
that load out.

The loads sign in as the seeded fixture account `frank@restmail.test`
(`--user`, `--password`).

### Outbound delivery

`smtp_out` is what the gateway accepted, so it says nothing of what happened
next. `smtp_delivery` is what arrived: the run starts a `loadgen sink` at
`--sink-ip` and publishes it as the MX of `loadgen.test` in the testbed DNS, the
way a remote domain is found, so the mail takes the real path out (queue worker,
MX lookup, DKIM signing, SMTP). The fragment is removed when the run ends.

Every message says which run sent it and when. The sink counts each one once and
takes its latency at arrival, in milliseconds, from the moment the generator
began sending it, so the latency covers submission, the queue and the relay. It
waits until all that was accepted has arrived or `--delivery-drain` seconds pass
after the last submission; what has not arrived by then counts as errors. The
rate is over the sending time or, when the queue fell behind, the time the mail
took to arrive.

It is a sink rather than a count at the reference mail1 because mail1 is a Postfix,
rspamd and Dovecot sharing the same CPUs, so its limits would be reported as REST
Mail's, and its mailbox can only be read back to the second.

### Limits the load cannot pass

A run sets out to find how much an install carries, and three limits meant for
abuse would stop it first. No production default changes for any of them.

| Limit | Why it stops a run | What the run does |
|---|---|---|
| Sends per account: 20 a minute, 100 an hour (`API_SEND_RATE_LIMIT_*`, `SEND_LIMIT_*`) | one account submits all the outbound load | `RESTMAIL_LOAD_LIMITS_OFF=true` at bring-up sets them to 0 on the testbed gateway |
| Sign-ins per client address: 1 a second, burst 15 (`AUTH_RATE_LIMIT_*`) | every SMTP and IMAP sign-in reaches the API from the gateway's one address | the same switch turns it off on the testbed API |
| Connections per client IP: 20 (`MaxPerIP`, in each gateway's `main.go`) | a generator is one container | each generator adds addresses to its own container and connects from them in turn |

`chore load` and the Load workflow set the switch. For `chore test:load`, bring
the topology up with `RESTMAIL_LOAD_LIMITS_OFF=true chore e2e:up`, or the submissions
are refused with `451 Sending temporarily held`. Each gateway's total of 1,000
connections (`MaxGlobal`) is left in place, so the default IMAP target sits right
at it.

`tests/load/loadgen` is a Go command in this module (unit tests beside it);
`run.py` builds it for the Docker host and runs it in a pinned Alpine image.

## Targets

| Flag                  | Environment           | Default |
|-----------------------|-----------------------|---------|
| `--smtp-in-rate`      | `LOAD_SMTP_IN_RATE`   | 100     |
| `--smtp-out-rate`     | `LOAD_SMTP_OUT_RATE`  | 50      |
| `--imap-conns`        | `LOAD_IMAP_CONNS`     | 1000    |
| `--api-rate`          | `LOAD_API_RATE`       | 100     |
| `--duration`          | `LOAD_DURATION`       | 60      |
| `--delivery-drain`    | `LOAD_DELIVERY_DRAIN` | 300     |
| `--out`               | `LOAD_OUT`            | `.cache/load` |

`python3 tests/load/run.py --help` lists the rest (addresses, recipients,
message size, the docker stats interval).

## Results

`results.json` holds:

- `tested`: the commit and `git describe` of the checkout that ran;
- `targets`, `duration_s`, the generator images, and the host's CPUs and memory;
- `loads.<kind>`: `achieved_rate`, `ok`, `errors`, `error_kinds`,
  `latency_ms` (p50, p90, p95, p99, max) and `met_target` (95% of the target,
  no errors). For `imap` the achieved figure is the most connections held at
  once, and `extra` has the connect latency and drops. For `smtp_delivery`, `ok`
  is the mail delivered, `errors` the mail accepted and not delivered, and `extra`
  has `accepted`, `delivered`, `duplicates` (a retry the sink saw again, not
  counted twice) and `other_messages` (not from this run, not counted);
- `resources.<container>`: average and peak CPU and memory of every service on
  the network, from `docker stats` samples taken through the run.

`results.txt` is the same in a few lines. The command exits 1 when any load
missed its target. `raw/` keeps each generator's own output and log.

`chore load:compare -- a.json b.json` prints each load's rate, p95, p99 and
errors (and for `smtp_delivery` the count delivered) side by side and counts what
got worse by more than `--threshold` percent (default 10); `--fail-on-regression`
makes that an exit code.

## Where to run it

On one machine the generators and the install share the CPUs, so a run
measures that machine. A hosted GitHub runner (4 vCPUs) shows whether a commit
made things worse than the last run did; the capacity of one install needs the
topology on real hardware and the generators on another host.
