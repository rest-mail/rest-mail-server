#!/usr/bin/env python3
"""Configure schema-generated probes after the complete examples gate passes.

vertrag 0.29 applies pins to documented examples too. Keep the full examples
run unpinned; this second run holds an opaque signing key and a snooze date in
the future, and omits the 4xx transactions whose probes gate.py already ignores.
All probes from successful transactions remain enabled. The first run enforces
the full surface gate.

A run narrowed to operations without a pinned field has nothing for that pin to
hold, and Vertrag refuses a pin that matches nothing. Such a run names the fields
in --no-pin=<name,name>; --no-pin alone holds nothing.

Usage: probes.py <vertrag.yml> <out.yml> <report.xml> [--no-pin[=<name,name>]]
"""

import sys
from pathlib import Path

import yaml
from reports import spec_location

REFUSALS = r" > 4[0-9]{2}( >|$)"
DKIM_KEY = Path(__file__).resolve().parent / "fixtures/dkim-key.pem"
# A schema cannot say a date must be in the future, and the generator's only
# date-time is in 2020, which the server rightly refuses. A hook cannot supply
# this: Vertrag 0.42.0 abandons a probe whose body a hook changed
# (ErrChangedByHook) without counting it, so the valid probes would never be
# sent. The pin is applied before the probe is rendered, so they are.
SNOOZE_UNTIL = "2099-01-01T10:00:00Z"


def main(config_path, out_path, report_path, pin=True, unpinned=()):
    source = Path(config_path).resolve()
    with source.open() as stream:
        config = yaml.safe_load(stream)
    config["spec"] = spec_location(config["spec"], source.parent)
    config["hookfiles"] = str((source.parent / config["hookfiles"]).resolve())
    config.pop("surface", None)
    config.setdefault("exclude-matching", []).append(REFUSALS)
    # Public test fixture: a schema generator cannot construct a signing key.
    if pin:
        pins = {"private_key": DKIM_KEY.read_text(), "snoozed_until": SNOOZE_UNTIL}
        for name in unpinned:
            del pins[name]
        if pins:
            config.setdefault("fuzz", {}).setdefault("pin", {}).update(pins)
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
    dropped = [arg for arg in args if arg.startswith("--no-pin=")]
    args = [arg for arg in args if arg not in dropped]
    if len(args) != 3 or len(dropped) > 1:
        sys.exit("usage: probes.py <config> <out.yml> <report.xml> [--no-pin[=<name,name>]]")
    names = dropped[0].split("=", 1)[1].split(",") if dropped else []
    sys.exit(main(*args, pin=keep_pin, unpinned=names))
