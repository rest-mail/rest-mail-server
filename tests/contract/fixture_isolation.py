"""Run own-data selections alone, shuffled and repeated on the same endpoint.

Every child is the ordinary own-data command: it uses the full served
description, real hooks and per-exchange reset/seed, with no server restart.
Receipts keep each JUnit report and the hook's reset trace under isolation/.
"""

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import random
import subprocess
import xml.etree.ElementTree as ET

import own_data

DEFAULT_OPERATIONS = ("getDomain", "createFolder", "releaseHeldMessage")


def schedule(operations, seed=2099, repeats=2):
    if not operations or len(set(operations)) != len(operations):
        raise ValueError("isolation requires distinct operations")
    if any(operation not in own_data.MOVED for operation in operations):
        raise ValueError("isolation selects only own-data operations")
    if repeats < 2:
        raise ValueError("isolation requires at least two shuffled repetitions")
    result = [("alone", operation) for operation in operations]
    rng = random.Random(seed)
    for repeat in range(repeats):
        shuffled = list(operations)
        rng.shuffle(shuffled)
        result.extend((f"shuffle-{repeat + 1}", operation) for operation in shuffled)
    return result


def observations(report, trace, endpoint, *, operation):
    cases = list(ET.parse(report).getroot().iter("testcase"))
    if not cases:
        raise RuntimeError("isolation executed zero exchanges")
    for case in cases:
        if any(case.find(tag) is not None for tag in ("skipped", "failure", "error")):
            raise RuntimeError("isolation exchange did not pass: " + str(case.get("name")))
    resets = [json.loads(line) for line in Path(trace).read_text().splitlines()]
    if len(resets) != len(cases):
        raise RuntimeError(f"isolation has {len(resets)} resets for {len(cases)} exchanges")
    if Counter(reset.get("name") for reset in resets) != Counter(case.get("name") for case in cases):
        raise RuntimeError("isolation reset trace does not name every executed exchange")
    if {reset["origin"] for reset in resets} != {endpoint.rstrip("/")}:
        raise RuntimeError("isolation reset a different server")
    for reset in resets:
        if reset.get("operation") != operation:
            raise RuntimeError(f"isolation expected operation {operation}, got {reset.get('operation')!r}")
    return len(cases)


def run(operations=DEFAULT_OPERATIONS, *, seed=2099, repeats=2, output=Path(".cache/contract/isolation")):
    endpoint = os.environ.get("CONTRACT_ENDPOINT")
    if not endpoint:
        raise ValueError("CONTRACT_ENDPOINT must name the one isolated server")
    output = Path(output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    counts, receipt = {}, []
    receipt_path = output / "receipt.json"
    evidence = {"status": "running", "endpoint": endpoint, "seed": seed, "repeats": repeats, "runs": receipt}
    receipt_path.write_text(json.dumps(evidence, indent=2) + "\n")
    for index, (phase, operation) in enumerate(schedule(operations, seed, repeats)):
        directory = output / f"{index + 1:02d}-{phase}-{operation}"
        directory.mkdir(parents=True, exist_ok=True)
        trace = directory / "resets.jsonl"
        trace.write_text("")
        env = {
            **os.environ,
            "CONTRACT_LOGS": str(directory),
            "CONTRACT_ISOLATION_TRACE": str(trace),
            "CONTRACT_OWN_DATA_OUTPUT": str(directory),
        }
        subprocess.run(["bash", "tests/contract/run.sh", "own-data", "--operation-id", operation], env=env, check=True)
        report = directory / "own-data.xml"
        count = observations(report, trace, endpoint, operation=operation)
        if operation in counts and counts[operation] != count:
            raise RuntimeError(f"{operation}: exchange count changed from {counts[operation]} to {count}")
        counts[operation] = count
        receipt.append({"phase": phase, "operation": operation, "exchanges": count, "report": str(report), "resets": str(trace)})
        receipt_path.write_text(json.dumps(evidence, indent=2) + "\n")
        print(f"isolation: {phase} {operation}: {count} passed, {count} reset/seed pairs", flush=True)
    evidence["status"] = "passed"
    receipt_path.write_text(json.dumps(evidence, indent=2) + "\n")
    print(f"isolation: {len(receipt)} runs, {sum(row['exchanges'] for row in receipt)} exchanges passed on one server", flush=True)
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--operation-id", action="append", dest="operations")
    parser.add_argument("--seed", type=int, default=2099)
    parser.add_argument("--repeats", type=int, default=2)
    args = parser.parse_args()
    return run(args.operations or DEFAULT_OPERATIONS, seed=args.seed, repeats=args.repeats)


if __name__ == "__main__":
    raise SystemExit(main())
