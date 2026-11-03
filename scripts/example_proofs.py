"""Every example pair in openapi-example.json that needs setup is proven (#1237).

A pair whose `requires` names a principal, a body, a limit or a fault cannot be sent
by the live contract run, which provides none of them; the in-process example tests
set each up and prove it. This fails when one of those pairs was proven by no test.
A pair with no `requires` is the live run's to prove, and Vertrag judges it there,
unless no phase of the live run sends its operation (never_sent): then every pair
of it must be proven in process too (#1334).
CI runs the tests in more than one job, each writing its own proofs file, so a pair
counts as proven when any of the files names it.

--live names the directory of the runners tests/contract/live_proofs.sh runs against
the E2E topology; a pair one of them proved counts as well. The runner's list alone is
not enough: each pair must also have its own passing Vertrag verdict from a run that
finished. Anything else (no result, an empty list, a failed or skipped exchange, a tool
error) counts for nothing, and says why.

--defer-live is for the job that has no live results to read: a pair a live runner
selects and no in-process test proved is left to the job that does (.github/workflows/
e2e.yml, a required check in the merge queue), which fails on it if no runner proved it.

usage: example_proofs.py [--live <directory>] [--defer-live] <openapi-example.json>
       <proofs.json> [<proofs.json> ...]
"""

import argparse
import ast
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# What tests/contract/live_requirements.py selects: its result files, by requirement.
LIVE_REQUIREMENTS = {
    "anonymous": {"principal": "anonymous"},
    "over-cap": {"body": "over_cap"},
}
# The one pair each cell of tests/contract/live_principals.py sends.
PRINCIPAL_CELLS = {
    "admin": ("getDomain", "found"),
    "ordinary": ("getDomain", "outside_domain"),
    "anonymous": ("getDomain", "unauthenticated"),
    "app-password-own": ("listFolders", "found"),
    "app-password-outside": ("listFolders", "key_wrong_scope"),
    "scoped-grant-inside": ("getDomain", "found"),
    "scoped-grant-outside": ("getDomain", "outside_domain"),
    "api-key-inside": ("getDomain", "found"),
    "api-key-outside": ("getDomain", "outside_domain"),
}


def unproven(examples, proofs, never_sent=frozenset()):
    proven = {(op, name) for op, names in proofs.items() for name in names}
    return sorted(
        (op, name, pair.get("requires") or {"sent": "never"})
        for op, pairs in examples.items()
        for name, pair in pairs.items()
        if (pair.get("requires") or op in never_sent) and (op, name) not in proven
    )


def never_sent(root):
    """The operations no phase of the live run sends: vertrag.yml's
    exclude-operation-id, but for those the teardown pass sends
    (#1334). Every example of these must be proven in process."""
    excluded, listing = [], False
    for line in (root / "vertrag.yml").read_text().splitlines():
        if line.startswith("exclude-operation-id:"):
            listing = True
        elif listing and line.startswith("  - "):
            excluded.append(line[4:].strip())
        elif listing and line.strip() and not line.startswith("#"):
            break
    passes = set()
    for name in ("teardown.py",):
        tree = ast.parse((root / "tests/contract" / name).read_text())
        for node in tree.body:
            if isinstance(node, ast.Assign) and any(
                getattr(t, "id", "") == "OPERATIONS" for t in node.targets
            ):
                passes.update(ast.literal_eval(node.value))
    return frozenset(op for op in excluded if op not in passes)


def _read_json(path):
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError):
        return None


def runner_proofs(directory, kind, examples, passed, only=None):
    """The pairs one live run proved, and why anything else it claims does not count."""
    label = f"live runner {directory.name}/{kind}"
    claimed = _read_json(directory / f"{kind}-proofs.json")
    if claimed is None:
        return {}, [f"{label}: no readable result"]
    if not isinstance(claimed, dict) or not all(
        isinstance(names, list) for names in claimed.values()
    ):
        return {}, [f"{label}: its proof list is malformed"]
    pairs = [(op, name) for op, names in claimed.items() for name in names]
    if not pairs:
        return {}, [f"{label}: proved nothing"]
    # Written after the last case, so a run that stopped part way has none.
    inventory = _read_json(directory / f"{kind}-inventory.json")
    unsettled = _read_json(directory / f"{kind}-unproven.json")
    if not isinstance(inventory, dict) or not isinstance(unsettled, list):
        return {}, [f"{label}: did not finish"]
    if inventory.get("proven") != len(pairs):
        return {}, [f"{label}: its inventory disagrees with its proof list"]
    failed = {(item.get("operation"), item.get("example")) for item in unsettled}
    proven, refused = {}, []
    for op, name in pairs:
        pair = examples.get(op, {}).get(name)
        verdict = _read_json(directory / f"{kind}-{op}-{name}.json")
        if pair is None:
            reason = "is not in the examples"
        elif only is not None and (op, name) != only:
            reason = "is not what this run sends"
        elif (pair.get("requires") or {}) != inventory.get("requires"):
            reason = "needs something other than what this run provided"
        elif (op, name) in failed:
            reason = "is also listed as unproven"
        elif verdict is None:
            reason = "has no verdict"
        elif not passed(verdict, pair.get("response", {}).get("status")):
            reason = "has no passing verdict"
        else:
            proven.setdefault(op, []).append(name)
            continue
        refused.append(f"{label}: {op}/{name} {reason}")
    return proven, refused


def live_proofs(root, examples):
    """What every live run under root proved, and what it does not count. Nothing a
    runner failed at is an error here: the pair is simply not proven by it."""
    sys.path.insert(0, str(ROOT / "tests/contract"))
    # Beside the runners' own rule for a proof; this needs PyYAML, so only --live loads it.
    from live_requirements import passed

    root = Path(root)
    runs = [(root / kind, kind, None) for kind in LIVE_REQUIREMENTS]
    runs += [
        (root / "principals", kind, pair) for kind, pair in PRINCIPAL_CELLS.items()
    ]
    proofs, refused = {}, []
    for directory, kind, only in runs:
        proven, reasons = runner_proofs(directory, kind, examples, passed, only)
        refused += reasons
        for op, names in proven.items():
            proofs.setdefault(op, []).extend(names)
    return proofs, refused


def live_selects(op, name, requires):
    return requires in LIVE_REQUIREMENTS.values() or (
        (op, name) in PRINCIPAL_CELLS.values()
    )


def main(argv):
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--live")
    parser.add_argument("--defer-live", action="store_true")
    parser.add_argument("examples_path")
    parser.add_argument("proofs_paths", nargs="*")
    args = parser.parse_args(argv)
    examples_path, proofs_paths = args.examples_path, args.proofs_paths
    with open(examples_path) as f:
        examples = json.load(f)
    if not proofs_paths:
        print(__doc__)
        return 2
    proofs = {}
    for proofs_path in proofs_paths:
        try:
            with open(proofs_path) as f:
                written = json.load(f)
        except FileNotFoundError:
            print(
                f"{proofs_path}: no proofs were written; run the in-process example tests with "
                "RESTMAIL_EXAMPLE_PROOFS set"
            )
            return 1
        for op, names in written.items():
            proofs.setdefault(op, []).extend(names)
    live = {}
    if args.live:
        live, refused = live_proofs(args.live, examples)
        for reason in refused:
            print(reason)
    unsent = never_sent(ROOT)
    missing = unproven(
        examples,
        {op: proofs.get(op, []) + live.get(op, []) for op in {*proofs, *live}},
        unsent,
    )
    deferred = []
    if args.defer_live:
        deferred = [item for item in missing if live_selects(*item)]
        missing = [item for item in missing if item not in deferred]
    who = "no runner" if args.live else "no in-process test"
    for op, name, requires in missing:
        if requires == {"sent": "never"}:
            print(f"{op}/{name}: the live run never sends {op}, and {who} proved it")
            continue
        needs = ", ".join(f"{k} {v}" for k, v in sorted(requires.items()))
        print(f"{op}/{name} requires {needs}, and {who} proved it")
    for op, name, _ in deferred:
        print(
            f"{op}/{name}: no in-process test proved it; left to the live proofs in the E2E job"
        )
    needing = sum(
        1 for pairs in examples.values() for p in pairs.values() if p.get("requires")
    )
    proven = needing - len(missing) - len(deferred)
    if args.live:
        only_live = len(unproven(examples, proofs, unsent)) - len(missing)
        print(
            f"{proven} of {needing} examples that need setup are proven, "
            f"{only_live} of them only live"
        )
    else:
        print(f"{proven} of {needing} examples that need setup are proven in process")
    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
