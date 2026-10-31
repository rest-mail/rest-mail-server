#!/usr/bin/env python3
"""The contract run's pass against the internal mTLS listener (#1334).

checkMailboxAddress and deliverMessage are served only there: the gateways'
machine routes, which take the internal CA's client certificate and no session.
The primary run, against the public listener, leaves them out (vertrag.yml's
exclude-operation-id); this pass sends their examples to the internal listener
with the gateway's certificate, and fails as the gate does.

The configuration is a phase configuration (reports.py), narrowed to these
operations, pointed at the internal listener, without the surface gate, which is
about the whole API, and without the session sign-in the public run makes.

Usage: internal.py <vertrag-internal.yml> <endpoint> <certs dir>
The certs dir holds internal-ca.crt, internal-client.crt and internal-client.key.
"""

from pathlib import Path
import sys

import yaml

OPERATIONS = ("checkMailboxAddress", "deliverMessage")


def configure(config, endpoint, certs):
    certs = Path(certs).resolve()
    config["exclude-operation-id"] = [
        op for op in config.get("exclude-operation-id") or [] if op not in OPERATIONS
    ]
    config["operation-id"] = list(OPERATIONS)
    config.pop("surface", None)
    # The session sign-in is the public listener's; these routes take none.
    config.pop("auth", None)
    config["endpoint"] = endpoint
    transport = dict(config.get("transport") or {})
    transport["ca-cert"] = str(certs / "internal-ca.crt")
    transport["cert"] = str(certs / "internal-client.crt")
    transport["cert-key"] = str(certs / "internal-client.key")
    config["transport"] = transport
    # The gate's list of what it could not send is its own.
    config["unproven"] = ".cache/contract/unproven-internal.json"
    return config


def main(argv):
    if len(argv) != 3:
        sys.exit("usage: internal.py <vertrag-internal.yml> <endpoint> <certs dir>")
    path, endpoint, certs = argv
    for name in ("internal-ca.crt", "internal-client.crt", "internal-client.key"):
        if not (Path(certs) / name).is_file():
            sys.exit(f"the internal listener's {name} is missing from {certs}")
    with open(path) as stream:
        config = yaml.safe_load(stream)
    with open(path, "w") as stream:
        yaml.safe_dump(configure(config, endpoint, certs), stream, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
