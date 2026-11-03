#!/usr/bin/env python3
"""Configure schema-generated probes after the complete examples gate passes.

vertrag 0.29 applies pins to documented examples too. Keep the full examples
run unpinned; this second run holds an opaque signing key, the dates a schema
cannot say must be in the future, an immediate send, and an API key's permissions,
and omits the 4xx transactions whose probes gate.py already ignores. All probes
from successful transactions remain enabled. The first run enforces the full
surface gate.

A fuzz run (a report named fuzz.xml) also holds a delivery's mailbox_id at the
recipient the fixtures make. A coverage run does not.

A run narrowed to operations without a pinned field has nothing for that pin to
hold, and Vertrag refuses a pin that matches nothing. Such a run names the fields
in --no-pin=<name,name>; --no-pin alone holds nothing.

Usage: probes.py <vertrag.yml> <out.yml> <report.xml> [--no-pin[=<name,name>]]
"""

import base64
import json
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
# A send's time must be a minute to a year ahead, so the generator's date is
# refused there too. Null is what the description permits and means "send at
# once", which keeps the success example the 201 it documents: vertrag pins the
# example as well, and any time would make it a 202. sendMessage is the only
# request body with this field. Its named scheduled_at_* examples prove the
# refusals in the examples gate, which is not pinned.
SCHEDULED_AT = None
# The same for the expiry of an API key or an app password.
EXPIRES_AT = "2099-01-01T10:00:00Z"
# A schema-valid permission is a capability "x" at scope "x", and a key may hold
# only a real capability that its maker and its account both hold. The API key
# hooks send these probes as this mailbox's administrator, on its own account
# (mfa_admin.py). The named examples of createAPIKey and updateAPIKey prove the
# refusals a pin stops sending.
KEY_PERMISSIONS = [{"capability": "mail:read", "scope": "mailbox:contract-probe-admin@restmail.test"}]
# A schema cannot say a value must be one the server can parse: an IANA zone, a
# Sieve script, an RFC 5322 message. The generator draws "x" or base64 of "vertrag",
# and a hook cannot supply them. Refusals of these fields are named pairs:
# invalid_zone; invalid_syntax, script_missing, script_null; script_empty,
# script_wrong_type; invalid_base64, missing_raw, raw_message_invalid, raw_message_nul.
TIME_ZONE = "UTC"
SCRIPT = "keep;"
RAW_MESSAGE = base64.b64encode(
    b"Message-ID: <contract-probe@example.test>\r\n"
    b"From: Original <old@example.test>\r\n"
    b"Subject: imported\r\n\r\nhistory\r\n"
).decode()
# A schema cannot say a mailbox must exist either. Fuzz draws a mailbox_id or an
# address that names nothing and reads the 404 as a disagreement, so the rest of
# delivery is never reached. The fuzz run holds mailbox_id at the recipient the
# fixtures make (fixtures --delivery-recipient). The coverage run does not: it has
# no valid delivery probe to hold, and a pin would stop its two mailbox_id probes.
FIXTURE_PLAN = Path(__file__).resolve().parent / "fixtureplan/defaults.json"


def fuzz_pins():
    plan = json.loads(FIXTURE_PLAN.read_text())
    return {"mailbox_id": plan["delivery_recipient"]["id"]}


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
        pins = {
            "private_key": DKIM_KEY.read_text(),
            "snoozed_until": SNOOZE_UNTIL,
            "scheduled_at": SCHEDULED_AT,
            "expires_at": EXPIRES_AT,
            "permissions": KEY_PERMISSIONS,
            "time_zone": TIME_ZONE,
            "script": SCRIPT,
            "raw_message": RAW_MESSAGE,
        }
        if Path(report_path).stem == "fuzz":
            pins.update(fuzz_pins())
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
