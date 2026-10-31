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


class HeldBackStatuses(unittest.TestCase):
    """vertrag.yml holds back a documented status only when no example proves
    it, or surface.exclude gives the reason it cannot be sent (#1334): each
    exclude-matching pattern names one operation, under a comment with its
    operationId, and exactly that operation's statuses in
    tests/contract/status-baseline.txt, none of which has an example, and in
    surface.exclude. Every other status with an example is sent."""

    PATTERN = re.compile(r"^\^(.+) > (.+) > (\d{3}|\((?:\d{3}\|)+\d{3}\))\( >\|\$\)$")

    def setUp(self):
        with open(os.path.join(ROOT, "vertrag.yml")) as source:
            self.text = source.read()
        self.config = yaml.safe_load(self.text)
        with open(os.path.join(ROOT, "openapi-example.json")) as source:
            self.examples = json.load(source)
        self.baseline = {}
        with open(os.path.join(ROOT, "tests/contract/status-baseline.txt")) as source:
            for line in source:
                line = line.split("#", 1)[0].strip()
                if line:
                    op, status = line.split()
                    self.baseline.setdefault(op, set()).add(status)

    def held(self):
        """operationId -> the statuses its pattern holds back."""
        held = {}
        lines = self.text.split("\n")
        start = lines.index("exclude-matching:")
        for previous, line in zip(lines[start:], lines[start + 1:]):
            if not line.startswith("  - "):
                if line and not line.startswith("  #"):
                    break
                continue
            pattern = yaml.safe_load(line[4:])
            match = self.PATTERN.match(pattern)
            self.assertIsNotNone(match, pattern)
            self.assertTrue(previous.startswith("  # ") and " " not in previous[4:].strip(), pattern)
            op = previous[4:].strip()
            self.assertNotIn(op, held, "one pattern an operation")
            held[op] = set(re.findall(r"\d{3}", match.group(3)))
        return held

    def reasoned(self):
        reasoned = {}
        for entry in self.config["surface"]["exclude"]:
            if "status" in entry:
                reasoned.setdefault(entry["operation"], set()).add(str(entry["status"]))
        return reasoned

    def test_each_pattern_holds_back_its_operations_baseline_or_reasoned_statuses(self):
        expected = {}
        for source in (self.baseline, self.reasoned()):
            for op, statuses in source.items():
                expected.setdefault(op, set()).update(statuses)
        self.assertEqual(self.held(), expected)

    def test_no_baseline_status_has_an_example(self):
        for op, statuses in self.baseline.items():
            for status in statuses:
                with self.subTest(operation=op, status=status):
                    proven = [name for name, pair in self.examples.get(op, {}).items()
                              if str(pair["response"]["status"]) == status]
                    self.assertEqual(proven, [])

    def test_every_pattern_is_in_the_run(self):
        self.assertEqual(len(self.held()), len(self.config["exclude-matching"]))


if __name__ == "__main__":
    unittest.main()
