#!/usr/bin/env python3
"""gate.py's coverage ratchet, against a report written here.

Run: python3 -m unittest discover -s tests/contract -p 'test_gate.py'
"""
import os
import tempfile
import unittest

import gate

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
    return '<testcase name="%s">%s</testcase>' % (name, body)


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
        report = self.write("examples.xml", "<testsuites><testsuite>%s</testsuite></testsuites>" % "".join(cases))
        base = self.write("baseline.txt", "".join(op + "\n" for op in listed))
        return gate.gate(spec, config, report, base)

    def test_a_probe_built_from_a_refusal_is_not_judged(self):
        cases = [
            case("coverage: /api/v1/admin/aliases > Create an alias > 201 > application/json · body · source_address missing", False),
            case("coverage: /api/v1/admin/aliases > Create an alias > 422 > application/json · body · valid", True),
            case("coverage: /api/v1/admin/aliases > Create an alias > 400 · body · valid", True),
        ]
        self.assertEqual(self.run_gate(cases, []), 0)

    def test_a_failing_probe_built_from_a_success_still_fails(self):
        cases = [
            case("coverage: /api/v1/admin/aliases > Create an alias > 201 > application/json · body · source_address missing", True),
        ]
        self.assertEqual(self.run_gate(cases, []), 1)

    def test_an_operation_probed_only_from_a_refusal_stays_in_the_baseline(self):
        # Its refused probes pass, but they are not judged, so they cannot take
        # it out of the baseline.
        cases = [
            case("coverage: /api/v1/admin/domains > Create a domain > 422 > application/json · body · valid", False),
        ]
        self.assertEqual(self.run_gate(cases, ["createDomain"]), 0)

    def test_a_baseline_operation_whose_probes_pass_must_come_out(self):
        cases = [
            case("coverage: /api/v1/admin/domains > Create a domain > 201 > application/json · body · name missing", False),
        ]
        self.assertEqual(self.run_gate(cases, ["createDomain"]), 1)


if __name__ == "__main__":
    unittest.main()
