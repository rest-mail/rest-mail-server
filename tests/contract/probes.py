#!/usr/bin/env python3
"""Configure schema-generated probes after the complete examples gate passes.

vertrag 0.29 applies pins to documented examples too. Keep the full examples
run unpinned; this second run holds an opaque signing key and omits the 4xx
transactions whose probes gate.py already ignores. All probes from successful
transactions remain enabled. The first run enforces the full surface gate.

A run narrowed to operations with no signing key has nothing for the pin to hold,
and Vertrag refuses a pin that matches nothing. Such a run passes --no-pin.

Usage: probes.py <vertrag.yml> <out.yml> <report.xml> [--no-pin]
"""

import sys
from pathlib import Path

import yaml
from reports import spec_location

REFUSALS = r" > 4[0-9]{2}( >|$)"
DKIM_KEY = Path(__file__).resolve().parent / "fixtures/dkim-key.pem"


def main(config_path, out_path, report_path, pin=True):
    source = Path(config_path).resolve()
    with source.open() as stream:
        config = yaml.safe_load(stream)
    config["spec"] = spec_location(config["spec"], source.parent)
    config["hookfiles"] = str((source.parent / config["hookfiles"]).resolve())
    config.pop("surface", None)
    config.setdefault("exclude-matching", []).append(REFUSALS)
    # Public test fixture: a schema generator cannot construct a signing key.
    if pin:
        config.setdefault("fuzz", {}).setdefault("pin", {})["private_key"] = (
            DKIM_KEY.read_text()
        )
    config["reporter"] = ["cli", "junit"]
    config["output"] = ["", str(Path(report_path).resolve())]
    with Path(out_path).open("w") as stream:
        yaml.safe_dump(config, stream, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    args = sys.argv[1:]
    keep_pin = "--no-pin" not in args
    if not keep_pin:
        args.remove("--no-pin")
    if len(args) != 3:
        sys.exit("usage: probes.py <config> <out.yml> <report.xml> [--no-pin]")
    sys.exit(main(*args, pin=keep_pin))
