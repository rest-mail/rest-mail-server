"""Every example pair in openapi-example.json that needs setup is proven (#1237).

A pair whose `requires` names a principal, a body, a limit or a fault cannot be sent
by the live contract run, which provides none of them; the in-process example tests
set each up and prove it. This fails when one of those pairs was proven by no test.
A pair with no `requires` is the live run's to prove, and Vertrag judges it there.

usage: example_proofs.py <openapi-example.json> <proofs.json>
"""
import json
import sys


def unproven(examples, proofs):
    proven = {(op, name) for op, names in proofs.items() for name in names}
    return sorted(
        (op, name, pair["requires"])
        for op, pairs in examples.items()
        for name, pair in pairs.items()
        if pair.get("requires") and (op, name) not in proven
    )


def main(argv):
    examples_path, proofs_path = argv
    with open(examples_path) as f:
        examples = json.load(f)
    try:
        with open(proofs_path) as f:
            proofs = json.load(f)
    except FileNotFoundError:
        print(f"{proofs_path}: no proofs were written; run the in-process example tests with "
              "RESTMAIL_EXAMPLE_PROOFS set")
        return 1
    missing = unproven(examples, proofs)
    for op, name, requires in missing:
        needs = ", ".join(f"{k} {v}" for k, v in sorted(requires.items()))
        print(f"{op}/{name} requires {needs}, and no in-process test proved it")
    needing = sum(1 for pairs in examples.values() for p in pairs.values() if p.get("requires"))
    print(f"{needing - len(missing)} of {needing} examples that need setup are proven in process")
    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
