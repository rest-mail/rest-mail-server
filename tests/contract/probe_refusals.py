"""Send unchanged generated probes whose documented business refusal is not a shape error.

Vertrag 0.42 judges every schema-valid body's 4xx as a finding. These two
operations deliberately refuse valid shapes for database state. Send the exact
request here and check its status, code and published response schema before
ending its engine send. The lost-probe counter still counts that ending;
replayed-probes.jsonl is the evidence that it no longer means an unsent probe.
"""

import hashlib
import json
import os
import sys
import urllib.error
import urllib.request
from collections import Counter
from functools import lru_cache
from pathlib import Path

from jsonschema import Draft202012Validator, FormatChecker


@lru_cache(maxsize=1)
def description(path):
    return json.loads(Path(path).read_text())


def validator(schema):
    document = description(os.environ["CONTRACT_SPEC"])
    return Draft202012Validator(
        {**schema, "components": document["components"]}, format_checker=FormatChecker()
    )


# The refusal of a well-formed report that names no hosted policy domain
# (internal/api/handlers/tlsrpt.go): 422 validation_failed, failing field policies.
NO_HOSTED_DOMAIN = {"policies": "None of the policy domains in the report match a known domain"}


def tls_report_refusal(transaction):
    document = description(os.environ["CONTRACT_SPEC"])
    operation = document["paths"]["/.well-known/smtp-tlsrpt"]["post"]
    schema = operation["requestBody"]["content"]["application/tlsrpt+json"]["schema"]
    body = json.loads(transaction["request"]["body"])
    # Both refusals are 422 validation_failed. A report the published schema
    # accepts must be refused only for naming no hosted domain.
    fields = NO_HOSTED_DOMAIN if validator(schema).is_valid(body) else None
    send_refusal_probe(
        transaction, 422, "validation_failed",
        "External reports may omit policy and policy-domain. An unchanged report with no hosted domain is refused on policies, not rewritten; malformed reports still require validation_failed.",
        fields=fields,
    )


def send_refusal_probe(transaction, status, code, reason, fields=None):
    evidence = Path(os.environ["CONTRACT_PROBE_REPLAYS"])
    document = description(os.environ["CONTRACT_SPEC"])
    operation = next(
        operation for item in document["paths"].values() for operation in item.values()
        if isinstance(operation, dict) and operation.get("operationId") == transaction["operationId"]
    )
    schema = operation["responses"][str(status)]["content"]["application/json"]["schema"]
    original = transaction["request"].get("body") or ""
    request = urllib.request.Request(
        "{}//{}:{}{}".format(transaction["protocol"], transaction["host"], transaction["port"], transaction["fullPath"]),
        data=original.encode() if original else None,
        headers=transaction["request"].get("headers") or {},
        method=transaction["request"].get("method", "POST"),
    )
    try:
        response = urllib.request.urlopen(request, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        actual_status = response.status
        answer = json.load(response)
    actual_code = answer.get("error", {}).get("code")
    if actual_status != status or actual_code != code:
        raise RuntimeError(f"unchanged {transaction['operationId']} probe expected {status} {code}, got {actual_status} {actual_code}")
    actual_fields = (answer.get("error", {}).get("details") or {}).get("fields")
    if fields is not None and actual_fields != fields:
        raise RuntimeError(f"unchanged {transaction['operationId']} probe expected {status} {code} on {fields}, got {actual_fields}")
    validator(schema).validate(answer)
    with evidence.open("a") as output:
        output.write(json.dumps({
            "operation": transaction["operationId"], "transaction": transaction["name"],
            "request_sha256": hashlib.sha256(original.encode()).hexdigest(),
            "status": actual_status, "code": code, "reason": reason,
        }, sort_keys=True) + "\n")
    transaction["skip"] = True


def check_replays(lost_path, evidence_path):
    allowed = {
        "receiveTlsReport": {(422, "validation_failed")},
        "inviteAccount": {(409, "account_not_invitable")},
    }
    ended = Counter(name for name in Path(lost_path).read_text().splitlines() if name in allowed)
    sent = Counter()
    for line in Path(evidence_path).read_text().splitlines():
        receipt = json.loads(line)
        operation = receipt["operation"]
        if operation not in allowed or (receipt["status"], receipt["code"]) not in allowed[operation]:
            raise ValueError("unexpected business-state exception")
        if not receipt.get("reason") or len(receipt.get("request_sha256", "")) != 64:
            raise ValueError("incomplete business-state probe receipt")
        sent[operation] += 1
    if sent != ended:
        raise ValueError(f"business-state probes ended without matching verified sends: ended={dict(ended)}, sent={dict(sent)}")
    print(f"Business-state probes: {sum(sent.values())} unchanged alternate sends match their hook endings: {dict(sent)}")
    return 0


if __name__ == "__main__":
    try:
        if len(sys.argv) != 3:
            raise ValueError("usage: probe_refusals.py <lost-probes.txt> <replayed-probes.jsonl>")
        sys.exit(check_replays(*sys.argv[1:]))
    except (OSError, ValueError, KeyError) as error:
        print(error, file=sys.stderr)
        sys.exit(1)
