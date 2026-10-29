"""Require the contract run to catch every fault in tests/contract/faults.yml (#654).

Against a running E2E topology (`chore contract:faults`, nightly in
.github/workflows/contract-faults.yml):

1. Each faulted operation's phase runs, narrowed to it, on the normal API and
   must pass there; otherwise a red run proves nothing.
2. For each fault, the API is rebuilt with the fault's build tags, its log must
   show the fault active, and the fault's phase, narrowed to its operation,
   must fail that operation.
3. The normal API is restored.

Any fault that is not caught, or a baseline that is not green, fails the run.
Reports land in .cache/contract/: faults.md, and each run's examples report
as faults-<name>-examples.xml.

Usage:
  faults.py tags                       every build tag in the catalogue, comma separated
  faults.py run --api-container NAME   the fault run
"""

import argparse
import os
import shutil
import subprocess
import sys
import xml.etree.ElementTree as ET
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]
CATALOGUE = ROOT / "tests/contract/faults.yml"
OUT = ROOT / ".cache/contract"
ACTIVE = "fault injection active"

# Each phase a fault can name: the run.sh phase that runs it, and its report.
PHASES = {"examples": ("gate", "examples.xml")}


def load(path=CATALOGUE):
    faults = yaml.safe_load(Path(path).read_text())["faults"]
    for fault in faults:
        if fault["phase"] not in PHASES:
            raise ValueError(f"{fault['name']}: no fault run for phase {fault['phase']}")
    return faults


def all_tags(faults):
    tags = []
    for fault in faults:
        for tag in fault["build_tags"].split(","):
            if tag not in tags:
                tags.append(tag)
    return ",".join(tags)


def failures_for(report, operation):
    """Names of the failed transactions in a JUnit report that are operation's."""
    failed = []
    for case in ET.parse(report).getroot().iter("testcase"):
        verdict = case.find("failure")
        if verdict is None:
            verdict = case.find("error")
        if verdict is None:
            continue
        text = " ".join(
            [case.get("name", ""), case.get("classname", ""), verdict.get("message", ""), verdict.text or ""]
        )
        if operation in text:
            failed.append(case.get("name", ""))
    return failed


def judge(report, operation, faulted):
    """(passed, why) for one run's report.

    A faulted run passes when the report fails the operation; a baseline run
    passes when it does not.
    """
    if not Path(report).exists():
        return False, "the phase did not run: no report"
    failed = failures_for(report, operation)
    if faulted:
        if failed:
            return True, "caught: " + "; ".join(failed)
        return False, f"not caught: no failed transaction of {operation}"
    if failed:
        return False, "red without a fault: " + "; ".join(failed)
    return True, "green"


def chore(*args):
    subprocess.run([os.environ.get("CHORE_BIN", "chore"), *args], cwd=ROOT, check=True)


def restart_api(tags):
    # The E2E topology runs the API with internal mTLS on (e2e:up); keep it so.
    chore("api:up", f"RESTMAIL_API_BUILD_TAGS={tags}", "RESTMAIL_INTERNAL_MTLS=true")
    chore("health:wait")


def fault_active(container, name):
    logs = subprocess.run(
        ["docker", "logs", container], capture_output=True, text=True, check=False
    )
    return any(ACTIVE in line and name in line for line in (logs.stdout + logs.stderr).splitlines())


def run_phase(phase, operation, keep_as):
    runner, report = PHASES[phase]
    code = subprocess.run(
        ["tests/contract/run.sh", runner, "--operation-id", operation], cwd=ROOT, check=False
    ).returncode
    print(f"run.sh {runner} --operation-id {operation} exited {code}")
    source = OUT / report
    kept = OUT / keep_as
    kept.unlink(missing_ok=True)
    if source.exists():
        shutil.copyfile(source, kept)
    return kept


def run(container):
    faults = load()
    rows = ["| Fault | Operation | Phase | Verdict |", "|---|---|---|---|"]
    ok = True

    baselines = {(f["phase"], f["operation"]) for f in faults}
    for phase, operation in sorted(baselines):
        print(f"::group::baseline: {phase} of {operation} on the normal API")
        report = run_phase(phase, operation, f"faults-baseline-{operation}-{phase}.xml")
        passed, why = judge(report, operation, faulted=False)
        print("::endgroup::")
        rows.append(f"| (none) | {operation} | {phase} | {why} |")
        ok = ok and passed
    if not ok:
        print("::error::a faulted operation is not green on the normal API, so no fault can be judged")
    else:
        for fault in faults:
            name, operation, phase = fault["name"], fault["operation"], fault["phase"]
            print(f"::group::fault {name}: {fault['breaks']}")
            restart_api(fault["build_tags"])
            if not fault_active(container, name):
                passed, why = False, f"the API did not log '{ACTIVE}' for {name}: the switch did not take"
            else:
                report = run_phase(phase, operation, f"faults-{name}-{phase}.xml")
                passed, why = judge(report, operation, faulted=True)
            print("::endgroup::")
            if not passed:
                print(f"::error::fault {name}: {why}")
            rows.append(f"| {name} | {operation} | {phase} | {why} |")
            ok = ok and passed
        restart_api("")

    table = "\n".join(rows) + "\n"
    OUT.mkdir(parents=True, exist_ok=True)
    (OUT / "faults.md").write_text("# Contract faults\n\n" + table)
    print(table, end="")
    return 0 if ok else 1


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("tags")
    running = sub.add_parser("run")
    running.add_argument("--api-container", required=True)
    args = parser.parse_args(argv)
    if args.command == "tags":
        print(all_tags(load()))
        return 0
    return run(args.api_container)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
