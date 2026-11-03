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
Reports land in .cache/contract/: faults.md and faults-<name>-<phase>.xml.
Limit phases send their own login probes, not Vertrag examples. Known gaps
remain until live proof is reviewed; a newly caught gap deliberately fails.

Usage:
  faults.py tags                       every build tag in the catalogue, comma separated
  faults.py run --api-container NAME   the fault run
"""

import argparse
import http.client
import json
import os
import shutil
import subprocess
import sys
import xml.etree.ElementTree as ET
from pathlib import Path
from urllib.parse import urlsplit

import yaml

ROOT = Path(__file__).resolve().parents[2]
CATALOGUE = ROOT / "tests/contract/faults.yml"
OUT = ROOT / ".cache/contract"
ACTIVE = "fault injection active"

# Each phase a fault can name: the run.sh phase that runs it, and its report.
PHASES = {"examples": ("gate", "examples.xml"), "body_cap": None, "auth_throttle": None}
LIMIT_PHASES = {"body_cap", "auth_throttle"}
LOGIN = "/api/v1/auth/login"
BODY_BYTES = 32 << 20
THROTTLE_ATTEMPTS = 200


def load(path=CATALOGUE):
    faults = yaml.safe_load(Path(path).read_text())["faults"]
    for fault in faults:
        if fault["phase"] not in PHASES:
            raise ValueError(f"{fault['name']}: no fault run for phase {fault['phase']}")
        if fault["phase"] in LIMIT_PHASES and (fault["operation"], fault["path"]) != ("login", LOGIN):
            raise ValueError(f"{fault['name']}: limit probes only exercise POST {LOGIN}")
    return faults


def all_tags(faults):
    tags = []
    for fault in faults:
        for tag in fault["build_tags"].split(","):
            if tag not in tags:
                tags.append(tag)
    return ",".join(tags)


def transactions_of(report, path):
    """(sent, failed) for a route in a JUnit report: how many of its transactions
    the report holds, and for each that failed its name and Vertrag's reason.
    Vertrag names a transaction "<path> > <summary> > <status> > <media type>"."""
    sent, failed = 0, []
    for case in ET.parse(report).getroot().iter("testcase"):
        name = case.get("name", "")
        if name.split(" > ", 1)[0] != path:
            continue
        sent += 1
        problem = case.find("failure")
        if problem is None:
            problem = case.find("error")
        if problem is not None:
            reason = problem.get("message")
            failed.append(f"{name}: {reason}" if reason else name)
    return sent, failed


def limit_transactions_of(report, path, phase):
    cases = list(ET.parse(report).getroot().iter("testcase"))
    if len(cases) != 1 or cases[0].get("name") != f"{path} > {phase}" or cases[0].get("classname") != "login":
        raise ValueError("no unique login limit probe for the operation and reason")
    case = cases[0]
    if case.find("error") is not None or case.find("skipped") is not None:
        raise ValueError("the limit probe errored or was skipped: " + ET.tostring(case, encoding="unicode"))
    failures = case.findall("failure")
    if any(failure.get("type") != phase for failure in failures):
        raise ValueError("the limit probe failed for a different reason")
    return 1, [failure.get("message", phase) for failure in failures]


def judge(report, path, faulted, known_gap=None, phase="examples", expected_failure=None):
    """(passed, why) for one run's report.

    A faulted run passes when the report fails the route; a baseline run passes
    when the route was sent and nothing failed. With expected_failure, a failure
    of the route only counts if one of them gives that reason, so a run that went
    red for something else proves nothing. A fault with a known gap is one the
    run cannot catch yet: it passes uncaught, and fails once it is caught, so the
    gap's entry is removed when the gap closes.
    """
    if not Path(report).exists():
        return False, "the phase did not run: no report"
    if phase in LIMIT_PHASES:
        try:
            sent, failed = limit_transactions_of(report, path, phase)
        except (ValueError, ET.ParseError) as error:
            return False, f"invalid limit proof: {error}"
    else:
        sent, failed = transactions_of(report, path)
    if faulted:
        if known_gap:
            if failed:
                return False, f"now caught, so remove known_gap {known_gap}: " + "; ".join(failed)
            return True, f"known gap: {known_gap}"
        if failed and expected_failure and not any(expected_failure in reason for reason in failed):
            return False, f"not caught: wanted the failure {expected_failure!r}, got " + "; ".join(failed)
        if failed:
            return True, "caught: " + "; ".join(failed)
        return False, f"not caught: no failed transaction of {path}"
    if failed:
        return False, "red without a fault: " + "; ".join(failed)
    if not sent:
        return False, f"the phase sent no transaction of {path}"
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


def login_response(endpoint, payload, body_bytes=None):
    target = urlsplit(endpoint)
    if target.scheme not in {"http", "https"} or not target.hostname or target.path not in {"", "/"} or target.query or target.fragment:
        raise ValueError("limit probes require an HTTP(S) API origin")
    connection_type = http.client.HTTPSConnection if target.scheme == "https" else http.client.HTTPConnection
    connection = connection_type(target.hostname, target.port, timeout=15)
    body = json.dumps(payload).encode()
    length = body_bytes if body_bytes is not None else len(body)

    def padded_body():
        yield body
        remaining = length - len(body)
        while remaining:
            chunk = b" " * min(remaining, 65536)
            yield chunk
            remaining -= len(chunk)

    try:
        try:
            connection.request("POST", LOGIN, body=padded_body(), headers={
                "Content-Type": "application/json",
                "Content-Length": str(length),
                "Connection": "close",
            })
        except (BrokenPipeError, ConnectionResetError):
            pass
        response = connection.getresponse()
        status, retry = response.status, response.getheader("Retry-After", "")
        answer = json.loads(response.read(65536))
        return status, answer["error"]["code"], retry
    finally:
        connection.close()


def probe_limit(phase, endpoint):
    if phase == "body_cap":
        status, code, _ = login_response(endpoint, {}, body_bytes=BODY_BYTES)
        evidence = f"POST {LOGIN}, Content-Length {BODY_BYTES}, space-padded JSON {{}}: {status} {code}"
        if (status, code) == (413, "request_too_large"):
            return False, evidence
        if (status, code) == (422, "validation_failed"):
            return True, evidence + "; wanted 413 request_too_large before validation"
        raise ValueError(evidence + "; not evidence of a missing body cap")
    if phase != "auth_throttle":
        raise ValueError(f"no limit probe for {phase}")
    for attempt in range(1, THROTTLE_ATTEMPTS + 1):
        status, code, retry = login_response(endpoint, {
            "email": "contract-fault-unknown@restmail.test", "password": "not the password",
        })
        if (status, code) == (429, "rate_limited") and attempt > 1 and retry.isascii() and retry.isdecimal() and int(retry) > 0:
            return False, f"POST {LOGIN}: {attempt - 1} refused logins, then 429 rate_limited, Retry-After {retry}"
        if (status, code) != (401, "unauthorized"):
            raise ValueError(f"POST {LOGIN}, attempt {attempt}: {status} {code}, Retry-After {retry!r}; expected a refused login then a valid throttle")
    return True, f"POST {LOGIN}: all {THROTTLE_ATTEMPTS} requests returned 401 unauthorized; wanted 429 rate_limited"


def run_limits(phase, operation, keep_as):
    report = OUT / keep_as
    report.parent.mkdir(parents=True, exist_ok=True)
    suite = ET.Element("testsuite", name=phase)
    case = ET.SubElement(suite, "testcase", classname=operation, name=f"{LOGIN} > {phase}")
    try:
        endpoint = os.environ.get("CONTRACT_ENDPOINT") or yaml.safe_load((ROOT / "vertrag.yml").read_text())["endpoint"]
        missing, evidence = probe_limit(phase, endpoint)
        if missing:
            ET.SubElement(case, "failure", type=phase, message=evidence)
        ET.SubElement(case, "system-out").text = evidence
    except (OSError, http.client.HTTPException, ValueError, KeyError, TypeError) as error:
        ET.SubElement(case, "error", message=str(error)).text = str(error)
    ET.ElementTree(suite).write(report, encoding="utf-8", xml_declaration=True)
    return report


def run_phase(phase, operation, keep_as):
    if phase in LIMIT_PHASES:
        return run_limits(phase, operation, keep_as)
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

    baselines = {(f["phase"], f["operation"], f["path"]) for f in faults}
    for phase, operation, path in sorted(baselines):
        print(f"::group::baseline: {phase} of {operation} on the normal API")
        if phase in LIMIT_PHASES:
            restart_api("")
        report = run_phase(phase, operation, f"faults-baseline-{operation}-{phase}.xml")
        passed, why = judge(report, path, faulted=False, phase=phase)
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
                passed, why = judge(
                    report, fault["path"], faulted=True, known_gap=fault.get("known_gap"), phase=phase,
                    expected_failure=fault.get("expected_failure"),
                )
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
    # Keep these lines in order with the output of the commands this runs.
    sys.stdout.reconfigure(line_buffering=True)
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
