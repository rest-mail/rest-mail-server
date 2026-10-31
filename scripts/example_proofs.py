"""Every example pair in openapi-example.json that needs setup is proven (#1237).

A pair whose `requires` names a principal, a body, a limit or a fault cannot be sent
by the live contract run, which provides none of them; the in-process example tests
set each up and prove it. This fails when one of those pairs was proven by no test.
A pair with no `requires` is the live run's to prove, and Vertrag judges it there,
unless no phase of the live run sends its operation (never_sent): then every pair
of it must be proven in process too (#1334).
CI runs the tests in more than one job, each writing its own proofs file, so a pair
counts as proven when any of the files names it.

usage: example_proofs.py <openapi-example.json> <proofs.json> [<proofs.json> ...]
"""

import ast
import json
import sys
from pathlib import Path


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
            if isinstance(node, ast.Assign) and any(getattr(t, "id", "") == "OPERATIONS" for t in node.targets):
                passes.update(ast.literal_eval(node.value))
    return frozenset(op for op in excluded if op not in passes)


def main(argv):
    examples_path, *proofs_paths = argv
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
    missing = unproven(examples, proofs, never_sent(Path(__file__).resolve().parents[1]))
    for op, name, requires in missing:
        if requires == {"sent": "never"}:
            print(f"{op}/{name}: the live run never sends {op}, and no in-process test proved it")
            continue
        needs = ", ".join(f"{k} {v}" for k, v in sorted(requires.items()))
        print(f"{op}/{name} requires {needs}, and no in-process test proved it")
    needing = sum(
        1 for pairs in examples.values() for p in pairs.values() if p.get("requires")
    )
    print(
        f"{needing - len(missing)} of {needing} examples that need setup are proven in process"
    )
    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
