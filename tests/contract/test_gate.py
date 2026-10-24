"""gate.py's coverage ratchet, against a report written here.

Run: python3 -m unittest discover -s tests/contract -p 'test_gate.py'
"""

import os
import re
import tempfile
import unittest
from typing import ClassVar

import gate
import yaml

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..")

SPEC = """
paths:
  /api/v1/admin/aliases:
    post:
      operationId: createAlias
      summary: Create an alias
  /api/v1/admin/domains:
    post:
      operationId: createDomain
      summary: Create a domain
"""

CONFIG = "exclude-operation-id: []\n"


def case(name, failed):
    body = '<failure message="refused"/>' if failed else ""
    return f'<testcase name="{name}">{body}</testcase>'


class Gate(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)

    def write(self, name, text):
        path = os.path.join(self.dir.name, name)
        with open(path, "w") as f:
            f.write(text)
        return path

    def run_gate(self, cases, listed):
        spec = self.write("openapi.yaml", SPEC)
        config = self.write("vertrag.yml", CONFIG)
        report = self.write(
            "examples.xml",
            f"<testsuites><testsuite>{''.join(cases)}</testsuite></testsuites>",
        )
        base = self.write("baseline.txt", "".join(op + "\n" for op in listed))
        return gate.gate(spec, config, report, base)

    def test_a_probe_built_from_a_refusal_is_not_judged(self):
        cases = [
            case(
                "coverage: /api/v1/admin/aliases > Create an alias > 201 > application/json · body · source_address missing",
                False,
            ),
            case(
                "coverage: /api/v1/admin/aliases > Create an alias > 422 > application/json · body · valid",
                True,
            ),
            case(
                "coverage: /api/v1/admin/aliases > Create an alias > 400 · body · valid",
                True,
            ),
        ]
        self.assertEqual(self.run_gate(cases, []), 0)

    def test_a_failing_probe_built_from_a_success_still_fails(self):
        cases = [
            case(
                "coverage: /api/v1/admin/aliases > Create an alias > 201 > application/json · body · source_address missing",
                True,
            ),
        ]
        self.assertEqual(self.run_gate(cases, []), 1)

    def test_an_operation_probed_only_from_a_refusal_stays_in_the_baseline(self):
        # Its refused probes pass, but they are not judged, so they cannot take
        # it out of the baseline.
        cases = [
            case(
                "coverage: /api/v1/admin/domains > Create a domain > 422 > application/json · body · valid",
                False,
            ),
        ]
        self.assertEqual(self.run_gate(cases, ["createDomain"]), 0)

    def test_a_baseline_operation_whose_probes_pass_must_come_out(self):
        cases = [
            case(
                "coverage: /api/v1/admin/domains > Create a domain > 201 > application/json · body · name missing",
                False,
            ),
        ]
        self.assertEqual(self.run_gate(cases, ["createDomain"]), 1)


class PairedRefusals(unittest.TestCase):
    """vertrag.yml sends a documented 400 or 422 only where the spec pairs it
    with a request that provokes it (#386), and every one of those."""

    # The operations whose documented 400s and 422s each have their named
    # request example.
    PAIRED: ClassVar[set[str]] = {
        "login",
        "updateAlias",
        "setDomainRelay",
        "setDKIMRotation",
        "sendMessage",
        "microsoftAutodiscover",
        "createDomain",
        "updateDomain",
        "createAlias",
        "createPipeline",
        "updatePipeline",
        "createBan",
        "createCustomFilter",
        "updateCustomFilter",
        "validateCustomFilter",
        "sendTestEmail",
        "createAccount",
        "importContacts",
        "linkAccount",
        "createAppPassword",
        "createFolder",
        "restmailDeliver",
        "bulkDeleteQueue",
        "bulkRetryQueue",
        "bulkBounceQueue",
        "addToAllowlist",
        "addToBlocklist",
        "testFilter",
        "testPipeline",
        "testCustomFilter",
        "receiveTlsReport",
        "createCertificate",
        "protocolSignIn",
        "probeServices",
        "setMtaStsPolicy",
        "setVacation",
        "updateMailbox",
        "putSieveScript",
        "validateSieveScript",
        "createContact",
        "setDKIMKey",
    }

    def test_exclude_matching_sends_exactly_the_paired_refusals(self):
        with open(os.path.join(ROOT, "openapi.yaml")) as source:
            spec = yaml.safe_load(source)
        with open(os.path.join(ROOT, "vertrag.yml")) as source:
            config = yaml.safe_load(source)
        patterns = [re.compile(p) for p in config["exclude-matching"]]
        never = set(config["exclude-operation-id"])
        sent, paired = set(), set()
        for path, item in spec["paths"].items():
            for method in gate.METHODS:
                op = item.get(method)
                if op is None or op["operationId"] in never:
                    continue
                for status in ("400", "422"):
                    if status not in op["responses"]:
                        continue
                    if op["operationId"] in self.PAIRED:
                        paired.add((op["operationId"], status))
                    name = f"{path} > {op['summary']} > {status} > application/json"
                    if not any(p.search(name) for p in patterns):
                        sent.add((op["operationId"], status))
        self.assertEqual({op for op, _ in paired}, self.PAIRED)
        self.assertEqual(sent, paired)

    def test_each_sent_refusal_has_a_named_request_pair(self):
        with open(os.path.join(ROOT, "openapi.yaml")) as f:
            spec = yaml.safe_load(f)
        for item in spec["paths"].values():
            for method in gate.METHODS:
                op = item.get(method)
                if op is None or op["operationId"] not in self.PAIRED:
                    continue
                request_content = op["requestBody"]["content"]
                self.assertEqual(len(request_content), 1)
                requests = next(iter(request_content.values()))
                for status in ("400", "422"):
                    if status not in op["responses"]:
                        continue
                    with self.subTest(operation=op["operationId"], status=status):
                        for response in op["responses"][status]["content"].values():
                            # An unnamed side pairs with every example on the
                            # other side, including requests meant to succeed.
                            self.assertNotIn("example", requests)
                            self.assertNotIn("example", response)
                            examples = response.get("examples", {})
                            self.assertTrue(examples)
                            self.assertLessEqual(
                                set(examples), set(requests.get("examples", {}))
                            )


class MissingResourceRefusals(unittest.TestCase):
    def test_only_enabled_lookups_send_their_documented_404(self):
        with open(os.path.join(ROOT, "openapi.yaml")) as file:
            spec = yaml.safe_load(file)
        with open(os.path.join(ROOT, "vertrag.yml")) as file:
            config = yaml.safe_load(file)
        patterns = [re.compile(pattern) for pattern in config["exclude-matching"]]
        never = set(config["exclude-operation-id"])
        sent = set()
        for path, item in spec["paths"].items():
            for method in gate.METHODS:
                op = item.get(method)
                if (
                    op is None
                    or op["operationId"] in never
                    or "404" not in op["responses"]
                ):
                    continue
                name = f"{path} > {op['summary']} > 404 > application/json"
                if not any(pattern.search(name) for pattern in patterns):
                    sent.add(op["operationId"])
        self.assertEqual(
            sent,
            {
                "getDomain",
                "getCertificate",
                "getAlias",
                "getCustomFilter",
                "getMailbox",
            },
        )


if __name__ == "__main__":
    unittest.main()
