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
Limit phases send their own login probes, not Vertrag examples. The secret_scan
phase runs the same narrowed gate and judges its recording with secret_scan.py,
because a secret in a free string fits the schema and passes the examples phase.
The browser_session phase runs the same narrowed gate, whose first step is the
browser-session checker (tests/contract/browser_sessions), and judges only that
checker: its output line and its receipt (browser-sessions.json).
The pairs phase needs no API: a fault with a patch removes a real guard in a
throwaway tree of HEAD, and the example-pair test CI requires (chore
contract:pairs) must fail its witness pair there, through the real router.
Known gaps remain until live proof is reviewed; a newly caught gap deliberately
fails.

Usage:
  faults.py tags                       every build tag in the catalogue, comma separated
  faults.py run --api-container NAME   the fault run
  faults.py pairs                      only the pairs phase's faults; no topology
"""

import argparse
import contextlib
import http.client
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import xml.etree.ElementTree as ET
from pathlib import Path
from urllib.parse import urlsplit

import yaml

import secret_scan

ROOT = Path(__file__).resolve().parents[2]
CATALOGUE = ROOT / "tests/contract/faults.yml"
OUT = ROOT / ".cache/contract"
ACTIVE = "fault injection active"

# Each phase a fault can name: the run.sh phase that runs it, and its report.
PHASES = {
    "examples": ("gate", "examples.xml"), "body_cap": None, "auth_throttle": None,
    "secret_scan": ("gate", "examples.har"), "browser_session": ("gate", "browser-sessions.json"),
    "pairs": None,
}
LIMIT_PHASES = {"body_cap", "auth_throttle"}
SCAN_PHASE = "secret_scan"
SESSION_PHASE = "browser_session"
PAIRS_PHASE = "pairs"
# The phases whose report is the one case this runner writes for the route.
OWN_CASE_PHASES = {SCAN_PHASE, SESSION_PHASE, PAIRS_PHASE}
# What the browser-session checker prints, and the requests it sends a cookie
# session's unsafe operation lacking a matching CSRF token (browser_sessions/main.go).
SESSION_CHECKER = "browser session contract: "
CSRF_VARIANTS = ("cookie-without-csrf", "cookie-mismatched-csrf", "cookie-without-csrf-cookie")
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
        if (fault["phase"] == PAIRS_PHASE) != ("patch" in fault):
            raise ValueError(f"{fault['name']}: a patch is applied and judged only by the pairs phase")
        if "patch" in fault:
            if "build_tags" in fault:
                raise ValueError(f"{fault['name']}: a patch or build tags, not both")
            # A compile error or a panic also fails the test, so the reason is required.
            for field in ("test", "subtest", "expected_failure"):
                if not fault.get(field):
                    raise ValueError(f"{fault['name']}: a patch fault needs {field}")
    return faults


def all_tags(faults):
    tags = []
    for fault in faults:
        for tag in fault.get("build_tags", "").split(","):
            if not tag:
                continue
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


def probe_transactions_of(report, path, phase):
    """(1, failures) for the one case a limit probe or the scan writes. A case
    that errored or was skipped could not judge, so it is no verdict at all."""
    cases = list(ET.parse(report).getroot().iter("testcase"))
    if phase in OWN_CASE_PHASES:
        if len(cases) != 1 or cases[0].get("name") != f"{path} > {phase}":
            raise ValueError(f"no unique {phase} verdict for the route")
    elif len(cases) != 1 or cases[0].get("name") != f"{path} > {phase}" or cases[0].get("classname") != "login":
        raise ValueError("no unique login limit probe for the operation and reason")
    case = cases[0]
    if case.find("error") is not None or case.find("skipped") is not None:
        raise ValueError("the probe errored or was skipped: " + ET.tostring(case, encoding="unicode"))
    failures = case.findall("failure")
    if any(failure.get("type") != phase for failure in failures):
        raise ValueError("the probe failed for a different reason")
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
    if phase in LIMIT_PHASES or phase in OWN_CASE_PHASES:
        try:
            sent, failed = probe_transactions_of(report, path, phase)
        except (ValueError, ET.ParseError) as error:
            kind = {SCAN_PHASE: "scan", SESSION_PHASE: "session", PAIRS_PHASE: "pairs"}.get(phase, "limit")
            return False, f"invalid {kind} proof: {error}"
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


def answers_route(finding, path):
    """Whether a scan finding names this route as the one answering. It starts
    "<method> <url> [<transaction>] answers ...", or "the audit log's ..."."""
    parts = finding.split(" ", 2)
    return len(parts) == 3 and urlsplit(parts[1]).path == path


def sent_secrets(har, path):
    """The secrets the recording shows sent to this route. Without one the scan
    has nothing to look for there, and a clean scan proves nothing."""
    found = []
    for entry in har["log"]["entries"]:
        if secret_scan.path_of(entry) == path:
            post = entry.get("request", {}).get("postData") or {}
            found += secret_scan.json_secrets(secret_scan.body_text(post), secret_scan.SENT_FIELDS)
    return found


def scan_recording(har_path, operation, path, keep_as):
    """Judge one recording with the scanner, as a report the run can read: a
    failure per finding on this route. Any finding elsewhere, a missing recording
    or a route sent no secret is an error, so it neither catches nor clears."""
    report = OUT / keep_as
    report.parent.mkdir(parents=True, exist_ok=True)
    suite = ET.Element("testsuite", name=SCAN_PHASE)
    case = ET.SubElement(suite, "testcase", classname=operation, name=f"{path} > {SCAN_PHASE}")
    try:
        har = json.loads(Path(har_path).read_text())
        sent = sent_secrets(har, path)
        if not sent:
            raise ValueError(f"the recording shows no secret sent to {path}")
        findings = secret_scan.scan(har)
        elsewhere = [finding for finding in findings if not answers_route(finding, path)]
        if elsewhere:
            raise ValueError("a finding on another route: " + "; ".join(elsewhere))
        for finding in findings:
            ET.SubElement(case, "failure", type=SCAN_PHASE, message=finding)
        ET.SubElement(case, "system-out").text = (
            f"{len(har['log']['entries'])} recorded exchanges, {len(sent)} secrets sent to {path}, {len(findings)} findings"
        )
    except (OSError, ValueError, KeyError, TypeError) as error:
        ET.SubElement(case, "error", message=str(error)).text = str(error)
    ET.ElementTree(suite).write(report, encoding="utf-8", xml_declaration=True)
    return report


def session_verdict(output, receipt_path, operation, path, keep_as):
    """Judge the browser-session checker's run, as a report the run can read: a
    failure only when it failed this operation's CSRF refusal. A pass that never
    saw the operation refuse each request lacking CSRF, or a failure anywhere or
    for any other reason, is an error, so it neither catches nor clears."""
    report = OUT / keep_as
    report.parent.mkdir(parents=True, exist_ok=True)
    suite = ET.Element("testsuite", name=SESSION_PHASE)
    case = ET.SubElement(suite, "testcase", classname=operation, name=f"{path} > {SESSION_PHASE}")
    try:
        verdicts = [line[len(SESSION_CHECKER):] for line in output.splitlines() if line.startswith(SESSION_CHECKER)]
        if len(verdicts) != 1:
            raise ValueError(f"{len(verdicts)} browser-session checker verdicts in the output, wanted one")
        verdict = verdicts[0]
        receipt = json.loads(Path(receipt_path).read_text())
        checks = receipt["checks"]
        if receipt["passed"] is True:
            if not re.fullmatch(r"\d+ checks passed", verdict):
                raise ValueError("the receipt passed but the checker said: " + verdict)
            refused = sorted(c["principal"] for c in checks if c["operation"] == operation and c["status"] == 403)
            if refused != sorted(CSRF_VARIANTS):
                raise ValueError(f"the checker passed without refusing each request to {operation} lacking CSRF")
        else:
            failed = re.fullmatch(
                re.escape(operation) + r" \((" + "|".join(map(re.escape, CSRF_VARIANTS)) + r")\): "
                r"(expected 403 csrf_failed, got status (\d+) or a different error code)",
                verdict,
            )
            if not failed:
                raise ValueError("the checker failed for another reason: " + verdict)
            if not checks or checks[-1] != {"operation": operation, "principal": failed[1], "status": int(failed[3])}:
                raise ValueError("the receipt does not end with the check that failed")
            ET.SubElement(case, "failure", type=SESSION_PHASE, message=f"{operation} ({failed[1]}): {failed[2]}")
        ET.SubElement(case, "system-out").text = f"{len(checks)} browser-session checks: {verdict}"
    except (OSError, ValueError, KeyError, TypeError) as error:
        ET.SubElement(case, "error", message=str(error)).text = str(error)
    ET.ElementTree(suite).write(report, encoding="utf-8", xml_declaration=True)
    return report


@contextlib.contextmanager
def throwaway_tree(patch):
    """A tree of HEAD with the patch applied, gone afterwards. Never the running
    checkout: the topology's API rebuilds itself when that changes."""
    scratch = Path(tempfile.mkdtemp(prefix="contract-fault-"))
    tree = scratch / "tree"
    subprocess.run(["git", "worktree", "add", "--quiet", "--detach", str(tree), "HEAD"], cwd=ROOT, check=True)
    try:
        applied = subprocess.run(["git", "apply", "-"], input=patch, text=True, cwd=tree, capture_output=True, check=False)
        if applied.returncode:
            raise ValueError("the patch no longer applies: " + applied.stderr.strip())
        yield tree
    finally:
        subprocess.run(["git", "worktree", "remove", "--force", str(tree)], cwd=ROOT, check=False)
        shutil.rmtree(scratch, ignore_errors=True)


def subtest_outcome(events, test, subtest):
    """(outcome, output) of one subtest in `go test -json`'s stream. No outcome
    means it never ran, and the output is then what the build said, if anything."""
    name = f"{test}/{subtest}"
    outcome, output, built = None, [], []
    for line in events.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        if event.get("Action") == "build-output":
            built.append(event.get("Output", ""))
        if event.get("Test") != name:
            continue
        if event.get("Action") in {"pass", "fail", "skip"}:
            outcome = event["Action"]
        elif event.get("Action") == "output":
            output.append(event.get("Output", ""))
    return outcome, " ".join("".join(output if outcome else built).split())


def run_pair(fault, keep_as, patched):
    """Run the fault's witness pair, on HEAD or with its patch, as a report the
    run can read: a failure when the pair failed. A pair that never ran or was
    skipped is an error, so it neither catches nor clears."""
    operation, path, test, subtest = fault["operation"], fault["path"], fault["test"], fault["subtest"]
    report = OUT / keep_as
    report.parent.mkdir(parents=True, exist_ok=True)
    suite = ET.Element("testsuite", name=PAIRS_PHASE)
    case = ET.SubElement(suite, "testcase", classname=operation, name=f"{path} > {PAIRS_PHASE}")
    try:
        binary = os.environ.get("RESTMAIL_VERTRAG_BINARY")
        if not binary:
            raise ValueError("RESTMAIL_VERTRAG_BINARY names no Vertrag, so the pair would skip")
        environment = {**os.environ, "RESTMAIL_VERTRAG_BINARY": str((ROOT / binary).resolve())}
        selected = "/".join("^" + part + "$" for part in [test, *subtest.split("/")])
        with throwaway_tree(fault["patch"]) if patched else contextlib.nullcontext(ROOT) as tree:
            # -trimpath lets the trees share the build cache, so only what a patch touches recompiles.
            done = subprocess.run(
                ["go", "test", "-json", "-trimpath", "-count=1", "-timeout", "10m", "-run", selected, "./internal/api"],
                cwd=tree, env=environment, capture_output=True, text=True, check=False,
            )
        outcome, output = subtest_outcome(done.stdout, test, subtest)
        if outcome not in {"pass", "fail"}:
            raise ValueError(f"{test}/{subtest} did not run ({outcome}), go test exit {done.returncode}: {output[:500]} {done.stderr[-500:]}")
        if outcome == "fail":
            expected = fault["expected_failure"]
            ET.SubElement(case, "failure", type=PAIRS_PHASE, message=f"{subtest}: " + (expected if expected in output else output[:2000]))
        ET.SubElement(case, "system-out").text = f"{test}/{subtest}: {outcome}"
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        ET.SubElement(case, "error", message=str(error)).text = str(error)
    ET.ElementTree(suite).write(report, encoding="utf-8", xml_declaration=True)
    return report


def run_phase(phase, operation, keep_as, path=None):
    if phase in LIMIT_PHASES:
        return run_limits(phase, operation, keep_as)
    runner, report = PHASES[phase]
    if phase == SESSION_PHASE:
        # The checker's verdict is its output line, so keep the output as well.
        done = subprocess.run(
            ["tests/contract/run.sh", runner, "--operation-id", operation], cwd=ROOT, check=False,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        )
        print(done.stdout, end="")
        print(f"run.sh {runner} --operation-id {operation} exited {done.returncode}")
        return session_verdict(done.stdout, OUT / report, operation, path, keep_as)
    code = subprocess.run(
        ["tests/contract/run.sh", runner, "--operation-id", operation], cwd=ROOT, check=False
    ).returncode
    print(f"run.sh {runner} --operation-id {operation} exited {code}")
    source = OUT / report
    if phase == SCAN_PHASE:
        return scan_recording(source, operation, path, keep_as)
    kept = OUT / keep_as
    kept.unlink(missing_ok=True)
    if source.exists():
        shutil.copyfile(source, kept)
    return kept


def run(container, phases=None):
    faults = [fault for fault in load() if phases is None or fault["phase"] in phases]
    rows = ["| Fault | Operation | Phase | Verdict |", "|---|---|---|---|"]
    ok = True

    # A patch's witness is one subtest, so each of its witnesses has its own baseline.
    baselines = {(f["phase"], f["operation"], f["path"], f.get("subtest", "")): f for f in faults}
    for (phase, operation, path, subtest), fault in sorted(baselines.items(), key=lambda item: item[0]):
        print(f"::group::baseline: {phase} of {operation} on the normal API")
        if phase in LIMIT_PHASES:
            restart_api("")
        if phase == PAIRS_PHASE:
            report = run_pair(fault, f"faults-baseline-{operation}-{subtest.replace('/', '-')}-{phase}.xml", patched=False)
        else:
            report = run_phase(phase, operation, f"faults-baseline-{operation}-{phase}.xml", path)
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
            if phase == PAIRS_PHASE:
                report = run_pair(fault, f"faults-{name}-{phase}.xml", patched=True)
            else:
                restart_api(fault["build_tags"])
                active = fault_active(container, name)
                report = active and run_phase(phase, operation, f"faults-{name}-{phase}.xml", fault["path"])
            if report:
                passed, why = judge(
                    report, fault["path"], faulted=True, known_gap=fault.get("known_gap"), phase=phase,
                    expected_failure=fault.get("expected_failure"),
                )
            else:
                passed, why = False, f"the API did not log '{ACTIVE}' for {name}: the switch did not take"
            print("::endgroup::")
            if not passed:
                print(f"::error::fault {name}: {why}")
            rows.append(f"| {name} | {operation} | {phase} | {why} |")
            ok = ok and passed
        if any(fault["phase"] != PAIRS_PHASE for fault in faults):
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
    sub.add_parser("pairs")
    args = parser.parse_args(argv)
    if args.command == "tags":
        print(all_tags(load()))
        return 0
    if args.command == "pairs":
        return run(None, {PAIRS_PHASE})
    return run(args.api_container)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
