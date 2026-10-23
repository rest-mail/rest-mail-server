#!/usr/bin/env python3
"""Configure schema-generated probes after the complete examples gate passes.

vertrag 0.29 applies pins to documented examples too. Keep the full examples
run unpinned; this second run holds an opaque signing key and omits the 4xx
transactions whose probes gate.py already ignores. All probes from successful
transactions remain enabled. The first run enforces the full surface gate.

Usage: probes.py <vertrag.yml> <out.yml> <report.xml>
"""
from pathlib import Path
import sys

import yaml

REFUSALS = r" > 4[0-9]{2}( >|$)"
DKIM_KEY = Path(__file__).resolve().parent / "fixtures/dkim-key.pem"


def main(config_path, out_path, report_path):
    source = Path(config_path).resolve()
    with source.open() as stream:
        config = yaml.safe_load(stream)
    config["spec"] = str((source.parent / config["spec"]).resolve())
    config["hookfiles"] = str((source.parent / config["hookfiles"]).resolve())
    config.pop("surface", None)
    config.setdefault("exclude-matching", []).append(REFUSALS)
    # Public test fixture: a schema generator cannot construct a signing key.
    config.setdefault("fuzz", {}).setdefault("pin", {})["private_key"] = DKIM_KEY.read_text()
    config["reporter"] = ["cli", "junit"]
    config["output"] = ["", str(Path(report_path).resolve())]
    with Path(out_path).open("w") as stream:
        yaml.safe_dump(config, stream, sort_keys=False, allow_unicode=True)
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.exit("usage: probes.py <config> <out.yml> <report.xml>")
    sys.exit(main(*sys.argv[1:]))
