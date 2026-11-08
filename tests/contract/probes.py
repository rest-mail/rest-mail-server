#!/usr/bin/env python3
"""Configure schema-generated probes after the complete examples gate passes.

vertrag 0.29 applies pins to documented examples too. Keep the full examples
run unpinned; this second run holds an opaque signing key, the dates a schema
cannot say must be in the future, an immediate send, and an API key's permissions,
and omits the 4xx transactions whose probes gate.py already ignores. All probes
from successful transactions remain enabled. The first run enforces the full
surface gate.

A fuzz run (a report named fuzz.xml) also holds a delivery's mailbox_id at the
recipient the fixtures make, and discovery's domain at the hosted fixture in a
fuzz-only description. A coverage run does not.

A run narrowed to operations without a pinned field has nothing for that pin to
hold, and Vertrag refuses a pin that matches nothing. Such a run names the fields
in --no-pin=<name,name>; --no-pin alone holds nothing.

Usage: probes.py <vertrag.yml> <out.yml> <report.xml> [--no-pin[=<name,name>]]
"""

import base64
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from urllib.parse import urlsplit

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
# raw_message also reaches restmailDeliver, whose field is the message itself and not
# base64, so the handler cannot read a recipient or a Date from the value above. Only
# restmailDeliver's body has these two fields: a delivery to a mailbox the run has,
# on the documented date. Refusals: recipients_missing, recipients_wrong_type,
# recipient_wrong_type, date_wrong_type.
RECIPIENTS = ["eve@restmail.test"]
DELIVERY_DATE = "Sat, 17 Oct 2026 12:00:00 +0000"
# A schema cannot say a filter must be registered either, and the generator's "x" names
# none. The body refers to a schema with references of its own, so its description holds
# it inline (describedTestFilterBody, internal/api/handlers/pipeline.go), or this would
# not engage. Refusals: filter_name_missing, filter_name_empty, filter_name_wrong_type.
FILTER_NAME = "header_validate"
# Nor can it make a certificate. A current certificate names the probes' own .invalid
# domain, never the fixture admin's domain whose certificates the gateways cache.
# Vertrag prints what it pins, so its key is used for nothing else.
# A hook removes what the probes install, by this issuer
# (remove_the_probe_certificates). Refusals: cert_missing, cert_empty, cert_wrong_type,
# cert_invalid, key_missing, key_empty, key_wrong_type.
PROBE_CERTIFICATE_NAME = "contract-probe-certificate"
# A schema cannot say a mailbox must exist either. Fuzz draws a mailbox_id or an
# address that names nothing and reads the 404 as a disagreement, so the rest of
# delivery is never reached. The fuzz run holds mailbox_id at the recipient the
# fixtures make (fixtures --delivery-recipient). The coverage run does not: it has
# no valid delivery probe to hold, and a pin would stop its two mailbox_id probes.
FIXTURE_PLAN = Path(__file__).resolve().parent / "fixtureplan/defaults.json"


def probe_certificate(common_name=None):
    """Current, matching throwaway material for the isolated certificate domain."""
    domain = json.loads(FIXTURE_PLAN.read_text())["certificate_domain"]
    with tempfile.TemporaryDirectory() as name:
        directory = Path(name)
        key, certificate = directory / "tls.key", directory / "tls.crt"
        subprocess.run(
            ["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
             "-nodes", "-days", "2", "-subj", "/CN=" + (common_name or PROBE_CERTIFICATE_NAME),
             "-addext", "subjectAltName=DNS:" + (common_name or domain["name"]),
             "-keyout", str(key), "-out", str(certificate)],
            check=True, capture_output=True,
        )
        return certificate.read_text(), key.read_text()


def fuzz_pins():
    plan = json.loads(FIXTURE_PLAN.read_text())
    return {"mailbox_id": plan["delivery_recipient"]["id"]}


def hold_discovery_domain(config, out_path):
    location = os.environ.get("CONTRACT_SPEC") or config["spec"]
    if urlsplit(location).scheme in ("http", "https"):
        raise ValueError("discovery fuzz requires the saved description in CONTRACT_SPEC")
    source = Path(location)
    document = yaml.safe_load(source.read_text())
    for item in document.get("paths", {}).values():
        operation = item.get("get", {})
        if operation.get("operationId") != "restmailDiscover":
            continue
        for parameter in operation.get("parameters", []):
            if parameter.get("in") == "query" and parameter.get("name") == "domain":
                plan = json.loads(FIXTURE_PLAN.read_text())
                parameter["schema"]["enum"] = [plan["domain"]]
                if "example" in parameter:
                    parameter["example"] = plan["domain"]
                for example in parameter.get("examples", {}).values():
                    if "value" in example:
                        example["value"] = plan["domain"]
                target = Path(out_path).resolve().with_name("fuzz-openapi.json")
                target.write_text(json.dumps(document))
                config["spec"] = str(target)
                return
        raise ValueError("restmailDiscover has no domain query parameter to hold")


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
        certificate, certificate_key = probe_certificate()
        certificate_domain = json.loads(FIXTURE_PLAN.read_text())["certificate_domain"]
        config.setdefault("fuzz", {}).setdefault("pin-valid", {})["createCertificate"] = {
            "domain_id": certificate_domain["id"],
        }
        pins = {
            "private_key": DKIM_KEY.read_text(),
            "snoozed_until": SNOOZE_UNTIL,
            "scheduled_at": SCHEDULED_AT,
            "expires_at": EXPIRES_AT,
            "permissions": KEY_PERMISSIONS,
            "time_zone": TIME_ZONE,
            "script": SCRIPT,
            "raw_message": RAW_MESSAGE,
            "recipients": RECIPIENTS,
            "date": DELIVERY_DATE,
            "filter_name": FILTER_NAME,
            "cert_pem": certificate,
            "key_pem": certificate_key,
        }
        if Path(report_path).stem == "fuzz":
            pins.update(fuzz_pins())
            hold_discovery_domain(config, out_path)
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
