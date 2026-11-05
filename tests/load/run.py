#!/usr/bin/env python3
"""One load run against a running REST Mail topology (#1583).

Starts each load in its own container on the topology's network, as another
user of the simulated internet (the way `chore test:e2e` runs the e2e suite):

  smtp_in   messages to restmail.test:25, one per connection   (tests/load/loadgen)
            from a sender on the domain's allowlist, so past the greylist
  smtp_in_greylist  the same from new senders, which the greylist defers;
            what that costs is its own figure                  (tests/load/loadgen)
  smtp_out  authenticated submissions on :465 to loadgen.test  (tests/load/loadgen)
            recipient, what the gateway ACCEPTED
  smtp_delivery  what then ARRIVED at loadgen.test's MX, a sink (tests/load/loadgen)
            this run publishes in the testbed DNS, and how long it took
  imap      N authenticated connections held open, NOOP/SELECT (tests/load/loadgen)
  api       authenticated GETs at a constant arrival rate      (pinned grafana/k6)

While they run it samples `docker stats` for every other container. It writes
one JSON file (what was tested, each load's achieved rate, latency percentiles,
errors, and each service's CPU and memory) and a short text summary beside it.
When the run ends it saves the gateway's and the API's logs, and what the database
says became of the mail, beside the raw results.
tests/load/compare.py compares two of those files.

A target of 0 leaves that load out.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import math
import os
import platform
import re
import shutil
import subprocess
import sys
import threading
import time
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

# Pinned like every image the topology uses: a run compares against another
# only if the generators are the same.
K6_IMAGE = "grafana/k6:0.54.0@sha256:1f40432b1cbe7234e977f96c362c9bc550a2d2b583d014dd8669fe40d3e9e755"
RUNNER_IMAGE = "alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc"

# Seconds a generator gets after its duration to finish what is in flight.
GENERATOR_DRAIN = 300

SCHEMA = 1

# What a gateway allows one client IP (cmd/*-gateway: MaxPerIP 20). The generators
# connect from a pool of addresses, stay under it, and so measure the install
# rather than that limit; the pools are outside what the topology and Docker use.
MAX_PER_IP = 18
SMTP_IN_SOURCES = 201
SMTP_OUT_SOURCES = 202
IMAP_SOURCES = 203
SMTP_IN_GREYLIST_SOURCES = 204
SMTP_MAX_INFLIGHT = 500
SMTP_IN_GREYLIST_MAX_INFLIGHT = 100

# The logs a run keeps, as many lines of each as a run's mail takes, and what the
# database says became of that mail. Both go in the artifact: without them the first
# run could only be explained from the code (#1583).
LOG_SERVICES = ("rest-mail-smtp-gateway", "rest-mail-api")
LOG_LINES = 50000
DB_CONTAINER = "rest-mail-postgres"
DB_REPORT = """
SELECT status, attempts, failure_category, count(*) FROM outbound_queue GROUP BY 1, 2, 3 ORDER BY 4 DESC;
SELECT direction, outcome, reason_code, count(*) FROM message_traces GROUP BY 1, 2, 3 ORDER BY 4 DESC;
SELECT passed, count(*) FROM greylist_entries GROUP BY 1;
"""

FRAGMENT = "20-load-sink.conf"


def parse_args(argv: list[str]) -> argparse.Namespace:
    env = os.environ.get
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--smtp-in-rate", type=float, default=float(env("LOAD_SMTP_IN_RATE", "100")), help="inbound messages per second (default 100)")
    p.add_argument("--smtp-in-greylist-rate", type=float, default=float(env("LOAD_SMTP_IN_GREYLIST_RATE", "10")), help="inbound messages per second the greylist defers, each from a new sender (default 10)")
    p.add_argument("--smtp-out-rate", type=float, default=float(env("LOAD_SMTP_OUT_RATE", "50")), help="outbound submissions per second (default 50)")
    p.add_argument("--imap-conns", type=int, default=int(env("LOAD_IMAP_CONNS", "1000")), help="concurrent IMAP connections (default 1000)")
    p.add_argument("--api-rate", type=float, default=float(env("LOAD_API_RATE", "100")), help="API requests per second (default 100)")
    p.add_argument("--duration", type=int, default=int(env("LOAD_DURATION", "60")), help="seconds each load runs (default 60)")
    p.add_argument("--imap-connect-rate", type=float, default=float(env("LOAD_IMAP_CONNECT_RATE", "50")), help="new IMAP connections per second while ramping")
    p.add_argument("--out", default=env("LOAD_OUT", str(ROOT / ".cache" / "load")), help="directory for results.json, results.txt and the raw files")
    p.add_argument("--network", default=env("RESTMAIL_NETWORK", "testbed_mailnet"))
    p.add_argument("--dns", default=env("RESTMAIL_TESTBED_DNS_IP", "10.99.0.10"), help="the testbed resolver the loads use")
    p.add_argument("--api", default=env("API_BASE_URL", "http://10.99.0.20:8080"))
    p.add_argument("--smtp-in-addr", default="restmail.test:25")
    p.add_argument("--smtp-in-from", default="load@loadgen.test")
    p.add_argument("--smtp-in-to", default="frank@restmail.test")
    p.add_argument("--smtp-in-greylist-from", default="greylisted@loadgen.test", help="tagged per message, and not on the allowlist")
    p.add_argument("--admin-user", default=env("LOAD_ADMIN_USER", "admin@restmail.test"), help="admin who puts the inbound sender on the allowlist (a seeded fixture)")
    p.add_argument("--admin-password", default=env("LOAD_ADMIN_PASSWORD", "admin123!@"))
    p.add_argument("--submit-addr", default="restmail.test:465")
    p.add_argument("--sink-domain", default="loadgen.test", help="the remote domain outbound load is sent to; its MX is the sink")
    p.add_argument("--sink-ip", default=env("LOAD_SINK_IP", "10.99.0.250"), help="the sink's address on the topology's network")
    p.add_argument("--smtp-out-to", default="load@loadgen.test")
    p.add_argument("--delivery-drain", type=int, default=int(env("LOAD_DELIVERY_DRAIN", "300")), help="seconds after the last submission to wait for delivery (default 300)")
    p.add_argument("--dnsmasq-container", default=env("DNSMASQ_CONTAINER", "testbed-dnsmasq"))
    p.add_argument("--dnsmasq-fragments", default=env("DNSMASQ_FRAGMENTS_VOLUME", "testbed_dnsmasq_fragments"))
    p.add_argument("--source-net", default=env("LOAD_SOURCE_NET", "10.99"), help="first two octets of the generators' extra addresses, inside the network's /16")
    p.add_argument("--imap-addr", default="imap.restmail.test:993")
    p.add_argument("--user", default=env("LOAD_USER", "frank@restmail.test"), help="account the loads sign in as (a seeded fixture)")
    p.add_argument("--password", default=env("LOAD_PASSWORD", "password123"))
    p.add_argument("--message-size", type=int, default=4096)
    p.add_argument("--stats-interval", type=float, default=5.0, help="seconds between docker stats samples")
    p.add_argument("--name-prefix", default=env("LOAD_NAME_PREFIX", "restmail-load"), help="names of the containers this run starts")
    p.add_argument("--label", default=env("LOAD_LABEL", "rest-mail-load=1"), help="label on the containers this run starts")
    return p.parse_args(argv)


def run(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, check=True, text=True, **kw)


def git(*args: str) -> str:
    try:
        return subprocess.run(["git", "-C", str(ROOT), *args], check=True, text=True, capture_output=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return ""


def docker_arch() -> str:
    arch = subprocess.run(["docker", "info", "--format", "{{.Architecture}}"], check=True, text=True, capture_output=True).stdout.strip()
    return {"x86_64": "amd64", "aarch64": "arm64"}.get(arch, arch)


def build_loadgen(bindir: Path) -> Path:
    bindir.mkdir(parents=True, exist_ok=True)
    out = bindir / "loadgen"
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=docker_arch())
    run(["go", "build", "-trimpath", "-o", str(out), "./tests/load/loadgen"], cwd=ROOT, env=env)
    return out


class Stats(threading.Thread):
    """Samples `docker stats` for the topology's containers, not the run's own."""

    def __init__(self, interval: float, skip_prefix: str, names: list[str]):
        super().__init__(daemon=True)
        self.interval = interval
        self.skip_prefix = skip_prefix
        self.names = names
        self.samples: dict[str, list[dict]] = {}
        self.stop = threading.Event()

    def run(self) -> None:
        while self.names and not self.stop.is_set():
            t0 = time.monotonic()
            try:
                out = subprocess.run(["docker", "stats", "--no-stream", "--format", "{{json .}}", *self.names],
                                     check=True, text=True, capture_output=True, timeout=60).stdout
            except (subprocess.SubprocessError, OSError):
                out = ""
            for line in out.splitlines():
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                name = row.get("Name", "")
                if not name or name.startswith(self.skip_prefix):
                    continue
                self.samples.setdefault(name, []).append({
                    "cpu_pct": percent(row.get("CPUPerc", "")),
                    "mem_mib": mib(row.get("MemUsage", "").split("/")[0]),
                    "net_io": row.get("NetIO", ""),
                    "block_io": row.get("BlockIO", ""),
                    "pids": row.get("PIDs", ""),
                })
            self.stop.wait(max(0.0, self.interval - (time.monotonic() - t0)))

    def summary(self) -> dict:
        out = {}
        for name, rows in sorted(self.samples.items()):
            cpu = [r["cpu_pct"] for r in rows if r["cpu_pct"] is not None]
            mem = [r["mem_mib"] for r in rows if r["mem_mib"] is not None]
            out[name] = {
                "samples": len(rows),
                "cpu_pct": {"avg": avg(cpu), "max": max(cpu, default=None)},
                "mem_mib": {"avg": avg(mem), "max": max(mem, default=None)},
                "net_io_last": rows[-1]["net_io"],
                "block_io_last": rows[-1]["block_io"],
                "pids_last": rows[-1]["pids"],
            }
        return out


def network_containers(network: str) -> list[str]:
    """Every container on the topology's network: the services under load."""
    try:
        out = subprocess.run(["docker", "network", "inspect", network, "--format", "{{range .Containers}}{{.Name}} {{end}}"],
                             check=True, text=True, capture_output=True).stdout
    except (OSError, subprocess.CalledProcessError) as e:
        sys.exit(f"network {network} is not there: is the topology up? ({e})")
    return sorted(out.split())


def percent(s: str) -> float | None:
    try:
        return float(s.strip().rstrip("%"))
    except ValueError:
        return None


_UNITS = {"B": 1 / 2**20, "KIB": 1 / 1024, "MIB": 1, "GIB": 1024, "TIB": 2**20,
          "KB": 1e3 / 2**20, "MB": 1e6 / 2**20, "GB": 1e9 / 2**20}


def mib(s: str) -> float | None:
    m = re.fullmatch(r"\s*([0-9.]+)\s*([A-Za-z]+)\s*", s)
    if not m or m.group(2).upper() not in _UNITS:
        return None
    return round(float(m.group(1)) * _UNITS[m.group(2).upper()], 1)


def avg(xs: list[float]) -> float | None:
    return round(sum(xs) / len(xs), 2) if xs else None


def k6_result(path: Path, target: float, duration: int) -> dict:
    """The k6 summary export in the shape loadgen writes."""
    m = json.loads(path.read_text())["metrics"]
    reqs = int(m.get("http_reqs", {}).get("count", 0))
    failed = int(m.get("http_req_failed", {}).get("passes", 0))
    dur = m.get("http_req_duration", {})
    # setup() signs in once; that request is not part of the load.
    attempted = max(0, reqs - 1)
    ok = max(0, attempted - failed)
    dropped = int(m.get("dropped_iterations", {}).get("count", 0))
    res = {
        "kind": "api", "target": target, "target_unit": "req/s", "duration_s": duration,
        "attempted": attempted + dropped, "ok": ok, "errors": failed + dropped,
        "achieved_rate": round(ok / duration, 3) if duration else 0,
        "latency_ms": {k: round(dur.get(f"p({k[1:]})" if k != "max" else "max", 0), 3) for k in ("p50", "p90", "p95", "p99", "max")},
        "extra": {"dropped_iterations": dropped},
    }
    if failed or dropped:
        res["error_kinds"] = {k: v for k, v in (("http status >= 400 or no response", failed), ("dropped: k6 ran out of VUs", dropped)) if v}
    return res


def sink_fragment(a: argparse.Namespace) -> str:
    """The testbed DNS for the sink's domain, in dnsmasq's fragment format.

    It is also the inbound load's sender domain, so that has an SPF record to
    pass and somewhere to send a bounce. Everything the generators connect from
    is inside the network's /16.
    """
    return (f"# tests/load/run.py: the remote domain of a load run; removed when the run ends.\n"
            f"address=/{a.sink_domain}/{a.sink_ip}\n"
            f"mx-host={a.sink_domain},{a.sink_domain},10\n"
            f'txt-record={a.sink_domain},"v=spf1 ip4:{a.source_net}.0.0/16 -all"\n')


def source_count(concurrent: int) -> int:
    """How many addresses keep this many connections under one gateway's per-IP limit."""
    return min(254, max(1, math.ceil(concurrent / MAX_PER_IP)))


def from_sources(a: argparse.Namespace, octet: int, count: int) -> tuple[str, list[str]]:
    """Shell that adds a generator's addresses, and the flags that use them."""
    base = f"{a.source_net}.{octet}"
    script = (f"i=1; while [ $i -le {count} ]; do ip addr add {base}.$i/16 dev eth0 || exit 1; i=$((i+1)); done; ")
    return script, ["-source-ips", f"{base}.1-{count}"]


def build_jobs(a: argparse.Namespace, loadgen: Path, raw: Path, resolv: Path, uid: str, run_id: str) -> dict[str, list[str]]:
    """The docker command of each load that has a target."""

    def container(name: str, image: str, mounts: list[str], env: dict[str, str], cmd: list[str], flags: list[str] | None = None) -> list[str]:
        c = ["docker", "run", "--rm", "--name", f"{a.name_prefix}-{name}", "--label", a.label,
             "--network", a.network, "-e", "HOME=/tmp",
             "-v", f"{resolv}:/etc/resolv.conf:ro", "-v", f"{raw}:/out", *(flags or ["-u", uid])]
        for m in mounts:
            c += ["-v", m]
        for k, v in env.items():
            c += ["-e", f"{k}={v}"]
        return c + [image, *cmd]

    # Adding an address takes root and NET_ADMIN in the generator's own container.
    add_addresses = ["--cap-add", "NET_ADMIN"]
    lg = [f"{loadgen}:/loadgen:ro"]
    d = f"{a.duration}s"
    jobs: dict[str, list[str]] = {}
    if a.smtp_in_rate > 0:
        script, sources = from_sources(a, SMTP_IN_SOURCES, source_count(SMTP_MAX_INFLIGHT))
        jobs["smtp_in"] = container("smtp-in", RUNNER_IMAGE, lg, {}, [
            "sh", "-c", script + 'exec /loadgen smtp -kind smtp_in "$@"', "loadgen", *sources,
            "-addr", a.smtp_in_addr, "-rate", str(a.smtp_in_rate), "-max-inflight", str(SMTP_MAX_INFLIGHT),
            "-duration", d, "-from", a.smtp_in_from, "-to", a.smtp_in_to, "-size", str(a.message_size), "-out", "/out/smtp_in.json"], add_addresses)
    if a.smtp_in_greylist_rate > 0:
        script, sources = from_sources(a, SMTP_IN_GREYLIST_SOURCES, source_count(SMTP_IN_GREYLIST_MAX_INFLIGHT))
        jobs["smtp_in_greylist"] = container("smtp-in-greylist", RUNNER_IMAGE, lg, {}, [
            "sh", "-c", script + 'exec /loadgen smtp -kind smtp_in_greylist -expect-defer -unique-from "$@"', "loadgen", *sources,
            "-addr", a.smtp_in_addr, "-rate", str(a.smtp_in_greylist_rate), "-max-inflight", str(SMTP_IN_GREYLIST_MAX_INFLIGHT),
            "-duration", d, "-from", a.smtp_in_greylist_from, "-to", a.smtp_in_to, "-size", str(a.message_size), "-out", "/out/smtp_in_greylist.json"], add_addresses)
    if a.smtp_out_rate > 0:
        script, sources = from_sources(a, SMTP_OUT_SOURCES, source_count(SMTP_MAX_INFLIGHT))
        jobs["smtp_delivery"] = container("smtp-delivery", RUNNER_IMAGE, lg, {}, [
            "/loadgen", "sink", "-addr", ":25", "-kind", "smtp_delivery", "-run", run_id,
            "-sent-file", "/out/smtp_out.json", "-drain", f"{a.delivery_drain}s",
            "-duration", f"{a.duration + GENERATOR_DRAIN + a.delivery_drain}s",
            "-ready-file", "/out/sink.ready", "-out", "/out/smtp_delivery.json"],
            ["-u", uid, "--ip", a.sink_ip, "--sysctl", "net.ipv4.ip_unprivileged_port_start=0"])
        jobs["smtp_out"] = container("smtp-out", RUNNER_IMAGE, lg, {"LOAD_PASSWORD": a.password}, [
            "sh", "-c", script + 'exec /loadgen smtp -kind smtp_out -tls -pass "$LOAD_PASSWORD" "$@"', "loadgen", *sources,
            "-addr", a.submit_addr, "-user", a.user, "-from", a.user, "-to", a.smtp_out_to, "-rate", str(a.smtp_out_rate),
            "-max-inflight", str(SMTP_MAX_INFLIGHT), "-run", run_id,
            "-duration", d, "-size", str(a.message_size), "-out", "/out/smtp_out.json"], add_addresses)
    if a.imap_conns > 0:
        script, sources = from_sources(a, IMAP_SOURCES, source_count(a.imap_conns))
        jobs["imap"] = container("imap", RUNNER_IMAGE, lg, {"LOAD_PASSWORD": a.password}, [
            "sh", "-c", script + 'exec /loadgen imap -pass "$LOAD_PASSWORD" "$@"', "loadgen", *sources,
            "-addr", a.imap_addr, "-user", a.user, "-conns", str(a.imap_conns), "-connect-rate", str(a.imap_connect_rate),
            "-duration", d, "-out", "/out/imap.json"], add_addresses)
    if a.api_rate > 0:
        jobs["api"] = container("api", K6_IMAGE, [f"{ROOT / 'tests' / 'load'}:/scripts:ro"], {
            "API_BASE_URL": a.api, "API_RATE": str(int(a.api_rate)), "DURATION": d,
            "LOAD_USER": a.user, "LOAD_PASSWORD": a.password,
        }, ["run", "--quiet", "--no-usage-report", "--summary-export", "/out/api-k6.json", "/scripts/api.js"])
    return jobs


def publish_sink_dns(a: argparse.Namespace) -> None:
    """Writes the sink's DNS fragment and reloads dnsmasq, as the topology's own consumers do."""
    subprocess.run(["docker", "run", "--rm", "-i", "--label", a.label, "-v", f"{a.dnsmasq_fragments}:/dst", RUNNER_IMAGE,
                    "sh", "-c", f"cat > /dst/{FRAGMENT}"], input=sink_fragment(a), check=True, text=True)
    run(["docker", "restart", a.dnsmasq_container], capture_output=True)


def wait_for_sink_dns(a: argparse.Namespace, timeout: float = 60) -> None:
    """Delivery asks this resolver for the MX: nothing is sent until it answers."""
    deadline = time.monotonic() + timeout
    while True:
        check = subprocess.run(["docker", "run", "--rm", "--label", a.label, "--network", a.network, RUNNER_IMAGE,
                                "nslookup", "-type=mx", a.sink_domain, a.dns], capture_output=True, text=True)
        if a.sink_domain in check.stdout and "mail exchanger" in check.stdout:
            return
        if time.monotonic() > deadline:
            sys.exit(f"the testbed DNS at {a.dns} has no MX for {a.sink_domain}:\n{check.stdout}{check.stderr}")
        time.sleep(1)


def remove_sink_dns(a: argparse.Namespace) -> None:
    subprocess.run(["docker", "run", "--rm", "--label", a.label, "-v", f"{a.dnsmasq_fragments}:/dst", RUNNER_IMAGE,
                    "rm", "-f", f"/dst/{FRAGMENT}"], capture_output=True)
    subprocess.run(["docker", "restart", a.dnsmasq_container], capture_output=True)


def allow_command(a: argparse.Namespace, loadgen: Path, remove: bool = False) -> list[str]:
    """Puts the inbound sender on its recipient domain's allowlist, or takes it off.

    It is the testbed's own configuration, through the admin API an operator uses.
    The default pipeline stays as it is, and the allowlist skips the greylist only
    for mail whose SPF passes and aligns, which the sink's DNS fragment provides.
    """
    domain = a.smtp_in_to.rsplit("@", 1)[-1]
    return ["docker", "run", "--rm", "--label", a.label, "--network", a.network,
            "-e", f"LOAD_ADMIN_PASSWORD={a.admin_password}", "-v", f"{loadgen}:/loadgen:ro", RUNNER_IMAGE,
            "sh", "-c", 'exec /loadgen allow -pass "$LOAD_ADMIN_PASSWORD" "$@"', "loadgen",
            "-api", a.api, "-user", a.admin_user, "-domain", domain, "-sender", a.smtp_in_from, *(["-remove"] if remove else [])]


def save_evidence(raw: Path, services: list[str]) -> None:
    """Keeps what explains a run: service logs and the database's count of the mail."""
    for name in LOG_SERVICES:
        if name not in services:
            continue
        with open(raw / f"service-{name}.log", "w") as f:
            try:
                subprocess.run(["docker", "logs", "--tail", str(LOG_LINES), name], stdout=f, stderr=subprocess.STDOUT, timeout=120)
            except (OSError, subprocess.SubprocessError) as e:
                f.write(f"could not read the logs: {e}\n")
    if DB_CONTAINER in services:
        with open(raw / "db-report.log", "w") as f:
            try:
                subprocess.run(["docker", "exec", "-i", DB_CONTAINER, "sh", "-c", 'exec psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"'],
                               input=DB_REPORT, text=True, stdout=f, stderr=subprocess.STDOUT, timeout=120)
            except (OSError, subprocess.SubprocessError) as e:
                f.write(f"could not read the database: {e}\n")


def wait_for_file(path: Path, proc: subprocess.Popen, what: str, timeout: float = 120) -> None:
    deadline = time.monotonic() + timeout
    while not path.exists():
        if proc.poll() is not None or time.monotonic() > deadline:
            sys.exit(f"{what} did not start (exit {proc.poll()})")
        time.sleep(0.2)


def main(argv: list[str]) -> int:
    a = parse_args(argv)
    out = Path(a.out).resolve()
    raw = out / "raw"
    raw.mkdir(parents=True, exist_ok=True)
    for stale in ("smtp_in.json", "smtp_in_greylist.json", "smtp_out.json", "smtp_delivery.json", "sink.ready", "imap.json", "api-k6.json", "k6.log"):
        (raw / stale).unlink(missing_ok=True)
    resolv = raw / "resolv.conf"
    resolv.write_text(f"nameserver {a.dns}\n")
    loadgen = build_loadgen(out / "bin")
    uid = f"{os.getuid()}:{os.getgid()}"
    jobs = build_jobs(a, loadgen, raw, resolv, uid, uuid.uuid4().hex)
    if not jobs:
        print("every target is 0: nothing to run", file=sys.stderr)
        return 2

    started = dt.datetime.now(dt.timezone.utc)
    services = [n for n in network_containers(a.network) if not n.startswith(a.name_prefix)]
    stats = Stats(a.stats_interval, a.name_prefix, services)
    stats.start()
    procs: dict[str, subprocess.Popen] = {}
    logs = {}
    measuring_delivery = "smtp_delivery" in jobs
    # The fragment is also the inbound sender's SPF record.
    needs_dns = measuring_delivery or "smtp_in" in jobs or "smtp_in_greylist" in jobs
    allowing = "smtp_in" in jobs
    try:
        if needs_dns:
            publish_sink_dns(a)
            wait_for_sink_dns(a)
        if allowing:
            # Otherwise smtp_in measures the greylist: every message is deferred.
            run(allow_command(a, loadgen))
        # The sink first, so every load starts together once it is listening.
        for kind in sorted(jobs, key=lambda k: k != "smtp_delivery"):
            logs[kind] = open(raw / f"{kind}.log", "w")
            procs[kind] = subprocess.Popen(jobs[kind], stdout=logs[kind], stderr=subprocess.STDOUT)
            if kind == "smtp_delivery":
                # Mail for an MX that is not listening yet would be deferred, and
                # the run would measure the retry schedule.
                wait_for_file(raw / "sink.ready", procs[kind], "the delivery sink")
        # Generous: each load drains what is in flight after its duration, and the
        # sink then waits for the queue on top of that.
        deadline = time.monotonic() + a.duration + GENERATOR_DRAIN
        for kind, p in procs.items():
            if kind == "smtp_delivery":
                continue
            try:
                p.wait(timeout=max(1, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                p.kill()
        if measuring_delivery:
            try:
                # The sink stops itself: at its own limit, or once all has arrived.
                procs["smtp_delivery"].wait(timeout=max(1, deadline + a.delivery_drain + 60 - time.monotonic()))
            except subprocess.TimeoutExpired:
                procs["smtp_delivery"].kill()
    finally:
        save_evidence(raw, services)
        if allowing:
            subprocess.run(allow_command(a, loadgen, remove=True), capture_output=True)
        if needs_dns:
            remove_sink_dns(a)
        for p in procs.values():
            if p.poll() is None:
                p.kill()
        for kind in jobs:
            subprocess.run(["docker", "rm", "-f", f"{a.name_prefix}-{kind.replace('_', '-')}"], capture_output=True)
        for f in logs.values():
            f.close()
        stats.stop.set()
        stats.join(timeout=a.stats_interval + 60)

    loads: dict[str, dict] = {}
    for kind in jobs:
        try:
            if kind == "api":
                loads[kind] = k6_result(raw / "api-k6.json", a.api_rate, a.duration)
            else:
                loads[kind] = json.loads((raw / f"{kind}.json").read_text())
        except (OSError, ValueError, KeyError) as e:
            tail = (raw / f"{kind}.log").read_text()[-2000:]
            loads[kind] = {"kind": kind, "failed_to_run": f"{e}", "log_tail": tail, "exit_code": procs[kind].returncode}
        loads[kind]["met_target"] = met(loads[kind])

    results = {
        "schema": SCHEMA,
        "tested": {"commit": git("rev-parse", "HEAD"), "describe": git("describe", "--tags", "--always", "--dirty")},
        "started_at": started.isoformat(timespec="seconds"),
        "duration_s": a.duration,
        "targets": {"smtp_in_msg_s": a.smtp_in_rate, "smtp_in_greylist_msg_s": a.smtp_in_greylist_rate, "smtp_out_msg_s": a.smtp_out_rate,
                    "imap_connections": a.imap_conns, "api_req_s": a.api_rate},
        "generators": {"loadgen": "tests/load/loadgen", "k6": K6_IMAGE, "runner": RUNNER_IMAGE},
        "host": {"machine": platform.machine(), "cpus": os.cpu_count(), "mem_mib": host_mem_mib()},
        "loads": loads,
        "resources": stats.summary(),
    }
    (out / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    text = summary(results)
    (out / "results.txt").write_text(text)
    print(text, end="")
    print(f"\nresults: {out / 'results.json'}")
    return 0 if all(l.get("met_target") for l in loads.values()) else 1


def met(load: dict) -> bool:
    """A load met its target when it reached 95% of it with no errors.

    For smtp_delivery, an error is a message accepted and never delivered.
    """
    if "failed_to_run" in load:
        return False
    return load.get("errors", 1) == 0 and load.get("achieved_rate", 0) >= 0.95 * load.get("target", 0)


def host_mem_mib() -> int | None:
    try:
        with open("/proc/meminfo") as f:
            for line in f:
                if line.startswith("MemTotal:"):
                    return int(line.split()[1]) // 1024
    except OSError:
        pass
    return None


def summary(r: dict) -> str:
    lines = [f"REST Mail load run, {r['tested']['describe'] or r['tested']['commit']} at {r['started_at']}, {r['duration_s']}s each", ""]
    for kind, l in r["loads"].items():
        if "failed_to_run" in l:
            lines.append(f"{kind:16} did not run: {l['failed_to_run']}")
            continue
        lat = l.get("latency_ms", {})
        unit = l.get("target_unit", "")
        verdict = "met" if l["met_target"] else "MISSED"
        lines.append(f"{kind:16} target {l['target']:g} {unit:11} achieved {l['achieved_rate']:g}  ok {l['ok']}  errors {l['errors']}  "
                     f"p50 {lat.get('p50', 0):g}ms p95 {lat.get('p95', 0):g}ms p99 {lat.get('p99', 0):g}ms  {verdict}")
        for msg, n in sorted(l.get("error_kinds", {}).items(), key=lambda kv: -kv[1])[:3]:
            lines.append(f"{'':16}   {n} x {msg}")
        extra = l.get("extra", {})
        if "delivered" in extra:
            lines.append(f"{'':16}   delivered {extra['delivered']} of {extra['accepted']} accepted, "
                         f"{extra['duplicates']} duplicates, {extra['first_to_last_s']:g}s first to last")
    res = r["resources"]
    if res:
        lines += ["", "busiest services (max CPU %, max memory MiB):"]
        top = sorted(res.items(), key=lambda kv: -(kv[1]["cpu_pct"]["max"] or 0))[:8]
        width = max(len(n) for n, _ in top)
        for name, s in top:
            lines.append(f"  {name:{width}}  cpu {s['cpu_pct']['max'] or 0:7.1f}%  mem {s['mem_mib']['max'] or 0:8.1f} MiB")
    return "\n".join(lines) + "\n"


if __name__ == "__main__":
    if not shutil.which("docker"):
        sys.exit("docker is required")
    sys.exit(main(sys.argv[1:]))
