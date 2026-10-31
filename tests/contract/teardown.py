#!/usr/bin/env python3
"""The contract run's last phase: the operations that wipe its data (#1334).

resetTestData and restoreTestSnapshot clear every account but the superadmins,
with their credentials and grants: every principal and fixture the other phases
use. The primary run leaves them out (vertrag.yml's exclude-operation-id), and
this phase sends their examples once nothing else needs the data, as the E2E
job's last contract step. A failed exchange fails it as one in the gate would.

The configuration is the run's own, narrowed to these operations and without the
surface gate, which is about the whole API.

Usage: teardown.py <vertrag-run.yml> <out.yml>
"""

import sys

import yaml

OPERATIONS = ("resetTestData", "restoreTestSnapshot")


def configure(config):
    config["exclude-operation-id"] = [
        op for op in config.get("exclude-operation-id") or [] if op not in OPERATIONS
    ]
    config["operation-id"] = list(OPERATIONS)
    config.pop("surface", None)
    # The gate's list of what it could not send is its own.
    config["unproven"] = ".cache/contract/unproven-teardown.json"
    return config


def main(argv):
    if len(argv) != 2:
        sys.exit("usage: teardown.py <vertrag-run.yml> <out.yml>")
    source, out = argv
    with open(source) as stream:
        config = yaml.safe_load(stream)
    with open(out, "w") as stream:
        yaml.safe_dump(configure(config), stream, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
