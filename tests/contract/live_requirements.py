"""Prove named anonymous and over-cap examples against a running API (#1237).

Each case keeps the served operation's response schema and headers unchanged.
Only that pair is selected; a missing route, skipped exchange, tool error or
failed validation is unproven, never a successful proof. These are additional
live proofs, not a replacement for the whole-topology examples/surface gate.
"""

import argparse
import copy
import hashlib
import json
import os
import subprocess
import tempfile
import threading
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import yaml

METHODS = ("get", "put", "post", "delete", "patch", "head", "options", "trace")
REQUIREMENTS = {
    "anonymous": {"principal": "anonymous"},
    "over-cap": {"body": "over_cap"},
}


def select_pair(document, operation_id, name, pair, path_values=None):
    selected = copy.deepcopy(document)
    for path, item in document["paths"].items():
        for method in METHODS:
            operation = item.get(method, {})
            if operation.get("operationId") != operation_id:
                continue
            operation = copy.deepcopy(operation)
            status = str(pair["response"]["status"])
            response = operation["responses"][status]
            found = False
            for media in response.get("content", {}).values():
                examples = media.get("examples", {})
                if name in examples:
                    if examples[name].get("x-requires", {}) != pair.get("requires", {}):
                        raise ValueError(
                            f"{operation_id}/{name}: served requirements differ"
                        )
                    media["examples"] = {name: examples[name]}
                    found = True
                else:
                    media.pop("examples", None)
            if not found:
                raise ValueError(
                    f"{operation_id}/{name}: named response absent from served description"
                )
            operation["responses"] = {status: response}
            path_fields = {
                key: copy.deepcopy(value)
                for key, value in item.items()
                if key not in METHODS
            }
            for parameter in [
                *path_fields.get("parameters", []),
                *operation.get("parameters", []),
            ]:
                examples = parameter.get("examples", {})
                if examples:
                    parameter["examples"] = (
                        {name: examples[name]} if name in examples else {}
                    )
                if parameter.get("in") == "path" and parameter["name"] in (
                    path_values or {}
                ):
                    parameter.pop("example", None)
                    parameter["examples"] = {
                        name: {"value": path_values[parameter["name"]]}
                    }
            for media in operation.get("requestBody", {}).get("content", {}).values():
                examples = media.get("examples", {})
                if examples:
                    media["examples"] = (
                        {name: examples[name]} if name in examples else {}
                    )
            selected["paths"] = {path: {**path_fields, method: operation}}
            return selected
    raise ValueError(f"{operation_id}/{name}: operation absent from served description")


def passed(report, status):
    results = report.get("results", [])
    return (
        len(results) == 1
        and results[0].get("status") == "pass"
        and results[0].get("expected") == str(status)
        and results[0].get("actual") == str(status)
        and not results[0].get("errors")
        and not results[0].get("additional")
        and report.get("summary", {}).get("passed") is True
    )


def prove(
    binary,
    endpoint,
    document,
    examples,
    kind,
    output,
    token="",
    body_bytes=32 * 1024 * 1024 + 1,
    operations=(),
    requirement=None,
    header="",
    path_values=None,
    renew_token=None,
    workers=1,
):
    requirement = REQUIREMENTS[kind] if requirement is None else requirement
    output = Path(output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    proof_path = output / f"{kind}-proofs.json"
    proof_path.write_text("{}\n")
    proven, unproven = {}, []
    refreshed = time.monotonic()
    session = threading.Lock()

    def current_token():
        nonlocal token, refreshed
        with session:
            if renew_token and time.monotonic() - refreshed >= 600:
                token = renew_token()
                refreshed = time.monotonic()
            return token

    def prove_case(case):
        operation_id, name, pair = case
        reason = ""
        case_report = output / f"{kind}-{operation_id}-{name}.json"
        case_report.unlink(missing_ok=True)
        try:
            selected = select_pair(document, operation_id, name, pair, path_values)
            # A directory for each case: cases can run side by side.
            case_work = work / f"{operation_id}-{name}"
            case_work.mkdir()
            spec = case_work / "openapi.json"
            spec.write_text(json.dumps(selected))
            report = case_work / "report.json"
            config = {
                "spec": str(spec),
                "endpoint": endpoint,
                "provides": {key: [value] for key, value in requirement.items()},
                "checks": {"header-schema": True},
                "reporter": ["json"],
                "output": [str(report)],
            }
            if "principal" in requirement:
                principal = {"name": requirement["principal"]}
                if header:
                    principal["auth"] = {"header": header}
                config["principals"] = [principal]
            elif header:
                config["header"] = [header]
            if kind == "over-cap":
                session_token = current_token()
                if not session_token:
                    raise ValueError(
                        "over-cap proofs require a current real MFA session"
                    )
                config["header"] = ["Authorization: Bearer " + session_token]
                config["hookfiles"] = str(
                    Path(__file__).with_name("live_requirement_hooks.py")
                )
                config["language"] = "python"
            config_path = case_work / "vertrag.yml"
            descriptor = os.open(
                config_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600
            )
            with os.fdopen(descriptor, "w") as stream:
                yaml.safe_dump(config, stream)
            environment = {
                **os.environ,
                "CONTRACT_OVER_CAP_BYTES": str(body_bytes),
            }
            run = subprocess.run(
                [
                    binary,
                    "run",
                    "--config",
                    str(config_path),
                    "--phases",
                    "examples",
                ],
                check=False,
                capture_output=True,
                timeout=120,
                env=environment,
            )
            # A run that stopped early can leave an empty report: say how it exited.
            written = report.read_text() if report.exists() else ""
            verdict = json.loads(written) if written else {}
            if run.returncode or not passed(verdict, pair["response"]["status"]):
                results = verdict.get("results", [])
                reason = (
                    f"vertrag exit {run.returncode}; expected {pair['response']['status']}; "
                    + "; ".join(
                        f"{result.get('status')}: HTTP {result.get('actual', 'unsent')} "
                        + ", ".join(result.get("errors", []))
                        for result in results
                    )
                )
            case_report.write_text(json.dumps(verdict, indent=2) + "\n")
        except (
            KeyError,
            ValueError,
            OSError,
            subprocess.TimeoutExpired,
        ) as error:
            reason = str(error)
        return operation_id, name, reason

    cases = [
        (operation_id, name, pair)
        for operation_id, pairs in sorted(examples.items())
        if not operations or operation_id in operations
        for name, pair in sorted(pairs.items())
        if pair.get("requires", {}) == requirement
    ]
    with (
        tempfile.TemporaryDirectory(prefix=f"{kind}-", dir=output) as temporary,
        ThreadPoolExecutor(max_workers=workers) as pool,
    ):
        work = Path(temporary)
        for operation_id, name, reason in pool.map(prove_case, cases):
            case = f"{operation_id}/{name}"
            if reason:
                unproven.append(
                    {
                        "operation": operation_id,
                        "example": name,
                        "requires": requirement,
                        "reason": reason,
                    }
                )
                print(f"UNPROVEN {case}: {reason}", flush=True)
            else:
                proven.setdefault(operation_id, []).append(name)
                print(f"PROVEN {case}", flush=True)
    proof_path.write_text(json.dumps(proven, indent=2) + "\n")
    (output / f"{kind}-unproven.json").write_text(json.dumps(unproven, indent=2) + "\n")
    count = sum(map(len, proven.values()))
    (output / f"{kind}-inventory.json").write_text(
        json.dumps(
            {
                "endpoint": endpoint,
                "requires": requirement,
                "path_values": path_values or {},
                "body_bytes": body_bytes if kind == "over-cap" else 0,
                "description_sha256": hashlib.sha256(
                    json.dumps(document, sort_keys=True).encode()
                ).hexdigest(),
                "proven": count,
                "unproven": len(unproven),
            },
            indent=2,
        )
        + "\n"
    )
    print(f"{kind}: {count} live examples proven; {len(unproven)} unproven")
    return 1 if unproven or not count else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kind", choices=REQUIREMENTS)
    parser.add_argument("endpoint")
    parser.add_argument("output")
    parser.add_argument("--vertrag", required=True)
    parser.add_argument("--examples", default="openapi-example.json")
    parser.add_argument("--body-bytes", type=int, default=32 * 1024 * 1024 + 1)
    parser.add_argument("--operation-id", action="append", default=[])
    # A 33 MB body costs about 10 s of CPU in Vertrag's hook worker, nearly all of a
    # case's time, so the over-cap inventory takes most of an hour one at a time.
    parser.add_argument("--workers", type=int, default=1)
    args = parser.parse_args()
    if args.body_bytes <= 0:
        parser.error("--body-bytes must be positive")
    if args.workers <= 0:
        parser.error("--workers must be positive")
    with urllib.request.urlopen(
        args.endpoint.rstrip("/") + "/api/docs/openapi.json", timeout=30
    ) as response:
        document = json.load(response)
    token = ""
    renew_token = None
    if args.kind == "over-cap":
        import mfa_admin

        renew_token = lambda: mfa_admin.session(args.endpoint)
        token = renew_token()
    return prove(
        args.vertrag,
        args.endpoint,
        document,
        json.loads(Path(args.examples).read_text()),
        args.kind,
        args.output,
        token,
        args.body_bytes,
        args.operation_id,
        renew_token=renew_token,
        workers=args.workers,
    )


if __name__ == "__main__":
    raise SystemExit(main())
