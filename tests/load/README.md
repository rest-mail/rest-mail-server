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
| `smtp_out` | 50 msg/s           | signed-in submission on `restmail.test:465` to `alice@mail1.test`, which the gateway relays to the reference mail1 | `loadgen smtp -user`         |
| `imap`     | 1,000 connections  | opens them at 50 a second, signs each in, then holds them, each sending NOOP or SELECT INBOX every 10 seconds   | `loadgen imap`               |
| `api`      | 100 req/s          | signs in once, then `GET /api/v1/accounts` at a constant arrival rate                                           | `grafana/k6` (pinned digest) |

Every load runs for `--duration` seconds (default 60) at the same time, so the
install carries them all at once. Rates are open loop: a message is due on
schedule whether or not the last one finished, so a slow server shows as
latency and errors, never as less load quietly offered. A target of 0 leaves
that load out.

The loads sign in as the seeded fixture account `frank@restmail.test`
(`--user`, `--password`). `smtp_out` counts submissions the gateway accepted;
whether mail1 then received them all is not yet measured.

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
  once, and `extra` has the connect latency and drops;
- `resources.<container>`: average and peak CPU and memory of every service on
  the network, from `docker stats` samples taken through the run.

`results.txt` is the same in a few lines. The command exits 1 when any load
missed its target. `raw/` keeps each generator's own output and log.

`chore load:compare -- a.json b.json` prints each load's rate, p95, p99 and
errors side by side and counts what got worse by more than `--threshold`
percent (default 10); `--fail-on-regression` makes that an exit code.

## Where to run it

On one machine the generators and the install share the CPUs, so a run
measures that machine. A hosted GitHub runner (4 vCPUs) shows whether a commit
made things worse than the last run did; the capacity of one install needs the
topology on real hardware and the generators on another host.
