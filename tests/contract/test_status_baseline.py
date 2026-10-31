"""The documented statuses not proven yet are listed by operation, and the list
only shrinks (#1334)."""

import io
import json
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stdout

import yaml

import status_baseline

ROOT = Path(__file__).resolve().parents[2]
SPEC = {"paths": {"/things/{id}": {
    "get": {"operationId": "getThing", "summary": "Get a thing", "responses": {"200": {}, "404": {}, "422": {}}},
    "delete": {"operationId": "deleteThing", "summary": "Delete a thing", "responses": {"204": {}, "409": {}}},
}}}


def junit(cases):
    body = "".join('<testcase name="%s">%s</testcase>' % (name, "<failure/>" if failed else "")
                   for name, failed in cases)
    return "<testsuites><testsuite>" + body + "</testsuite></testsuites>"


class StatusBaseline(unittest.TestCase):
    def files(self, baseline, cases):
        directory = Path(tempfile.mkdtemp())
        (directory / "openapi.json").write_text(json.dumps(SPEC))
        (directory / "examples.xml").write_text(junit(cases))
        (directory / "baseline.txt").write_text(baseline)
        return [str(directory / name) for name in ("openapi.json", "examples.xml", "baseline.txt")]

    def check(self, baseline, cases):
        out = io.StringIO()
        with redirect_stdout(out):
            code = status_baseline.check(*self.files(baseline, cases))
        return code, out.getvalue()

    def test_unproven_statuses_pass(self):
        code, out = self.check("# comment\ndeleteThing 409\ngetThing 404\n",
                               [("/things/{id} > Get a thing > 200 > application/json", False)])
        self.assertEqual(code, 0, out)
        self.assertIn("2 documented statuses not proven yet", out)

    def test_a_proven_status_must_leave_the_list(self):
        code, out = self.check("getThing 404\n", [("/things/{id} > Get a thing > 404 > application/json", False)])
        self.assertEqual(code, 1)
        self.assertIn("Proven now", out)
        self.assertIn("getThing 404", out)

    def test_a_failed_send_does_not_prove_it(self):
        code, out = self.check("getThing 404\n", [("/things/{id} > Get a thing > 404 > application/json", True)])
        self.assertEqual(code, 0, out)

    def test_an_undocumented_status_or_operation_must_leave_the_list(self):
        for line in ("getThing 500\n", "missingThing 404\n"):
            with self.subTest(line=line):
                code, out = self.check(line, [])
                self.assertEqual(code, 1)
                self.assertIn("Not documented", out)

    def test_the_list_is_sorted_once(self):
        for baseline in ("getThing 404\ndeleteThing 409\n", "getThing 404\ngetThing 404\n"):
            with self.subTest(baseline=baseline):
                code, _ = self.check(baseline, [])
                self.assertEqual(code, 1)

    def test_expand_adds_each_status_to_the_surface(self):
        directory = Path(tempfile.mkdtemp())
        config = directory / "run.yml"
        config.write_text(yaml.safe_dump({"surface": {"exclude": [{"operation": "x", "reason": "r"}]}}))
        (directory / "baseline.txt").write_text("getThing 404\n")
        status_baseline.expand(str(config), str(directory / "baseline.txt"))
        exclude = yaml.safe_load(config.read_text())["surface"]["exclude"]
        self.assertEqual(exclude[1], {"operation": "getThing", "status": "404", "reason": status_baseline.REASON})

    def test_no_status_is_excluded_on_every_operation(self):
        config = yaml.safe_load((ROOT / "vertrag.yml").read_text())
        blanket = [e for e in config["surface"]["exclude"] if "operation" not in e]
        self.assertEqual(blanket, [])
        status_baseline.read(ROOT / "tests/contract/status-baseline.txt")


if __name__ == "__main__":
    unittest.main()
