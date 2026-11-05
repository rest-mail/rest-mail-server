#!/usr/bin/env python3
"""Check that internal/api's tests do not spend their shard's time on hashing.

A time budget would flake on a loaded machine, so this counts work instead.

By default it needs no database and takes under a few minutes:

- the Go tests named TestShardWork_ must run and pass in each package: a binary
  built without go test hashes at the production cost, while the API fixtures hash
  at the minimum cost and the described generators are built once;
- a test under internal/api may not call bcrypt directly at any other cost;
- only test files may call auth.UseMinimumHashCostInTests.

With --shard it also runs one CI shard the way CI does, under -race with coverage,
and fails when bcrypt holds more than --max-bcrypt-share of its CPU samples. Elapsed
time and load are printed for the record and decide nothing: go test's own 20 minute
timeout is the only time limit.
"""

import argparse
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
WORK_PACKAGES = {
    "./internal/auth": [
        "TestShardWork_ABinaryNotBuiltByGoTestHashesAtTheProductionCost",
        "TestShardWork_TheCostIsTheProductionOneUntilATestLowersIt",
    ],
    "./internal/api": [
        "TestShardWork_FixturesHashAtTheMinimumCost",
        "TestShardWork_TheGapGeneratorsAreBuiltOnce",
    ],
    "./internal/api/handlers": [
        "TestShardWork_FixturesHashAtTheMinimumCost",
        "TestShardWork_EnrollingHashesEveryRecoveryCodeAtTheMinimumCost",
    ],
}
SEAM = "UseMinimumHashCostInTests"
DIRECT_BCRYPT = re.compile(r"bcrypt\.GenerateFromPassword\((?P<args>[^\n]*)\)")


def static_findings(root):
    """What the source says about hash cost, as one message per problem."""
    findings = []
    for path in sorted((root / "internal" / "api").rglob("*_test.go")):
        for line_number, line in enumerate(path.read_text().splitlines(), 1):
            match = DIRECT_BCRYPT.search(line)
            if match and not match.group("args").rstrip().endswith("bcrypt.MinCost"):
                relative = path.relative_to(root)
                findings.append(f"{relative}:{line_number}: bcrypt at a cost other than bcrypt.MinCost")
    for directory in ("cmd", "internal", "tools", "tests"):
        for path in sorted((root / directory).rglob("*.go")):
            if path.name.endswith("_test.go") or "testdata" in path.parts or path.name == "hash_cost.go":
                continue
            if SEAM in path.read_text():
                findings.append(f"{path.relative_to(root)}: {SEAM} outside a test file")
    return findings


def missing_tests(output, wanted):
    """The wanted test names go test -v did not report as passing."""
    return [name for name in wanted if f"--- PASS: {name} " not in output]


def run_work_tests(root):
    failures = []
    for package, wanted in WORK_PACKAGES.items():
        command = ["go", "test", "-count=1", "-v", "-run", "^TestShardWork_", package]
        result = subprocess.run(command, cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
        missing = missing_tests(result.stdout, wanted)
        if result.returncode != 0 or missing:
            print(result.stdout)
            failures.append(f"{package}: exit {result.returncode}, not passed: {', '.join(missing) or 'none'}")
    return failures


def shard_names(output, shard, shards):
    """The tests CI's sorted round-robin gives this shard."""
    if not 1 <= shard <= shards:
        raise ValueError("shard must be between 1 and shards")
    names = sorted(line for line in output.splitlines() if re.match(r"^(Test|Example|Fuzz)", line))
    selected = names[shard - 1 :: shards]
    if not selected:
        raise ValueError(f"shard {shard}/{shards} has no tests")
    return selected


def bcrypt_share(top_output):
    """bcrypt's share of the CPU samples in go tool pprof -top -cum output."""
    total = re.search(r"Total samples = ([0-9.]+)(ms|s|mins|hrs)", top_output)
    if not total:
        raise ValueError("no sample total in the profile report")
    match = re.search(r"([0-9.]+)(ms|s|mins|hrs)\s+[0-9.]+%\s+golang\.org/x/crypto/bcrypt\.bcrypt$", top_output, re.MULTILINE)
    if not match:
        return 0.0
    units = {"ms": 0.001, "s": 1, "mins": 60, "hrs": 3600}
    return float(match.group(1)) * units[match.group(2)] / (float(total.group(1)) * units[total.group(2)])


def load_summary():
    return f"cpus={os.cpu_count()} load1/5/15={os.getloadavg()}"


def run_shard(root, shard, shards, executor, max_share, profile_dir=None):
    listing = subprocess.run(
        ["go", "test", "-race", "-list", ".*", "./internal/api/"], cwd=root, text=True, stdout=subprocess.PIPE, check=True
    )
    selected = shard_names(listing.stdout, shard, shards)
    with tempfile.TemporaryDirectory(prefix="restmail-api-shard-") as scratch:
        directory = Path(profile_dir or scratch).resolve()
        directory.mkdir(parents=True, exist_ok=True)
        binary, profile = directory / f"shard-{shard}.test", directory / f"shard-{shard}.cpu"
        command = [
            "go", "test", "-race", "-count=1", "-timeout=20m", "-v",
            f"-coverprofile={scratch}/coverage.out", f"-cpuprofile={profile}", "-o", str(binary),
            "-exec", executor,
            "-run", "^(" + "|".join(re.escape(name) for name in selected) + ")$",
            "./internal/api/",
        ]
        print(f"API shard {shard}/{shards}: {len(selected)} tests; {load_summary()}", flush=True)
        started = os.times().elapsed
        result = subprocess.run(command, cwd=root, check=False)
        elapsed = os.times().elapsed - started
        top = subprocess.run(
            ["go", "tool", "pprof", "-top", "-cum", "-nodecount=4000", str(binary), str(profile)],
            cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=False,
        )
        share = bcrypt_share(top.stdout)
        passed = result.returncode == 0 and share <= max_share
        print(
            f"API shard {shard}/{shards}: {'PASS' if passed else 'FAIL'} exit={result.returncode} "
            f"bcrypt={share:.2%} of CPU samples (limit {max_share:.2%}); "
            f"elapsed={elapsed:.0f}s, not judged; {load_summary()}",
            flush=True,
        )
        return 0 if passed else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--shard", type=int, help="also run this CI shard, 1 to --shards; needs DB_HOST and --exec")
    parser.add_argument("--shards", type=int, default=4)
    parser.add_argument("--exec", dest="executor", help="a build of tools/testdb-exec")
    parser.add_argument("--max-bcrypt-share", type=float, default=0.05)
    parser.add_argument("--profile-dir", type=Path)
    args = parser.parse_args()
    if args.shard is not None and not (os.environ.get("DB_HOST") and args.executor):
        parser.error("--shard needs DB_HOST naming a disposable PostgreSQL server and --exec: no optional database skips")

    failures = static_findings(ROOT) + run_work_tests(ROOT)
    for failure in failures:
        print(f"API shard work: {failure}", file=sys.stderr)
    if failures:
        return 1
    print("API shard work: production hash cost proven; fixtures at the minimum cost; generators built once")
    if args.shard is None:
        return 0
    try:
        return run_shard(ROOT, args.shard, args.shards, args.executor, args.max_bcrypt_share, args.profile_dir)
    except (ValueError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"API shard check failed: {error}\n")


if __name__ == "__main__":
    raise SystemExit(main())
