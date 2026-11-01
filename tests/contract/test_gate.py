"""gate.py's coverage ratchet, against a report written here.

Run: python3 -m unittest discover -s tests/contract -p 'test_gate.py'
"""

import json
import os
import re
import tempfile
import unittest

import gate
import yaml

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..")


def topology_spec():
    """The oracle's routes registered when E2E has neither push nor ACME."""
    with open(os.path.join(ROOT, "openapi.yaml")) as source:
        spec = yaml.safe_load(source)
    for path in (
        "/api/v1/devices",
        "/api/v1/devices/{device_id}",
        "/.well-known/acme-challenge/{token}",
    ):
        del spec["paths"][path]
    return spec


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

    def test_repository_exclusions_match_the_e2e_topology(self):
        # Disabled routes are absent from the generated description, rather
        # than operations the run can exclude. Check the real configuration
        # and baseline with those routes absent, without needing a live API.
        spec = self.write("openapi.yaml", yaml.safe_dump(topology_spec()))
        report = self.write("coverage.xml", "<testsuites/>")
        self.assertEqual(
            gate.gate(
                spec,
                os.path.join(ROOT, "vertrag.yml"),
                report,
                os.path.join(ROOT, "tests/contract/coverage-baseline.txt"),
            ),
            0,
        )


class NothingHeldBack(unittest.TestCase):
    """No documented status is left out of the contract run (#1334): vertrag.yml
    has no pattern holding one back and no status excluded from the surface, and
    tests/contract/status-baseline.txt lists none not proven yet. A status the
    run cannot send gets an in-process proof, not an entry here."""

    def setUp(self):
        with open(os.path.join(ROOT, "vertrag.yml")) as source:
            self.config = yaml.safe_load(source)

    def test_no_pattern_holds_a_status_back(self):
        self.assertEqual(self.config.get("exclude-matching") or [], [])

    def test_no_status_is_excluded_from_the_surface(self):
        self.assertEqual(
            [e for e in self.config["surface"]["exclude"] if "status" in e], []
        )

    def test_the_status_baseline_is_empty(self):
        with open(os.path.join(ROOT, "tests/contract/status-baseline.txt")) as source:
            listed = [line for line in source if line.split("#", 1)[0].strip()]
        self.assertEqual(listed, [])


if __name__ == "__main__":
    unittest.main()
