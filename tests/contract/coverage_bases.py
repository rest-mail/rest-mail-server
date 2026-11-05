#!/usr/bin/env python3
"""Name the operations whose coverage probes would start from a different example.

Vertrag's coverage phase builds every valid probe of an operation on one example:
the first by name among its lowest 2xx status (successVariants, in v0.42.0). So a
new example that sorts first, or a changed request in that example, changes what
every one of those probes sends. That is how the "Skipped valid probes" failures
of #1942 began, and neither showed before a whole-API run:

- domainDNSCheck on #1832: dkim_record_differs sorted before found and named a
  domain only the in-process test builds. It answered 404, so its probe was skipped.
- restmailDeliver on #1724: password_at_minimum sorted before updated on
  updateMailbox, and those probes left the account restmailDeliver sends to inactive.

This reads no server, so it does not say a probe will be skipped. It says which
operations to send, and run.sh's narrowed gate gives the verdict:

  python3 tests/contract/coverage_bases.py --base origin/main
  chore contract:bases    (the same selection, sent to a running API)

A narrowed run holds only what it selects, so it can miss a change that harms a
different operation, as updateMailbox's did. Only a whole-API run shows that. A
named operation stays worth reading for that reason, even after its own run passes.

Exit 0: no operation's base changed. Exit 1: at least one did, and each is named.
Exit 2: the examples could not be read.
"""
import argparse
import json
import subprocess
import sys

EXAMPLES = "openapi-example.json"


def coverage_base(examples):
    """The (name, request) coverage starts from, or None without a 2xx example."""
    successes = [(example["response"]["status"], name) for name, example in examples.items()
                 if 200 <= example["response"]["status"] < 300]
    if not successes:
        return None
    name = min(successes)[1]
    return name, examples[name].get("request", {})


def changed_bases(before, after):
    """(operation, base before, base after) for each operation whose base changed."""
    changes = []
    for operation in sorted(after):
        old = coverage_base(before.get(operation, {}))
        new = coverage_base(after[operation])
        if new is not None and old != new:
            changes.append((operation, old, new))
    return changes


def read_examples(ref):
    """The example file at a git ref, or in the working tree when ref is None."""
    if ref is None:
        with open(EXAMPLES, encoding="utf-8") as source:
            return json.load(source)
    shown = subprocess.run(["git", "show", ref + ":" + EXAMPLES], capture_output=True, text=True, check=True)
    return json.loads(shown.stdout)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base", default="origin/main", help="the git ref to compare with (default origin/main)")
    parser.add_argument("--head", help="a git ref to read instead of the working tree")
    parser.add_argument("--flags", action="store_true",
                        help="print only run.sh's selection flags, and exit 0 whatever changed")
    args = parser.parse_args(argv)
    try:
        changes = changed_bases(read_examples(args.base), read_examples(args.head))
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as err:
        print("could not read the examples: %s" % err, file=sys.stderr)
        return 2
    flags = " ".join("--operation-id " + operation for operation, _, _ in changes)
    if args.flags:
        if flags:
            print(flags)
        return 0
    if not changes:
        print("No operation's coverage base changed against %s." % args.base)
        return 0
    print("Coverage would start from a different example on %d operation(s) against %s:" % (len(changes), args.base))
    for operation, old, new in changes:
        print("  %s: %s -> %s, request %s" % (operation, old[0] if old else "(none)", new[0], json.dumps(new[1], sort_keys=True)))
    print("Send them to a running API, which fails on a skipped valid probe:")
    print("  chore contract:run -- " + flags)
    return 1


if __name__ == "__main__":
    sys.exit(main())
